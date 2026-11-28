package pluginjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func taskReadFixture(tb testing.TB, count int, disk bool) (*Queue, Job, time.Time) {
	tb.Helper()
	now := time.Date(2026, 11, 28, 2, 0, 0, 0, time.UTC)
	state := InitialState()
	state.ArchivedJobs = map[string]ArchivedJob{}
	var claim Job
	for i := range count {
		in := Input{Plugin: fmt.Sprintf("fixture-%d", i/MaxPluginJobs), Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: fmt.Sprint("task-", i), ActorID: 1, Payload: json.RawMessage(`"` + strings.Repeat("x", 2048) + `"`)}
		id, fingerprint, err := identity(in)
		if err != nil {
			tb.Fatal(err)
		}
		job := newJob(in, id, fingerprint, now.Add(-time.Duration(i%100)*time.Second))
		job.Status = "succeeded"
		if i == 0 {
			job.Status, job.Attempts, job.Version = "running", 1, 2
			job.LeaseToken, job.LeaseUntil = strings.Repeat("b", 64), now.Add(LeaseDuration)
			claim = job
		}
		state.Jobs[id] = job
		in.Key += "-history"
		id, fingerprint, err = identity(in)
		if err != nil {
			tb.Fatal(err)
		}
		archived := newJob(in, id, fingerprint, job.UpdatedAt)
		archived.Status, archived.ArchivedAt = "succeeded", now
		state.ArchivedJobs[id] = ArchivedJob{Summary: Summarize(archived), Fingerprint: fingerprint}
	}
	if err := ValidateState(state); err != nil {
		tb.Fatal(err)
	}
	if err := checkStorageSize(state, true); err != nil {
		tb.Fatal(err)
	}
	path := ""
	if disk {
		path = filepath.Join(tb.TempDir(), "tasks.json")
		body, err := json.Marshal(state)
		if err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			tb.Fatal(err)
		}
	}
	b, err := NewFileBackend(path)
	if err != nil {
		tb.Fatal(err)
	}
	if !disk {
		b.state = state
	}
	return &Queue{Backend: b}, claim, now
}

func BenchmarkPluginTaskReads(b *testing.B) {
	for _, disk := range []bool{false, true} {
		for _, count := range []int{1000, 10000} {
			q, claim, now := taskReadFixture(b, count, disk)
			prefix := fmt.Sprintf("disk=%v/jobs=%d", disk, count)
			b.Run(prefix+"/authorize", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if err := q.CheckClaim(context.Background(), claim, now); err != nil {
						b.Fatal(err)
					}
				}
			})
			for _, archived := range []bool{false, true} {
				b.Run(prefix+fmt.Sprint("/list-archive=", archived), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						page, err := q.List(context.Background(), Query{Plugin: "fixture-0", Limit: 20, Archived: archived})
						if err != nil || page.Total != MaxPluginJobs || len(page.Items) != 20 {
							b.Fatal("list", page.Total, err)
						}
					}
				})
			}
		}
	}
}

// Isolate claim selection from file validation/serialization to measure the
// selection algorithm. Full Queue/FileBackend behavior is tested separately.
type selectionBenchmarkBackend struct{ state State }

func (s *selectionBenchmarkBackend) Read(context.Context) (State, error) { return s.state, nil }
func (s *selectionBenchmarkBackend) Update(_ context.Context, fn func(*State) error) error {
	return fn(&s.state)
}

func BenchmarkPluginTaskClaimSelection(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			fixture, claim, now := taskReadFixture(b, count, false)
			backend := &selectionBenchmarkBackend{state: fixture.Backend.(*FileBackend).state}
			claim.Status, claim.LeaseToken, claim.LeaseUntil = "pending", "", time.Time{}
			q := &Queue{Backend: backend}
			b.ReportAllocs()
			for b.Loop() {
				backend.state.Jobs[claim.ID] = claim
				got, found, err := q.Claim(context.Background(), now)
				if err != nil || !found || got.ID != claim.ID {
					b.Fatal(found, err)
				}
			}
		})
	}
}
