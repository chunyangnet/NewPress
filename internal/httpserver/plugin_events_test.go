package httpserver

import (
	"encoding/json"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/pluginjobs"
)

func TestPluginTaskEventBridgeFreezesRecipients(t *testing.T) {
	s, _, _, _, _ := newCommentHTTPTestServer(t)
	installTaskFixture(t, s)
	users, err := auth.NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("event fixture password 2026")
	if err != nil {
		t.Fatal(err)
	}
	s.auth, err = auth.NewWithContext(t.Context(), auth.Config{Username: "owner", PasswordHash: hash, Users: users, BootstrapEmail: "owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	_, principal, err := s.auth.Authenticate("events", "owner", "event fixture password 2026")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	s.SetPluginTaskStore(q)
	event := activity.New(activity.Publication, 1, 1, principal.UserID, 1, "publish", time.Now().UTC())
	if err = s.ObservePluginActivity(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	_, cfg, _, err := s.plugins.Configuration("task-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.plugins.ConfigureAs("task-fixture", cfg, principal.UserID); err != nil {
		t.Fatal(err)
	}
	epoch, err := s.plugins.CurrentTaskEpoch("task-fixture")
	if err != nil {
		t.Fatal(err)
	}
	manual, err := q.Enqueue(t.Context(), pluginjobs.Input{Plugin: "task-fixture", Epoch: epoch.Token, Kind: "task.execute.v1", Key: "worker-success", ActorID: principal.UserID, Payload: json.RawMessage(`{"method":"settings.get"}`)}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&pluginjobs.Worker{Queue: q, Executor: s.PluginTaskExecutor()}).Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	completed, err := backend.Read(t.Context())
	if err != nil || completed.Jobs[manual.ID].Status != "succeeded" {
		t.Fatal("real worker did not succeed", err)
	}
	// Keep the original empty event receipt while testing later subscriptions.
	if err = s.ObservePluginActivity(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	state, err := backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 1 {
		t.Fatal("old event gained recipient", err)
	}
	event = activity.New(activity.Publication, 2, 2, principal.UserID, 1, "publish", time.Now().UTC())
	for range 2 {
		if err = s.ObservePluginActivity(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	state, err = backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 2 {
		t.Fatal("event missing or duplicated", err)
	}
	for _, j := range state.Jobs {
		if j.ID == manual.ID {
			continue
		}
		if j.ActorID != principal.UserID || j.Kind != "event.deliver.v1" {
			t.Fatal("unbound event", j.ID)
		}
	}
	_, cfg, _, err = s.plugins.Configuration("task-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.plugins.Configure("task-fixture", cfg); err != nil {
		t.Fatal(err)
	}
	event = activity.New(activity.Publication, 3, 3, principal.UserID, 1, "publish", time.Now().UTC())
	if err = s.ObservePluginActivity(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	state, err = backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 2 {
		t.Fatal("revoked subscription delivered", err)
	}
	worker := &pluginjobs.Worker{Queue: q, Executor: s.PluginTaskExecutor()}
	if _, err = worker.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err = backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range state.Jobs {
		if job.ID == manual.ID {
			continue
		}
		if job.Status != "cancelled" || job.ErrorCode != "authorization_changed" {
			t.Fatal("revoked epoch executed", job.Status)
		}
	}
}
