package pluginjobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

type workerClaimBoundary struct {
	Store
	after   func()
	claimAt time.Time
}

func (q workerClaimBoundary) Claim(ctx context.Context, now time.Time) (Job, bool, error) {
	if !q.claimAt.IsZero() {
		now = q.claimAt
	}
	job, found, err := q.Store.Claim(ctx, now)
	if q.after != nil {
		q.after()
	}
	return job, found, err
}

func TestWorkerDoesNotExecuteCancelledOrExpiredClaim(t *testing.T) {
	for _, stage := range []string{"cancel-after-claim", "expired-lease", "completion-reserve", "allowed"} {
		t.Run(stage, func(t *testing.T) {
			q := testQueue(t, "")
			now := time.Now().UTC()
			job, err := q.Enqueue(t.Context(), testInput(), now.Add(-time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store := workerClaimBoundary{Store: q}
			if stage == "cancel-after-claim" {
				store.after = cancel
			} else if stage == "expired-lease" {
				store.claimAt = now.Add(-LeaseDuration - time.Second)
			} else if stage == "completion-reserve" {
				store.claimAt = now.Add(-LeaseDuration + 500*time.Millisecond)
			}
			calls := 0
			w := Worker{Queue: store, Executor: executorFunc(func(context.Context, Job) error { calls++; return nil })}
			worked, err := w.Tick(ctx)
			if !worked {
				t.Fatal("fixture did not commit its claim")
			}
			state, readErr := q.Backend.Read(t.Context())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if stage == "allowed" {
				if calls != 1 || err != nil || state.Jobs[job.ID].Status != "succeeded" {
					t.Fatal("valid work did not complete", calls, err, state.Jobs[job.ID].Status)
				}
				return
			}
			if calls != 0 {
				t.Errorf("cancelled/expired work still reached executor: calls=%d", calls)
			}
			if stage == "cancel-after-claim" && !errors.Is(err, context.Canceled) || stage == "expired-lease" && !errors.Is(err, ErrConflict) || stage == "completion-reserve" && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			want := "running"
			if stage == "completion-reserve" {
				want = "retry"
			}
			if state.Jobs[job.ID].Status != want {
				t.Fatalf("claim lost recovery state: got=%s want=%s", state.Jobs[job.ID].Status, want)
			}
		})
	}
}
