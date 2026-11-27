package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
	"newpress.local/newpress/internal/testwasm"
)

type taskEffectProbe struct {
	calls   int
	request plugineffects.Request
	err     error
}

func (p *taskEffectProbe) WriteContent(_ context.Context, request plugineffects.Request) (plugineffects.Result, error) {
	p.calls++
	p.request = request
	return plugineffects.Result{}, p.err
}

func taskEffectFixture(t *testing.T, shared, grant bool, code []byte) (*Manager, Manifest, pluginjobs.Job) {
	t.Helper()
	manifest := Manifest{Schema: 1, Slug: "effect-host", Name: "Effect host", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm",
		Capabilities: []string{"content.write", "settings.read"}, Hooks: map[string][]string{"task.execute.v1": {"np_handle"}, "admin.action": {"np_handle"}}}
	if code == nil {
		var err error
		code, err = os.ReadFile(filepath.Join("testdata", "host-bridge.wasm"))
		if err != nil {
			t.Fatal(err)
		}
	}
	opts := []ManagerOption{}
	if shared {
		opts = append(opts, WithTaskControlStore(&testTaskControls{}))
	}
	m := NewManager(t.TempDir(), opts...)
	if _, err := m.InstallPackage(createPluginArchive(t, manifest, code)); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(filepath.Join(m.root, manifest.Slug, ".official.json"), Release{Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	_, cfg, _, err := m.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Grants = []string{"settings.read"}
	if grant {
		cfg.Grants = append(cfg.Grants, "content.write")
	}
	if err = m.Configure(manifest.Slug, cfg); err != nil {
		t.Fatal(err)
	}
	if err = m.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	epoch, err := m.CurrentTaskEpoch(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	return m, manifest, pluginjobs.Job{ID: strings.Repeat("a", 64), Plugin: manifest.Slug, Epoch: epoch.Token, Kind: "task.execute.v1", Status: "running", ActorID: 19, LeaseToken: "host-private-token", LeaseUntil: time.Now().Add(time.Minute), Version: 8,
		Payload: json.RawMessage(`{"method":"content.update","key":"summary","id":5,"value":{"version":4,"patch":{"title":"updated"}}}`)}
}

func TestTaskEffectBindsClaimAndVerifiedPackage(t *testing.T) {
	m, manifest, job := taskEffectFixture(t, true, true, nil)
	probe := &taskEffectProbe{}
	executor := TaskExecutor{Manager: m, Effects: probe, Authorize: func(context.Context, pluginjobs.Job) error { return nil }}
	if err := executor.Execute(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(manifest)
	module, err := os.ReadFile(filepath.Join(m.root, manifest.Slug, "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 || probe.request.Authorization.Claim.LeaseToken != job.LeaseToken || probe.request.Authorization.Claim.ActorID != 19 ||
		probe.request.Authorization.ManifestDigest != plugineffects.Digest(raw) || probe.request.Authorization.ModuleDigest != plugineffects.Digest(module) || probe.request.PostID != 5 {
		t.Fatal("guest changed host binding", probe.request)
	}
}

func TestTaskEffectDenialCannotBecomeGuestSuccess(t *testing.T) {
	for _, mode := range []string{"file", "missing_service", "no_grant", "conflict", "unknown_field", "null_field", "invalid_envelope", "ignored_failure", "budget"} {
		t.Run(mode, func(t *testing.T) {
			var code []byte
			if mode == "ignored_failure" {
				code = testwasm.HostSequence([]byte(`{"method":"content.update","key":"write","id":1,"value":{"version":1,"patch":{"title":"test"}}}`), []byte(`{"method":"settings.get"}`))
			}
			if mode == "budget" {
				requests := make([][]byte, 33)
				for i := range requests {
					requests[i] = []byte(`{"method":"settings.get"}`)
				}
				requests[32] = []byte(`{"method":"content.update","key":"write","id":1,"value":{"version":1,"patch":{"title":"test"}}}`)
				code = testwasm.HostSequence(requests...)
			}
			m, manifest, job := taskEffectFixture(t, mode != "file", mode != "no_grant", code)
			probe := &taskEffectProbe{}
			if mode == "conflict" || mode == "ignored_failure" {
				probe.err = content.ErrVersionConflict
			}
			executor := TaskExecutor{Manager: m, Effects: probe, Authorize: func(context.Context, pluginjobs.Job) error { return nil }}
			if mode == "missing_service" {
				executor.Effects = nil
			}
			if mode == "unknown_field" {
				job.Payload = json.RawMessage(`{"method":"content.update","key":"x","id":1,"value":{"version":1,"actor_id":2,"patch":{"title":"x"}}}`)
			}
			if mode == "null_field" {
				job.Payload = json.RawMessage(`{"method":"content.update","key":"x","id":1,"value":{"version":1,"patch":{"title":"x","excerpt":null}}}`)
			}
			if mode == "invalid_envelope" {
				job.Payload = json.RawMessage(`{"method":"content.update","actor_id":2,"key":"x","id":1,"value":{"version":1,"patch":{"title":"x"}}}`)
			}
			for range 3 {
				if err := executor.Execute(t.Context(), job); err == nil {
					t.Fatal("failed effect reported success")
				}
			}
			m.mu.Lock()
			state := m.execution[manifest.Slug]
			m.mu.Unlock()
			if state.Failures != 0 || state.Consecutive != 0 || !state.BlockedUntil.IsZero() {
				t.Fatal("business conflict poisoned plugin health", state)
			}
			if mode != "conflict" && mode != "ignored_failure" && probe.calls != 0 {
				t.Fatal("unavailable write reached service")
			}
			if mode == "conflict" && !errors.Is(executor.Execute(t.Context(), job), content.ErrVersionConflict) {
				t.Fatal("lost business error")
			}
		})
	}
}
