package cdn

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
)

func snapshotJob(t *testing.T, s *Service, id string) Job {
	t.Helper()
	status, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range status.Jobs {
		if job.ID == id {
			return job
		}
	}
	t.Fatal("job missing from status")
	return Job{}
}

func snapshotBytes(t *testing.T, s *Service) []byte {
	t.Helper()
	var raw []byte
	if err := s.manager.WithData(plugin.CDN, false, 0, func(dir string, _ uint64) error {
		var err error
		raw, err = os.ReadFile(filepath.Join(dir, "state.json"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestJobViewsCannotChangeValidatedPurgeTargets(t *testing.T) {
	for _, source := range []string{"enqueue", "duplicate", "status"} {
		t.Run(source, func(t *testing.T) {
			s, m, settings := testService(t)
			const target = "https://www.example.com/approved"
			job := enqueue(t, s, settings, "purge_url", target)
			var exposed Job
			switch source {
			case "enqueue":
				exposed = job
			case "duplicate":
				exposed = enqueue(t, s, settings, "purge_url", target)
				if exposed.ID != job.ID {
					t.Fatal("duplicate task was not deduplicated")
				}
			case "status":
				exposed = snapshotJob(t, s, job.ID)
			}
			if exposed.Identity != "" || exposed.RunToken != "" {
				t.Fatal("public task exposes private identity")
			}
			before := snapshotBytes(t, s)
			exposed.Targets[0] = "https://outside.invalid/unapproved"
			if got := snapshotJob(t, s, job.ID).Targets[0]; got != target {
				t.Errorf("changing the %s result changed queued target to %q", source, got)
			}
			if !bytes.Equal(before, snapshotBytes(t, s)) {
				t.Fatal("editing a returned value wrote the durable state")
			}
			var executed Job
			s.execute = func(_ context.Context, _ Profile, got Job) (Receipt, error) {
				executed = got
				return accepted("purge-receipt"), nil
			}
			s.dispatch()
			s.wg.Wait()
			if len(executed.Targets) != 1 || executed.Targets[0] != target {
				t.Errorf("worker received targets changed outside validation: %v", executed.Targets)
			}
			reopened := New(m, s.publicURL)
			defer reopened.Close()
			saved := snapshotJob(t, reopened, job.ID)
			if saved.Status != "accepted" || saved.Targets[0] != target {
				t.Errorf("unapproved target became durable: status=%s targets=%v", saved.Status, saved.Targets)
			}
		})
	}
}

func TestBenchmarkRequestAndReturnedJobsOwnTheirData(t *testing.T) {
	for _, source := range []string{"request", "enqueue", "status"} {
		t.Run(source, func(t *testing.T) {
			s, m, settings := testService(t)
			request := BenchmarkRequest{URL: "https://www.example.com/probe", Candidates: []string{"104.16.0.1"}, SampleCount: 1, Rounds: 1, Concurrency: 1, TimeoutSeconds: 2, MinimumSuccess: 100}
			want := request
			want.Candidates = append([]string(nil), request.Candidates...)
			job, err := s.Enqueue(EnqueueRequest{ProfileID: "primary", Operation: "benchmark", ExpectedVersion: settings.Version, Benchmark: &request}, "tester", "manual")
			if err != nil {
				t.Fatal(err)
			}
			var exposed *BenchmarkRequest
			switch source {
			case "request":
				exposed = &request
			case "enqueue":
				exposed = job.BenchmarkRequest
			case "status":
				exposed = snapshotJob(t, s, job.ID).BenchmarkRequest
			}
			exposed.URL = "https://www.example.com/changed"
			exposed.Rounds = 5
			exposed.Candidates[0] = "104.16.0.2"
			if got := snapshotJob(t, s, job.ID).BenchmarkRequest; !reflect.DeepEqual(*got, want) {
				t.Errorf("%s mutation changed accepted benchmark: %+v", source, got)
			}
			var executed BenchmarkRequest
			s.benchmark = func(_ context.Context, _ Profile, got BenchmarkRequest) (Benchmark, error) {
				executed = got
				return Benchmark{URL: got.URL}, nil
			}
			s.dispatch()
			s.wg.Wait()
			if !reflect.DeepEqual(executed, want) {
				t.Errorf("worker received modified benchmark: %+v", executed)
			}
			reopened := New(m, s.publicURL)
			defer reopened.Close()
			saved := snapshotJob(t, reopened, job.ID)
			if saved.Status != "succeeded" || !reflect.DeepEqual(*saved.BenchmarkRequest, want) {
				t.Error("mutated benchmark was written on worker completion")
			}
		})
	}
}

func TestStatusResultsCannotAlterDurableReceipts(t *testing.T) {
	s, m, settings := testService(t)
	job := enqueue(t, s, settings, "test")
	if err := s.with(true, 0, true, func(st *diskState) error {
		j := &st.Jobs[0]
		j.Status = "succeeded"
		j.Receipt = &Receipt{ID: "original", Message: "accepted", Details: map[string]string{"task": "first"}}
		j.Benchmark = &Benchmark{URL: "https://www.example.com/probe", Results: []NodeResult{{IP: "104.16.0.1", Eligible: true}}}
		j.Diagnostic = &Diagnostic{URL: "https://www.example.com/probe", Headers: map[string]string{"Age": "10"}, Findings: []string{"original"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	baseline, err := json.Marshal(snapshotJob(t, s, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	exposed := snapshotJob(t, s, job.ID)
	exposed.Receipt.ID = "rewritten"
	exposed.Receipt.Details["task"] = "other"
	exposed.Benchmark.URL = "https://changed.invalid/probe"
	exposed.Benchmark.Results[0].IP = "104.16.0.2"
	exposed.Diagnostic.URL = "https://changed.invalid/probe"
	exposed.Diagnostic.Headers["Age"] = "999"
	exposed.Diagnostic.Findings[0] = "rewritten"
	current, _ := json.Marshal(snapshotJob(t, s, job.ID))
	if !bytes.Equal(baseline, current) {
		t.Error("editing status changed internal receipts or diagnostics")
	}
	status, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(status.Settings.Settings, nil); err != nil {
		t.Fatal(err)
	}
	reopened := New(m, s.publicURL)
	defer reopened.Close()
	after, _ := json.Marshal(snapshotJob(t, reopened, job.ID))
	if !bytes.Equal(baseline, after) {
		t.Error("an unrelated save persisted edits to a status response")
	}
}

func TestStatusClonePreservesNilAndEmptyCollections(t *testing.T) {
	s, _, settings := testService(t)
	job := enqueue(t, s, settings, "test")
	for _, empty := range []bool{false, true} {
		if err := s.with(true, 0, true, func(st *diskState) error {
			j := &st.Jobs[0]
			j.Targets = nil
			j.Receipt = &Receipt{}
			j.Benchmark = &Benchmark{Started: time.Now().UTC()}
			j.Diagnostic = &Diagnostic{}
			if empty {
				j.Targets = []string{}
				j.Receipt.Details = map[string]string{}
				j.Benchmark.Results = []NodeResult{}
				j.Diagnostic.Headers = map[string]string{}
				j.Diagnostic.Findings = []string{}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		got := snapshotJob(t, s, job.ID)
		if (got.Targets != nil) != empty || (got.Receipt.Details != nil) != empty || (got.Benchmark.Results != nil) != empty || (got.Diagnostic.Headers != nil) != empty || (got.Diagnostic.Findings != nil) != empty {
			t.Fatalf("nil/empty collection representation changed: empty=%v", empty)
		}
	}
}
