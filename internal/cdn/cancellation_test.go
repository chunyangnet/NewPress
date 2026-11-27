package cdn

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
)

// Stop at the same durable boundary as dispatch, before it registers the
// cancellation function and launches process. This makes the interleaving exact.
func claimBeforeWorkerStart(t *testing.T, s *Service, id string) (Job, uint64, context.Context) {
	t.Helper()
	var claimed Job
	var generation uint64
	var config context.Context
	if err := s.with(true, 0, true, func(st *diskState) error {
		for i, job := range st.Jobs {
			if job.ID != id {
				continue
			}
			job.Status = "running"
			job.Attempts++
			job.RunToken = randomID()
			job.Updated = time.Now().UTC()
			st.Jobs[i] = job
			claimed = cloneJob(job)
			generation, config = s.generation, s.configCtx
			return nil
		}
		t.Fatal("fixture job not found")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return claimed, generation, config
}

func TestCancelledClaimCannotStartCloudWork(t *testing.T) {
	for _, operation := range []string{"purge_url", "benchmark", "diagnose"} {
		for _, next := range []string{"cancel", "retry", "new_attempt"} {
			t.Run(operation+"/"+next, func(t *testing.T) {
				s, _, settings := testService(t)
				request := EnqueueRequest{ProfileID: "primary", Operation: operation, ExpectedVersion: settings.Version}
				if operation == "benchmark" {
					request.Benchmark = &BenchmarkRequest{URL: "https://www.example.com/probe", Candidates: []string{"104.16.0.1"}, SampleCount: 1, Rounds: 1, Concurrency: 1, TimeoutSeconds: 2, MinimumSuccess: 100}
				} else {
					request.Targets = []string{"https://www.example.com/approved"}
				}
				job, err := s.Enqueue(request, "tester", "manual")
				if err != nil {
					t.Fatal(err)
				}
				claimed, generation, config := claimBeforeWorkerStart(t, s, job.ID)
				if err := s.JobAction(job.ID, "cancel", settings.Version); err != nil {
					t.Fatal(err)
				}
				wantStatus := "cancelled"
				if next != "cancel" {
					if err := s.JobAction(job.ID, "retry", settings.Version); err != nil {
						t.Fatal(err)
					}
					wantStatus = "queued"
				}
				if next == "new_attempt" {
					claimBeforeWorkerStart(t, s, job.ID)
					wantStatus = "running"
				}
				before := snapshotBytes(t, s)
				calls := 0
				s.execute = func(context.Context, Profile, Job) (Receipt, error) {
					calls++
					return accepted("old-attempt"), nil
				}
				s.benchmark = func(context.Context, Profile, BenchmarkRequest) (Benchmark, error) {
					calls++
					return Benchmark{}, nil
				}
				s.api.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return response(http.StatusOK, "diagnostic"), nil
				})
				// The cancelling action ran before registration, so this context
				// is still live. A fresh state check must reject the old claim.
				ctx, cancel := context.WithTimeout(config, time.Second)
				defer cancel()
				if ctx.Err() != nil {
					t.Fatal("fixture accidentally cancelled the old worker context")
				}
				s.process(ctx, generation, settings.MaxAttempts, settings.Profiles[0], claimed)
				if calls != 0 {
					t.Errorf("stale %s claim invoked cloud work %d times after %s", operation, calls, next)
				}
				if !bytes.Equal(before, snapshotBytes(t, s)) || snapshotJob(t, s, job.ID).Status != wantStatus {
					t.Error("stale worker changed the newer durable task state")
				}
			})
		}
	}
}

func TestWorkerStartHonorsContextAndInstallation(t *testing.T) {
	for _, action := range []string{"cancel_context", "save_settings", "disable", "reenable", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			s, manager, settings := testService(t)
			job := enqueue(t, s, settings, "purge_url", "https://www.example.com/approved")
			claimed, generation, config := claimBeforeWorkerStart(t, s, job.ID)
			ctx, cancel := context.WithCancel(config)
			defer cancel()
			switch action {
			case "cancel_context":
				cancel()
			case "save_settings":
				settings.Paused = true
				if _, err := s.Save(settings, nil); err != nil {
					t.Fatal(err)
				}
			case "disable", "reenable":
				if err := manager.SetEnabled(plugin.CDN, false); err != nil {
					t.Fatal(err)
				}
				if action == "reenable" {
					if err := manager.SetEnabled(plugin.CDN, true); err != nil {
						t.Fatal(err)
					}
				}
			case "uninstall":
				if err := manager.Uninstall(plugin.CDN); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			s.execute = func(context.Context, Profile, Job) (Receipt, error) {
				calls++
				return accepted("stale-lifecycle"), nil
			}
			s.process(ctx, generation, settings.MaxAttempts, settings.Profiles[0], claimed)
			if calls != 0 {
				t.Errorf("worker started cloud work after %s", action)
			}
			if action != "uninstall" {
				got := snapshotJob(t, s, job.ID)
				if got.Status != "interrupted" || got.Receipt != nil {
					t.Errorf("unexpected post-cancellation task: status=%s receipt=%v", got.Status, got.Receipt)
				}
			}
		})
	}
}

func TestCancelledRunningJobRejectsLateReceiptAndAllowsRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "cancel"
		if retry {
			name = "retry"
		}
		t.Run(name, func(t *testing.T) {
			s, _, settings := testService(t)
			job := enqueue(t, s, settings, "purge_url", "https://www.example.com/approved")
			started := make(chan context.Context, 1)
			release := make(chan struct{})
			var once sync.Once
			releaseWorker := func() { once.Do(func() { close(release) }) }
			t.Cleanup(func() {
				s.Close()
				releaseWorker()
				s.wg.Wait()
			})
			s.execute = func(ctx context.Context, _ Profile, _ Job) (Receipt, error) {
				started <- ctx
				<-release
				return accepted("late-receipt"), nil
			}
			s.dispatch()
			var running context.Context
			select {
			case running = <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("worker never started")
			}
			if err := s.JobAction(job.ID, "cancel", settings.Version); err != nil {
				t.Fatal(err)
			}
			select {
			case <-running.Done():
			case <-time.After(time.Second):
				t.Fatal("cancel did not reach the active request")
			}
			wantStatus := "cancelled"
			if retry {
				if err := s.JobAction(job.ID, "retry", settings.Version); err != nil {
					t.Fatal(err)
				}
				wantStatus = "queued"
			}
			before := snapshotBytes(t, s)
			releaseWorker()
			s.wg.Wait()
			if !bytes.Equal(before, snapshotBytes(t, s)) {
				t.Fatal("late receipt overwrote the cancellation or retry")
			}
			got := snapshotJob(t, s, job.ID)
			if got.Status != wantStatus || got.Receipt != nil {
				t.Fatalf("unexpected late completion: status=%s receipt=%v", got.Status, got.Receipt)
			}
			if retry {
				// Advance only the fixture's dispatch clock to avoid sleeping.
				s.mu.Lock()
				s.lastDispatch["primary"] = time.Time{}
				s.mu.Unlock()
				calls := 0
				s.execute = func(context.Context, Profile, Job) (Receipt, error) {
					calls++
					return accepted("fresh-receipt"), nil
				}
				s.dispatch()
				s.wg.Wait()
				got = snapshotJob(t, s, job.ID)
				if calls != 1 || got.Status != "accepted" || got.Receipt == nil || got.Receipt.ID != "fresh-receipt" || got.Attempts != 1 {
					t.Fatal("explicit retry did not complete exactly once")
				}
			}
		})
	}
}

func TestFailedCancellationWriteKeepsRunningRequest(t *testing.T) {
	s, manager, settings := testService(t)
	job := enqueue(t, s, settings, "purge_url", "https://www.example.com/approved")
	_, _, config := claimBeforeWorkerStart(t, s, job.ID)
	ctx, cancel := context.WithCancel(config)
	defer cancel()
	s.mu.Lock()
	s.running[job.ID] = cancel
	s.mu.Unlock()
	var path string
	if err := manager.WithData(plugin.CDN, true, 0, func(dir string, _ uint64) error {
		path = filepath.Join(dir, "state.json")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := snapshotBytes(t, s)
	backup := path + ".original"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
		if err := os.Rename(backup, path); err != nil {
			t.Error(err)
		}
	})
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.JobAction(job.ID, "cancel", settings.Version); err == nil {
		t.Fatal("cancellation unexpectedly persisted through a directory")
	}
	if ctx.Err() != nil || snapshotJob(t, s, job.ID).Status != "running" {
		t.Fatal("failed command cancelled the request or changed memory")
	}
	retained, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(before, retained) {
		t.Fatal("failed command changed the original durable state", err)
	}
}
