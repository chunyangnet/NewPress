package database

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
)

func prepareWebhookWASMAdmission(t *testing.T) (*webhookFixture, plugin.TaskExecutor, pluginjobs.Job) {
	t.Helper()
	f := newWebhookFixture(t)
	archive, code := contentEffectProcessArchive(t, f.control.Manifest)
	f.control.ModuleDigest = plugineffects.Digest(code)
	f.control.Revision++
	if err := f.controls.SaveTaskControl(f.ctx, f.control.Revision-1, f.control); err != nil {
		t.Fatal(err)
	}
	old := f.req.Authorization.Claim
	if err := f.jobs.Change(f.ctx, old.Plugin, old.ID, old.Version, "cancel", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	m := plugin.NewManager(root, plugin.WithTaskControlStore(f.controls))
	if _, err := m.InstallPackage(archive); err != nil {
		t.Fatal(err)
	}
	if err := plugin.WritePrivate(filepath.Join(root, f.control.Slug, ".official.json"), plugin.Release{Manifest: f.control.Manifest}); err != nil {
		t.Fatal(err)
	}
	_, cfg, _, err := m.Configuration(f.control.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Configure(f.control.Slug, cfg); err != nil {
		t.Fatal(err)
	}
	if err = m.SetEnabled(f.control.Slug, true); err != nil {
		t.Fatal(err)
	}
	epoch, err := m.CurrentTaskEpoch(f.control.Slug)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"method":"http.enqueue","key":"wasm-notify","url":"https://example.com/webhook","value":{"event":"accepted","id":9007199254740993}}`)
	job, err := f.jobs.Enqueue(f.ctx, pluginjobs.Input{Plugin: f.control.Slug, Kind: "task.execute.v1", Epoch: epoch.Token, Key: "wasm-webhook", ActorID: f.user.ID, Payload: payload}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	claim, ok, err := f.jobs.Claim(f.ctx, time.Now().UTC())
	if err != nil || !ok || claim.ID != job.ID {
		t.Fatal(ok, err)
	}
	executor := plugin.TaskExecutor{Manager: m, Webhooks: f.webhooks, Authorize: func(ctx context.Context, j pluginjobs.Job) error {
		return f.jobs.CheckSharedClaim(ctx, j, time.Now().UTC())
	}}
	return f, executor, claim
}

func TestPostgreSQLWebhookWASMHostAdmission(t *testing.T) {
	f, executor, claim := prepareWebhookWASMAdmission(t)
	if err := executor.Execute(f.ctx, claim); err != nil {
		t.Fatal("real WASM host failed", err)
	}
	if err := executor.Execute(f.ctx, claim); err != nil {
		t.Fatal("real WASM stable-slot replay", err)
	}
	f.assertNoClaim(t)
	if err := f.jobs.Finish(f.ctx, claim, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	i := f.claim(t)
	if i.JobID != claim.ID || i.ManifestDigest == "" || i.Slot != "wasm-notify" {
		t.Fatal("host binding lost")
	}
	t.Log("real installed WASM invoked http.enqueue twice against PostgreSQL; 1 intent; no claim before source success; 1 claim after success")
}
