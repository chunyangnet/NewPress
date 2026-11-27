package pluginjobs

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type unavailableEnqueue struct{ Store }

func (s unavailableEnqueue) Enqueue(context.Context, Input, time.Time) (Job, error) {
	return Job{}, ErrCapacity
}

type failedCompletion struct{ *Queue }

func (s failedCompletion) CompleteFanout(context.Context, Event) error { return ErrCapacity }

func TestCompletedFanoutReplaySkipsEnqueueAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	q := testQueue(t, path)
	now := time.Now().UTC()
	event := Event{Key: "complete:1", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	subs := []Subscription{{Plugin: "reader", Epoch: strings.Repeat("a", 64), Since: now, ActorID: 1}}
	if err := DeliverFrozen(t.Context(), failedCompletion{q}, q, event, subs, now); !errors.Is(err, ErrCapacity) {
		t.Fatal("completion failure acknowledged", err)
	}
	receipt, _, err := q.FindFanout(t.Context(), event)
	if err != nil || receipt.Delivered {
		t.Fatal("failed completion became durable", err)
	}
	if err := DeliverFrozen(t.Context(), q, q, event, subs, now); err != nil {
		t.Fatal(err)
	}
	q = testQueue(t, path)
	receipt, _, err = q.FindFanout(t.Context(), event)
	if err != nil || !receipt.Delivered {
		t.Fatal("completion lost", err)
	}
	if err := DeliverFrozen(t.Context(), q, unavailableEnqueue{q}, event, nil, now); err != nil {
		t.Fatal("completed replay called enqueue", err)
	}
	state, err := q.Backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 1 {
		t.Fatal("duplicate task", err)
	}
	changed := event
	changed.Version++
	if err := q.CompleteFanout(t.Context(), changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
