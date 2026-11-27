package httpserver

import (
	"errors"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/pluginjobs"
	"testing"
	"time"
)

func TestPluginEventReplayDoesNotRediscoverAccounts(t *testing.T) {
	s, _, _, _, _ := newCommentHTTPTestServer(t)
	installTaskFixture(t, s)
	backend, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	s.SetPluginTaskStore(q)
	_, cfg, _, err := s.plugins.Configuration("task-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.plugins.ConfigureAs("task-fixture", cfg, 1); err != nil {
		t.Fatal(err)
	}
	epoch, err := s.plugins.CurrentTaskEpoch("task-fixture")
	if err != nil {
		t.Fatal(err)
	}
	event := activity.New(activity.Publication, 1, 1, 1, 1, "publish", time.Now().UTC())
	delivery := pluginjobs.Event{Key: event.Key, Kind: "content.published", ObjectID: 1, Version: 1, At: event.At}
	if _, err = q.FreezeFanout(t.Context(), delivery, []pluginjobs.Subscription{{Plugin: "task-fixture", Epoch: epoch.Token, Since: epoch.StartedAt, ActorID: 1}}); err != nil {
		t.Fatal(err)
	}
	// This server intentionally has only a legacy account (no durable user store).
	// Existing receipts must still enqueue; execution reauthorizes separately.
	if err = s.ObservePluginActivity(t.Context(), event); err != nil {
		t.Fatal("replay consulted current accounts", err)
	}
	state, err := backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 1 {
		t.Fatal("frozen recipient missing", err)
	}
	event.At = event.At.Add(time.Second)
	event.Due = event.At
	if err = s.ObservePluginActivity(t.Context(), event); !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
}
