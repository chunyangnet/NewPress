package httpserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/pluginjobs"
)

type sharedEventControls struct {
	control plugin.TaskControl
	failure error
}

func (s *sharedEventControls) LoadTaskControl(context.Context, string) (plugin.TaskControl, bool, error) {
	return s.control, s.failure == nil, s.failure
}

func (s *sharedEventControls) ListTaskControls(context.Context) ([]plugin.TaskControl, error) {
	if s.failure != nil {
		return nil, s.failure
	}
	return []plugin.TaskControl{s.control}, nil
}

func (s *sharedEventControls) SaveTaskControl(context.Context, int64, plugin.TaskControl) error {
	return errors.New("read-only shared event fixture")
}

func TestPluginTaskSharedEventDiscoveryRetainsFailuresAndFindsRemoteSubscription(t *testing.T) {
	s, _, _, principal := newReviewedTaskHTTPTestServer(t)
	epoch, err := plugin.NewTaskEpoch(principal.UserID)
	if err != nil {
		t.Fatal(err)
	}
	store := &sharedEventControls{control: plugin.TaskControl{Schema: 1, Slug: "remote-reader", Revision: 1,
		Epoch: epoch, Enabled: true, ModuleDigest: strings.Repeat("a", 64),
		Manifest: plugin.Manifest{Schema: 1, Slug: "remote-reader", Name: "Remote reader", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm",
			Capabilities: []string{"settings.read"}, Hooks: map[string][]string{"event.deliver.v1": {"np_handle"}}},
		Configuration: plugin.Configuration{Version: 1, Values: map[string]any{}, Grants: []string{"settings.read"}}}}
	if err := plugin.ValidateTaskControl(store.control); err != nil {
		t.Fatal(err)
	}
	// This event-observing instance deliberately has no local plugin package.
	s.plugins = plugin.NewManager(t.TempDir(), plugin.WithTaskControlStore(store))
	backend, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	queue := &pluginjobs.Queue{Backend: backend}
	s.SetPluginTaskStore(queue)
	event := activity.New(activity.Publication, 1, 1, principal.UserID, 1, "publish", time.Now().UTC())
	delivery := pluginjobs.Event{Key: event.Key, Kind: "content.published", ObjectID: event.ObjectID, Version: event.Version, At: event.At}
	for _, failure := range []error{plugin.ErrTaskControlUnavailable, plugin.ErrTaskControlCorrupt} {
		store.failure = failure
		if err := s.ObservePluginActivity(t.Context(), event); !errors.Is(err, failure) {
			t.Fatal("shared discovery failure was acknowledged", err)
		}
		if _, found, err := queue.FindFanout(t.Context(), delivery); err != nil || found {
			t.Fatal("failed discovery froze an empty receipt", found, err)
		}
	}
	store.failure = nil
	if err := s.ObservePluginActivity(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	state, err := backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 1 {
		t.Fatal("remote subscription was omitted or duplicated", err)
	}
	for _, job := range state.Jobs {
		if job.Plugin != store.control.Slug || job.Epoch != epoch.Token || job.ActorID != principal.UserID {
			t.Fatal("shared event was bound to another subscription", job.ID)
		}
	}
	store.failure = plugin.ErrTaskControlUnavailable
	if err := s.ObservePluginActivity(t.Context(), event); err != nil {
		t.Fatal("existing frozen receipt rediscovered unavailable authority", err)
	}
	after, err := backend.Read(t.Context())
	if err != nil || len(after.Jobs) != 1 {
		t.Fatal("receipt replay duplicated delivery", err)
	}
}
