package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestTaskExecutorRealWasmAndAuthorization(t *testing.T) {
	_, manifest, code := businessFixture(t)
	manifest.Hooks["task.execute.v1"] = []string{"np_handle"}
	m := NewManager(t.TempDir())
	if _, err := m.InstallPackage(createPluginArchive(t, manifest, code)); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(filepath.Join(m.root, manifest.Slug, ".official.json"), Release{Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfiguration(manifest)
	cfg.Grants = manifest.Capabilities
	if err := m.Configure(manifest.Slug, cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	epoch, err := m.CurrentTaskEpoch(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	j := pluginjobs.Job{ID: "host-job", Plugin: manifest.Slug, Epoch: epoch.Token, Kind: "task.execute.v1", Status: "running", LeaseUntil: time.Now().Add(time.Minute), Payload: json.RawMessage(`{}`)}
	authorized := true
	executor := TaskExecutor{Manager: m, Authorize: func(context.Context, pluginjobs.Job) error {
		if !authorized {
			return pluginjobs.ErrAuthorization
		}
		return nil
	}}
	if err = executor.Execute(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Dispatch(t.Context(), manifest.Slug, BusinessInput{Event: j.Kind}, nil); !errors.Is(err, ErrInactive) {
		t.Fatal("generic dispatch bypassed durable task authorization", err)
	}
	authorized = false
	if err = executor.Execute(t.Context(), j); !errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("revoked principal executed", err)
	}
	lookupFailure := errors.New("account storage temporarily unavailable")
	executor.Authorize = func(context.Context, pluginjobs.Job) error { return lookupFailure }
	if err = executor.Execute(t.Context(), j); !errors.Is(err, lookupFailure) || errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("transient lookup failure must remain retryable", err)
	}
	executor.Authorize = func(context.Context, pluginjobs.Job) error { return nil }
	authorized = true
	if err = m.SetEnabled(manifest.Slug, false); err != nil {
		t.Fatal(err)
	}
	if err = m.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	if err = executor.Execute(t.Context(), j); !errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("old epoch executed", err)
	}
}
