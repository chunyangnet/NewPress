package pluginjobs

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFrozenFanoutSurvivesRestartAndRecipientChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	q := testQueue(t, path)
	now := time.Now().UTC()
	event := Event{Key: "update:1:1", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	subs := []Subscription{{Plugin: "first", Epoch: strings.Repeat("a", 64), Since: now, ActorID: 1}, {Plugin: "second", Epoch: strings.Repeat("b", 64), Since: now, ActorID: 2}}
	if err := DeliverFrozen(t.Context(), q, failingRecipientStore{Store: q, plugin: "second"}, event, subs, now); err == nil {
		t.Fatal("partial enqueue acknowledged")
	}
	subs[0].ActorID = 99
	q = testQueue(t, path)
	if err := DeliverFrozen(t.Context(), q, q, event, nil, now); err != nil {
		t.Fatal(err)
	}
	state, err := q.Backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 2 {
		t.Fatal("lost frozen recipients", err)
	}
	for _, job := range state.Jobs {
		if job.ActorID == 99 {
			t.Fatal("aliased receipt")
		}
	}
	state.Fanouts[event.Key].Subscriptions[0].ActorID = 99
	frozen, err := q.FreezeFanout(t.Context(), event, nil)
	if err != nil || frozen.Subscriptions[0].ActorID != 1 {
		t.Fatal("read alias", err)
	}
	foundReceipt, found, err := q.FindFanout(t.Context(), event)
	if err != nil || !found || foundReceipt.Subscriptions[0].ActorID != 1 {
		t.Fatal("find receipt after restart", found, err)
	}
	foundReceipt.Subscriptions[0].ActorID = 88
	foundReceipt, found, err = q.FindFanout(t.Context(), event)
	if err != nil || !found || foundReceipt.Subscriptions[0].ActorID != 1 {
		t.Fatal("find receipt alias", err)
	}
	event.Version++
	if _, _, err = q.FindFanout(t.Context(), event); !errors.Is(err, ErrConflict) {
		t.Fatal("lookup accepted changed event", err)
	}
	if _, err = q.FreezeFanout(t.Context(), event, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("changed event accepted", err)
	}
}

func TestRestoredPartialFanoutDoesNotResume(t *testing.T) {
	q := testQueue(t, "")
	now := time.Now().UTC()
	event := Event{Key: "restore", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	subs := []Subscription{{Plugin: "first", Epoch: strings.Repeat("a", 64), Since: now, ActorID: 1}, {Plugin: "second", Epoch: strings.Repeat("b", 64), Since: now, ActorID: 2}}
	if err := DeliverFrozen(t.Context(), q, failingRecipientStore{Store: q, plugin: "second"}, event, subs, now); err == nil {
		t.Fatal("expected partial failure")
	}
	if err := q.CancelRestored(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if err := DeliverFrozen(t.Context(), q, q, event, subs, now); err != nil {
		t.Fatal(err)
	}
	state, err := q.Backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 1 {
		t.Fatal("restored event resumed", err)
	}
	for _, job := range state.Jobs {
		if job.Status != "cancelled" {
			t.Fatal("restored task active")
		}
	}
}

func TestFrozenEmptyFanoutDoesNotSubscribeLaterPlugins(t *testing.T) {
	q := testQueue(t, "")
	now := time.Now().UTC()
	event := Event{Key: "empty", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	if err := DeliverFrozen(t.Context(), q, q, event, nil, now); err != nil {
		t.Fatal(err)
	}
	subs := []Subscription{{Plugin: "later", Epoch: strings.Repeat("a", 64), Since: now, ActorID: 1}}
	if err := DeliverFrozen(t.Context(), q, q, event, subs, now); err != nil {
		t.Fatal(err)
	}
	state, err := q.Backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 0 {
		t.Fatal("old event gained recipients", err)
	}
}
