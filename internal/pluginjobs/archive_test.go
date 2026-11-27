package pluginjobs

import (
	"bytes"
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

func TestFileArchiveReclaimsTaskQuotaAndPrivatePayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	q := testQueue(t, path)
	now := time.Date(2026, 11, 24, 1, 0, 0, 123456789, time.UTC)
	in := testInput()
	in.Payload = json.RawMessage(`{"private":"archive removes this payload"}`)
	state := InitialState()
	var original Job
	for i := range MaxPluginJobs {
		item := in
		item.Key = fmt.Sprintf("private-key-%d", i)
		j := capacityJob(t, item, "succeeded")
		state.Jobs[j.ID] = j
		if i == 0 {
			original, in = j, item
		}
	}
	if err := q.Backend.Update(t.Context(), func(s *State) error { *s = state; return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	newInput := in
	newInput.Key = "new-work"
	if _, err = q.Enqueue(t.Context(), newInput, now); !errors.Is(err, ErrCapacity) {
		t.Fatal("fixture is not at the plugin quota", err)
	}
	query := ArchiveQuery{Plugin: in.Plugin, Before: now, Tasks: true, Limit: MaxArchiveBatch}
	wrongPlugin := query
	wrongPlugin.Plugin = "another-plugin"
	if result, err := q.Archive(t.Context(), wrongPlugin, now); err != nil || result.Tasks != 0 {
		t.Fatal("cross-plugin archive", result, err)
	}
	result, err := q.Archive(t.Context(), query, now)
	if err != nil || result.Tasks != MaxPluginJobs {
		t.Fatal("archive failed", result, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) >= len(before) {
		t.Fatalf("archive did not release payload bytes: before=%d after=%d", len(before), len(after))
	}
	for _, secret := range []string{"archive removes this payload", "private-key-", `"payload"`, `"actor_id"`, `"epoch"`, `"lease_token"`} {
		if bytes.Contains(after, []byte(secret)) {
			t.Fatalf("compact history retained %q", secret)
		}
	}
	q = testQueue(t, path)
	replay, err := q.Enqueue(t.Context(), in, now)
	if err != nil || replay.ID != original.ID || replay.Status != "succeeded" || !replay.ArchivedAt.Equal(now) || replay.Fingerprint != original.Fingerprint {
		t.Fatal("archived receipt could not replay after reopen", replay, err)
	}
	changed := in
	changed.ActorID++
	if _, err = q.Enqueue(t.Context(), changed, now); !errors.Is(err, ErrConflict) {
		t.Fatal("archived fingerprint did not bind the actor", err)
	}
	changed = in
	changed.Payload = json.RawMessage(`{"different":true}`)
	if _, err = q.Enqueue(t.Context(), changed, now); !errors.Is(err, ErrConflict) {
		t.Fatal("archived fingerprint did not bind the payload", err)
	}
	page, err := q.List(t.Context(), Query{Plugin: in.Plugin, Archived: true, Limit: 2, Offset: 1})
	if err != nil || page.Total != MaxPluginJobs || len(page.Items) != 2 || page.Items[0].ArchivedAt.IsZero() {
		t.Fatal("archived summaries missing", page, err)
	}
	if page, err := q.List(t.Context(), Query{Plugin: in.Plugin}); err != nil || page.Total != 0 {
		t.Fatal("archived tasks still consume the live list", page.Total, err)
	}
	if err = q.Change(t.Context(), in.Plugin, original.ID, original.Version, "cancel", now); !errors.Is(err, ErrConflict) {
		t.Fatal("archived receipt accepted a state transition", err)
	}
	if err = q.CheckClaim(t.Context(), original, now); !errors.Is(err, ErrAuthorization) {
		t.Fatal("archived receipt authorized a claim", err)
	}
	newJob, err := q.Enqueue(t.Context(), newInput, now)
	if err != nil {
		t.Fatal("archive did not reclaim admission quota", err)
	}
	claim, ok, err := q.Claim(t.Context(), now)
	if err != nil || !ok || claim.ID != newJob.ID {
		t.Fatal("archive replay created executable old work", claim.ID, ok, err)
	}
}

func TestFileArchivedFanoutsPreserveFrozenCohortsAndAckRetries(t *testing.T) {
	for _, count := range []int{0, 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.json")
			q := testQueue(t, path)
			now := time.Date(2026, 11, 24, 0, 0, 0, 123456789, time.UTC)
			event := Event{Key: "archive-event", Kind: "content.updated", ObjectID: 1, Version: 2, At: now}
			subs := []Subscription{}
			if count != 0 {
				subs = append(subs, Subscription{Plugin: "reader", Epoch: strings.Repeat("a", 64), Since: now, ActorID: 1})
			}
			if err := DeliverFrozen(t.Context(), q, q, event, subs, now); err != nil {
				t.Fatal(err)
			}
			incomplete := event
			incomplete.Key = "not-complete"
			if _, err := q.FreezeFanout(t.Context(), incomplete, subs); err != nil {
				t.Fatal(err)
			}
			result, err := q.Archive(t.Context(), ArchiveQuery{Before: now, Fanouts: true}, now.Add(time.Second))
			if err != nil || result.Fanouts != 1 {
				t.Fatal("archive moved an incomplete fanout", result, err)
			}
			q = testQueue(t, path)
			state, err := q.Backend.Read(t.Context())
			if err != nil || len(state.Fanouts) != 1 || len(state.ArchivedFanouts) != 1 {
				t.Fatal("fanout move did not persist", err)
			}
			newSubs := []Subscription{{Plugin: "new-reader", Epoch: strings.Repeat("b", 64), Since: now, ActorID: 2}}
			frozen, err := q.FreezeFanout(t.Context(), event, newSubs)
			if err != nil || !frozen.Delivered || !reflect.DeepEqual(frozen.Subscriptions, subs) {
				t.Fatal("archive rediscovered an old cohort", frozen, err)
			}
			if err = DeliverFrozen(t.Context(), q, q, event, newSubs, now.Add(2*time.Second)); err != nil {
				t.Fatal("lost ACK could not replay archived delivery", err)
			}
			if page, err := q.List(t.Context(), Query{Plugin: "new-reader"}); err != nil || page.Total != 0 {
				t.Fatal("old event admitted a new recipient", err)
			}
			if err = q.CompleteFanout(t.Context(), event); err != nil {
				t.Fatal("completion retry failed after archive", err)
			}
			changed := event
			changed.Version++
			if _, _, err := q.FindFanout(t.Context(), changed); !errors.Is(err, ErrConflict) {
				t.Fatal("archived event content was not bound", err)
			}
			if _, err := q.FreezeFanout(t.Context(), changed, nil); !errors.Is(err, ErrConflict) {
				t.Fatal("changed archived event could be frozen", err)
			}
			if err := q.CompleteFanout(t.Context(), changed); !errors.Is(err, ErrConflict) {
				t.Fatal("changed archived event could be completed", err)
			}
			// Returned cohorts are detached from durable history.
			if count != 0 {
				frozen.Subscriptions[0].Plugin = "tampered"
			}
			if receipt, found, err := q.FindFanout(t.Context(), event); err != nil || !found || !reflect.DeepEqual(receipt.Subscriptions, subs) {
				t.Fatal("caller changed archived subscriptions", err)
			}
			if err = q.CancelRestored(t.Context(), now.Add(3*time.Second)); err != nil {
				t.Fatal(err)
			}
			q = testQueue(t, path)
			if receipt, found, err := q.FindFanout(t.Context(), event); err != nil || !found || !receipt.Cancelled || !receipt.Delivered {
				t.Fatal("restore dropped archived delivery facts", receipt, err)
			}
		})
	}
}

func TestFileArchiveReclaimsFanoutQuota(t *testing.T) {
	q := testQueue(t, "")
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	state := InitialState()
	state.Fanouts = map[string]FanoutReceipt{}
	for i := range MaxFanouts {
		event := Event{Key: fmt.Sprintf("old-%05d", i), Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
		state.Fanouts[event.Key] = FanoutReceipt{Event: event, Subscriptions: []Subscription{}, Delivered: true}
	}
	if err := q.Backend.Update(t.Context(), func(s *State) error { *s = state; return nil }); err != nil {
		t.Fatal(err)
	}
	event := Event{Key: "new-event", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	if _, err := q.FreezeFanout(t.Context(), event, nil); !errors.Is(err, ErrCapacity) {
		t.Fatal("fixture is not at the fanout quota", err)
	}
	if result, err := q.Archive(t.Context(), ArchiveQuery{Before: now, Fanouts: true, Limit: 1}, now); err != nil || result.Fanouts != 1 {
		t.Fatal(result, err)
	}
	if _, err := q.FreezeFanout(t.Context(), event, nil); err != nil {
		t.Fatal("archive did not release fanout capacity", err)
	}
	old := event
	old.Key = "old-00000"
	if replay, err := q.FreezeFanout(t.Context(), old, nil); err != nil || !replay.Delivered {
		t.Fatal("capacity blocked archived fanout replay", replay, err)
	}
}

func TestArchiveEligibilityFailureAndLegacyUpgrade(t *testing.T) {
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	state := InitialState()
	state.Schema = 1
	for _, status := range []string{"pending", "retry", "failed", "succeeded", "cancelled"} {
		in := testInput()
		in.Key = status
		j := capacityJob(t, in, status)
		state.Jobs[j.ID] = j
	}
	q := &Queue{Backend: failedCommitBackend{state: state, failure: errors.New("disk full")}}
	if result, err := q.Archive(t.Context(), ArchiveQuery{Before: now, Tasks: true}, now); err == nil || result != (ArchiveResult{}) {
		t.Fatal("uncommitted archive reported capacity reclamation", result, err)
	}
	path := filepath.Join(t.TempDir(), "legacy.json")
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	q = testQueue(t, path)
	for _, query := range []ArchiveQuery{
		{}, {Before: now.Add(time.Second), Tasks: true}, {Before: now, Tasks: true, Limit: -1},
		{Before: now, Tasks: true, Limit: MaxArchiveBatch + 1}, {Before: now, Fanouts: true, Plugin: "example"},
	} {
		if result, err := q.Archive(t.Context(), query, now); !errors.Is(err, ErrInvalid) || result != (ArchiveResult{}) {
			t.Fatal("invalid archive query", query, result, err)
		}
	}
	result, err := q.Archive(t.Context(), ArchiveQuery{Before: now, Tasks: true, Limit: 1}, now)
	if err != nil || result.Tasks != 1 {
		t.Fatal("bounded archive did not move one terminal record", result, err)
	}
	result, err = q.Archive(t.Context(), ArchiveQuery{Before: now, Tasks: true}, now)
	if err != nil || result.Tasks != 1 {
		t.Fatal("bounded archive lost the next terminal record", result, err)
	}
	q = testQueue(t, path)
	s, err := q.Backend.Read(t.Context())
	if err != nil || s.Schema != 2 || len(s.Jobs) != 3 || len(s.ArchivedJobs) != 2 {
		t.Fatal("legacy upgrade or eligibility failed", err)
	}
	for _, j := range s.Jobs {
		if j.Status != "pending" && j.Status != "retry" && j.Status != "failed" {
			t.Fatal("archive eligibility moved the wrong state", j.Status)
		}
	}
	for id, archived := range s.ArchivedJobs {
		corrupt := cloneState(s)
		in := testInput()
		in.Key = archived.Status
		corrupt.Jobs[id] = capacityJob(t, in, archived.Status)
		if err := ValidateState(corrupt); err == nil {
			t.Fatal("accepted duplicate active and archived identities")
		}
		archived.Status = "failed"
		if err := ValidateArchivedJob(archived); err == nil {
			t.Fatal("accepted executable state in immutable history")
		}
		break
	}
}

func TestConcurrentFileArchiveAndReplayNeverAdmitOldWork(t *testing.T) {
	q := testQueue(t, "")
	in := testInput()
	now := time.Now().UTC()
	j, err := q.Enqueue(t.Context(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Change(t.Context(), j.Plugin, j.ID, j.Version, "cancel", now); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 24)
	start := make(chan struct{})
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				_, err := q.Archive(t.Context(), ArchiveQuery{Tasks: true, Before: now}, now)
				failures <- err
				return
			}
			replay, err := q.Enqueue(t.Context(), in, now)
			if err == nil && (replay.ID != j.ID || replay.Status != "cancelled") {
				err = errors.New("replay became executable")
			}
			failures <- err
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := q.Backend.Read(t.Context())
	if err != nil || len(s.Jobs) != 0 || len(s.ArchivedJobs) != 1 {
		t.Fatal("concurrent archive lost or duplicated an identity", err)
	}
}
