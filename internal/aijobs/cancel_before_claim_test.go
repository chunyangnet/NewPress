package aijobs

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkerCancelledBeforeClaimPreservesQueuedJob(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancelled"
		if deadline {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			q, _ := testQueue(t)
			epoch := snapshot(t, q).Epoch
			p := plan()
			p.Topics = p.Topics[:1]
			now := time.Now().UTC()
			if err := q.Enqueue(epoch, p, now); err != nil {
				t.Fatal(err)
			}
			before := queueBytes(t, q)
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				want = context.DeadlineExceeded
			}
			cancel()
			calls := 0
			ran, err := q.step(ctx, now, func(context.Context, Job) (string, error) { calls++; return "should not run", nil })
			if ran || !errors.Is(err, want) || calls != 0 {
				t.Fatalf("cancelled claim: ran=%v calls=%d err=%v", ran, calls, err)
			}
			if !bytes.Equal(before, queueBytes(t, q)) {
				t.Fatal("cancelled claim changed durable queue")
			}
			job := snapshot(t, q).Jobs[0]
			if job.Status != "queued" || job.Attempts != 0 || job.Version != 1 {
				t.Fatal(job)
			}
			ran, err = q.step(t.Context(), now, func(context.Context, Job) (string, error) { calls++; return "saved result", nil })
			if !ran || err != nil || calls != 1 || snapshot(t, q).Jobs[0].Status != "succeeded" {
				t.Fatal("cancelled claim consumed later work", ran, calls, err)
			}
		})
	}
}
