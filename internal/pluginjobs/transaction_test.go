package pluginjobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Simulates a transaction whose callback succeeds but durable commit fails.
type failedCommitBackend struct {
	state   State
	failure error
}

func (b failedCommitBackend) Read(context.Context) (State, error) { return cloneState(b.state), nil }
func (b failedCommitBackend) Update(_ context.Context, fn func(*State) error) error {
	s := cloneState(b.state)
	if err := fn(&s); err != nil {
		return err
	}
	return b.failure
}
func TestQueueNeverReturnsUncommittedWork(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	failure := errors.New("commit failed")
	q := &Queue{Backend: failedCommitBackend{state: InitialState(), failure: failure}}
	j, err := q.Enqueue(ctx, testInput(), now)
	if !errors.Is(err, failure) || j.ID != "" {
		t.Fatalf("uncommitted enqueue returned job: %q %v", j.ID, err)
	}
	real := testQueue(t, "")
	if _, err = real.Enqueue(ctx, testInput(), now); err != nil {
		t.Fatal(err)
	}
	s, err := real.Backend.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	q.Backend = failedCommitBackend{state: s, failure: failure}
	j, ok, err := q.Claim(ctx, now)
	if !errors.Is(err, failure) || ok || j.ID != "" {
		t.Fatalf("uncommitted claim returned work: %q %v %v", j.ID, ok, err)
	}
}
