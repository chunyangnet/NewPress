package pluginjobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func cancelledReviewSource(t *testing.T, q *Queue, now time.Time) (Job, ReissueRequest) {
	t.Helper()
	in := testInput()
	in.ActorID = 7
	in.Kind = "task.execute.v1"
	in.Key = "private-source"
	in.Payload = json.RawMessage(`{"private":"old source parameters"}`)
	source, err := q.Enqueue(t.Context(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.CancelRestored(t.Context(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	state, err := q.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	source = state.Jobs[source.ID]
	fresh := in
	fresh.Key = "deliberate-reissue"
	fresh.Payload = json.RawMessage(`{"public":"explicitly reviewed new parameters"}`)
	return source, ReissueRequest{SourceID: source.ID, SourceVersion: source.Version, Input: fresh, Reason: "Checked completed work before reissuing", AllowNew: true}
}

func TestFileReissueSurvivesReopenArchiveAndRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	q := testQueue(t, path)
	now := time.Date(2026, 11, 24, 0, 0, 0, 123456789, time.UTC)
	source, request := cancelledReviewSource(t, q, now)
	child, err := q.Reissue(t.Context(), request, now.Add(2*time.Second))
	if err != nil || child.ID == source.ID || child.Status != "pending" || child.Reissue == nil || child.Reissue.SourceID != source.ID || child.Reissue.SourceVersion != source.Version {
		t.Fatal("reviewed child", child, err)
	}
	state, err := q.Backend.Read(t.Context())
	if err != nil || !reflect.DeepEqual(state.Jobs[source.ID], source) {
		t.Fatal("reissue modified source", err)
	}
	stored := state.Jobs[child.ID]
	if stored.Review.ReviewerID != request.Input.ActorID || stored.Review.Reason != request.Reason || stored.Review.SourceFingerprint != source.Fingerprint || string(stored.Payload) != string(request.Input.Payload) {
		t.Fatal("lost durable review or explicit parameters")
	}
	stored.Review.Reason = "mutating a read copy"
	child.Reissue.SourceID = strings.Repeat("f", 64)
	details, err := q.GetReview(t.Context(), request.Input.Plugin, stored.ID)
	if err != nil || details.Provenance == nil || details.Provenance.Reason != request.Reason || details.Provenance.SourceID != source.ID {
		t.Fatal("read copy mutated private provenance", details, err)
	}
	q = testQueue(t, path)
	request.Input.Epoch = strings.Repeat("b", 64)
	request.AllowNew = false
	replay, err := q.Reissue(t.Context(), request, now.Add(3*time.Second))
	if err != nil || replay.ID != stored.ID || replay.Version != stored.Version {
		t.Fatal("epoch change admitted another request", replay, err)
	}
	if err = q.CancelRestored(t.Context(), now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if result, err := q.Archive(t.Context(), ArchiveQuery{Tasks: true, Before: now.Add(4 * time.Second)}, now.Add(5*time.Second)); err != nil || result.Tasks != 2 {
		t.Fatal("archive child and source", result, err)
	}
	q = testQueue(t, path)
	replay, err = q.Reissue(t.Context(), request, now.Add(6*time.Second))
	if err != nil || replay.ID != stored.ID || replay.Status != "cancelled" || replay.ArchivedAt.IsZero() || replay.Reissue == nil || replay.Reissue.SourceID != source.ID {
		t.Fatal("archived reissue replay", replay, err)
	}
	if _, ok, err := q.Claim(t.Context(), now.Add(6*time.Second)); err != nil || ok {
		t.Fatal("old reissue became runnable", ok, err)
	}
	if err = q.CancelRestored(t.Context(), now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	details, err = q.GetReview(t.Context(), request.Input.Plugin, stored.ID)
	if err != nil || details.Provenance == nil || details.Provenance.ReviewerID != request.Input.ActorID || details.Provenance.Reason != request.Reason {
		t.Fatal("restore lost provenance", details, err)
	}
	raw, err := json.Marshal(replay)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{request.Reason, "explicitly reviewed new parameters", "private-source", `"reviewer_id"`, `"actor_id"`, `"payload"`, `"epoch"`, `"fingerprint"`, `"request_id"`} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("ordinary summary leaked %q", private)
		}
	}
	state, err = q.Backend.Read(t.Context())
	if err != nil || state.Schema != CurrentSchema || len(state.Jobs) != 0 || len(state.ArchivedJobs) != 2 {
		t.Fatal("archive downgraded schema or created work", state.Schema, err)
	}
	archive := state.ArchivedJobs[stored.ID]
	archive.Review.Reason = "mutating an archive read"
	archive.Reissue.SourceID = strings.Repeat("d", 64)
	details, err = q.GetReview(t.Context(), request.Input.Plugin, stored.ID)
	if err != nil || details.Provenance.Reason != request.Reason || details.Task.Reissue.SourceID != source.ID {
		t.Fatal("archived read aliases backend", err)
	}
}

func TestFileReissueFingerprintConflictsDoNotChangeAcceptedRequest(t *testing.T) {
	q := testQueue(t, "")
	now := time.Now().UTC()
	_, request := cancelledReviewSource(t, q, now)
	first, err := q.Reissue(t.Context(), request, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ReissueRequest){
		"payload":        func(r *ReissueRequest) { r.Input.Payload = json.RawMessage(`{"different":true}`) },
		"reason":         func(r *ReissueRequest) { r.Reason = "different decision" },
		"reviewer":       func(r *ReissueRequest) { r.Input.ActorID++ },
		"source-version": func(r *ReissueRequest) { r.SourceVersion++ },
		"kind":           func(r *ReissueRequest) { r.Input.Kind = "event.deliver.v1" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			change(&changed)
			if _, err := q.Reissue(t.Context(), changed, now.Add(3*time.Second)); !errors.Is(err, ErrConflict) {
				t.Fatal("changed command reused receipt", err)
			}
		})
	}
	request.Input.Epoch = ""
	request.AllowNew = false
	if replay, err := q.Reissue(t.Context(), request, now.Add(4*time.Second)); err != nil || replay.ID != first.ID {
		t.Fatal("denied new authorization lost prior acceptance", replay, err)
	}
	state, err := q.Backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 2 {
		t.Fatal("conflicts created extra work", err)
	}
}

func TestFileReissueInvalidSourceOrReviewIsAtomic(t *testing.T) {
	q := testQueue(t, "")
	now := time.Now().UTC()
	source, request := cancelledReviewSource(t, q, now)
	before, _ := q.Backend.Read(t.Context())
	for name, change := range map[string]func(*ReissueRequest){
		"missing source":           func(r *ReissueRequest) { r.SourceID = strings.Repeat("f", 64) },
		"stale version":            func(r *ReissueRequest) { r.SourceVersion-- },
		"other plugin":             func(r *ReissueRequest) { r.Input.Plugin = "other" },
		"other kind":               func(r *ReissueRequest) { r.Input.Kind = "event.deliver.v1" },
		"anonymous reviewer":       func(r *ReissueRequest) { r.Input.ActorID = 0 },
		"no key":                   func(r *ReissueRequest) { r.Input.Key = "" },
		"source identity reused":   func(r *ReissueRequest) { r.Input.Key = source.Key },
		"empty reason":             func(r *ReissueRequest) { r.Reason = " \n " },
		"long reason":              func(r *ReissueRequest) { r.Reason = strings.Repeat("x", MaxReviewReasonBytes+1) },
		"encoded reason":           func(r *ReissueRequest) { r.Reason = strings.Repeat("<", MaxReviewReasonBytes) },
		"control reason":           func(r *ReissueRequest) { r.Reason = "invalid\x00reason" },
		"missing explicit payload": func(r *ReissueRequest) { r.Input.Payload = nil },
		"duplicate json key":       func(r *ReissueRequest) { r.Input.Payload = json.RawMessage(`{"x":1,"x":2}`) },
		"invalid epoch":            func(r *ReissueRequest) { r.Input.Epoch = "bad" },
		"new authorization denied": func(r *ReissueRequest) { r.AllowNew = false },
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			change(&changed)
			if _, err := q.Reissue(t.Context(), changed, now.Add(2*time.Second)); err == nil {
				t.Fatal("invalid reissue accepted")
			}
			after, err := q.Backend.Read(t.Context())
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed reissue modified storage", err)
			}
		})
	}
	if _, err := q.GetReview(t.Context(), "other", source.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("cross-plugin review returned source", err)
	}
	if err := q.Backend.Update(t.Context(), func(s *State) error { j := s.Jobs[source.ID]; j.Status = "succeeded"; s.Jobs[j.ID] = j; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Reissue(t.Context(), request, now.Add(2*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("successful source accepted reissue", err)
	}
}

func TestFileReissueFailedSourceVersionRace(t *testing.T) {
	q := testQueue(t, "")
	now := time.Now().UTC()
	source, request := cancelledReviewSource(t, q, now)
	if err := q.Backend.Update(t.Context(), func(s *State) error {
		j := s.Jobs[source.ID]
		j.Status = "failed"
		j.ErrorCode = "execution_failed"
		s.Jobs[j.ID] = j
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first, err := q.Reissue(t.Context(), request, now.Add(2*time.Second))
	if err != nil || first.Reissue.SourceStatus != "failed" {
		t.Fatal("failed source was not reviewable", err)
	}
	if err = q.Change(t.Context(), source.Plugin, source.ID, source.Version, "retry", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.Input.Key = "another-review"
	if _, err = q.Reissue(t.Context(), changed, now.Add(4*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("source retry did not fence reviewed version", err)
	}
	if replay, err := q.Reissue(t.Context(), request, now.Add(4*time.Second)); err != nil || replay.ID != first.ID {
		t.Fatal("source transition lost accepted receipt", err)
	}
}

func TestFileReissueQuotaRefusalLeavesNoReceipt(t *testing.T) {
	q := testQueue(t, "")
	now := time.Now().UTC()
	source, request := cancelledReviewSource(t, q, now)
	if err := q.Backend.Update(t.Context(), func(s *State) error {
		for i := 0; i < MaxPluginJobs-1; i++ {
			in := request.Input
			in.Key = fmt.Sprintf("other-%d", i)
			id, fp, err := identity(in)
			if err != nil {
				return err
			}
			s.Jobs[id] = newJob(in, id, fp, now)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Reissue(t.Context(), request, now.Add(2*time.Second)); !errors.Is(err, ErrCapacity) {
		t.Fatal("full quota accepted reissue", err)
	}
	if result, err := q.Archive(t.Context(), ArchiveQuery{Tasks: true, Before: now.Add(time.Second)}, now.Add(2*time.Second)); err != nil || result.Tasks != 1 {
		t.Fatal("source archive did not free capacity", result, err)
	}
	first, err := q.Reissue(t.Context(), request, now.Add(3*time.Second))
	if err != nil || first.Reissue.SourceID != source.ID {
		t.Fatal("unaccepted request was incorrectly remembered", first, err)
	}
	request.AllowNew = false
	if replay, err := q.Reissue(t.Context(), request, now.Add(4*time.Second)); err != nil || replay.ID != first.ID {
		t.Fatal("capacity blocked accepted replay", err)
	}
}

func TestConcurrentFileReissueArchiveAndRestore(t *testing.T) {
	q := testQueue(t, filepath.Join(t.TempDir(), "tasks.json"))
	now := time.Now().UTC()
	source, request := cancelledReviewSource(t, q, now)
	var wg sync.WaitGroup
	results := make(chan Summary, 24)
	errs := make(chan error, 26)
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := q.Reissue(t.Context(), request, now.Add(2*time.Second))
			results <- result
			errs <- err
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := q.Archive(t.Context(), ArchiveQuery{Tasks: true, Before: now.Add(time.Second)}, now.Add(2*time.Second))
		errs <- err
	}()
	wg.Add(1)
	go func() { defer wg.Done(); errs <- q.CancelRestored(t.Context(), now.Add(2*time.Second)) }()
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("concurrent operation", err)
		}
	}
	var id string
	for result := range results {
		if id == "" {
			id = result.ID
		}
		if id != result.ID || result.Reissue == nil || result.Reissue.SourceID != source.ID {
			t.Fatal("multiple reviewed children", result)
		}
	}
	state, err := q.Backend.Read(t.Context())
	if err != nil || len(state.Jobs)+len(state.ArchivedJobs) != 2 {
		t.Fatal("concurrent reissue duplicated work", err)
	}
}

func TestFileReissueLegacyUpgradeAndCorruptReview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	q := testQueue(t, "")
	now := time.Now().UTC()
	_, request := cancelledReviewSource(t, q, now)
	state, _ := q.Backend.Read(t.Context())
	state.Schema = 1
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	q = testQueue(t, path)
	child, err := q.Reissue(t.Context(), request, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	state, err = q.Backend.Read(t.Context())
	if err != nil || state.Schema != CurrentSchema {
		t.Fatal("legacy reissue failed to upgrade", err)
	}
	for _, schema := range []int{1, 2} {
		bad := cloneState(state)
		bad.Schema = schema
		if err := ValidateState(bad); err == nil {
			t.Fatal("older schema accepted new review fields")
		}
	}
	for name, change := range map[string]func(*ReissueProvenance){
		"request":  func(r *ReissueProvenance) { r.RequestID = strings.Repeat("d", 64) },
		"reviewer": func(r *ReissueProvenance) { r.ReviewerID++ },
		"reason":   func(r *ReissueProvenance) { r.Reason = "corrupt reason" },
		"time":     func(r *ReissueProvenance) { r.ReviewedAt = r.ReviewedAt.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := cloneState(state)
			change(bad.Jobs[child.ID].Review)
			if err := ValidateState(bad); err == nil {
				t.Fatal("corrupt review accepted")
			}
		})
	}
	if err = q.CancelRestored(t.Context(), now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Archive(t.Context(), ArchiveQuery{Tasks: true, Before: now.Add(3 * time.Second)}, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	state, _ = q.Backend.Read(t.Context())
	bad := cloneState(state)
	bad.ArchivedJobs[child.ID].Review.ReviewerID++
	if err := ValidateState(bad); err == nil {
		t.Fatal("archived reviewer tampering accepted")
	}
}
