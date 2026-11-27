package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
	"newpress.local/newpress/internal/testwasm"
)

type webhookProbe struct {
	requests []plugindispatch.Request
	failure  error
}

func (p *webhookProbe) Enqueue(_ context.Context, r plugindispatch.Request) (plugindispatch.Accepted, error) {
	p.requests = append(p.requests, r)
	return plugindispatch.Accepted{}, p.failure
}
func (*webhookProbe) Claim(context.Context) (plugindispatch.Intent, bool, error) {
	panic("guest must never dispatch")
}
func (*webhookProbe) Finish(context.Context, plugindispatch.Intent, plugindispatch.Outcome) error {
	panic("guest must never record a remote result")
}

func webhookHostFixture(t *testing.T, shared, grant bool, code []byte) (*Manager, Manifest, pluginjobs.Job) {
	t.Helper()
	manifest := Manifest{Schema: 1, Slug: "webhook-host", Name: "Webhook host", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm", Capabilities: []string{"http.send", "settings.read"}, OutboundHosts: []string{"example.com"}, Hooks: map[string][]string{"task.execute.v1": {"np_handle"}, "admin.action": {"np_handle"}}}
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
		cfg.Grants = append(cfg.Grants, "http.send")
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
	j := pluginjobs.Job{ID: strings.Repeat("a", 64), Plugin: manifest.Slug, Epoch: epoch.Token, Kind: "task.execute.v1", Status: "running", ActorID: 19, LeaseToken: "private-host-token", LeaseUntil: time.Now().Add(time.Minute), Version: 8, Payload: json.RawMessage(`{"method":"http.enqueue","key":"notify","url":"https://example.com/path","value":{"event":"ready"}}`)}
	return m, manifest, j
}

func TestTaskWebhookBindsHostClaimAndPackage(t *testing.T) {
	m, manifest, j := webhookHostFixture(t, true, true, nil)
	p := &webhookProbe{}
	e := TaskExecutor{Manager: m, Webhooks: p, Authorize: func(context.Context, pluginjobs.Job) error { return nil }}
	if err := e.Execute(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(manifest)
	code, err := os.ReadFile(filepath.Join(m.root, manifest.Slug, "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 1 || p.requests[0].Authorization.Claim.LeaseToken != j.LeaseToken || p.requests[0].Authorization.Claim.ActorID != 19 || p.requests[0].Authorization.ManifestDigest != plugineffects.Digest(raw) || p.requests[0].Authorization.ModuleDigest != plugineffects.Digest(code) {
		t.Fatal("guest replaced host authority")
	}
	if _, err = m.Dispatch(t.Context(), manifest.Slug, BusinessInput{Event: "admin.action", Payload: j.Payload}, nil); err == nil {
		t.Fatal("synchronous action admitted a webhook")
	}
}

func TestTaskWebhookDenialCannotBecomeGuestSuccess(t *testing.T) {
	for _, mode := range []string{"file", "missing", "grant", "storage", "ignored", "malformed", "budget"} {
		t.Run(mode, func(t *testing.T) {
			var code []byte
			q := []byte(`{"method":"http.enqueue","key":"notify","url":"https://example.com/path","value":{"event":"ready"}}`)
			if mode == "ignored" {
				code = testwasm.HostSequence(q, []byte(`{"method":"settings.get"}`))
			}
			if mode == "budget" {
				calls := make([][]byte, 33)
				for i := range calls {
					calls[i] = []byte(`{"method":"settings.get"}`)
				}
				code = testwasm.HostSequence(calls...)
			}
			m, _, j := webhookHostFixture(t, mode != "file", mode != "grant", code)
			p := &webhookProbe{}
			if mode == "storage" || mode == "ignored" {
				p.failure = plugindispatch.ErrUnavailable
			}
			if mode == "malformed" {
				j.Payload = json.RawMessage(`{"method":"http.enqueue","actor_id":20}`)
			}
			e := TaskExecutor{Manager: m, Webhooks: p, Authorize: func(context.Context, pluginjobs.Job) error { return nil }}
			if mode == "missing" {
				e.Webhooks = nil
			}
			if err := e.Execute(t.Context(), j); err == nil {
				t.Fatal("failed effect became guest success")
			}
			if mode == "ignored" && !errors.Is(p.failure, plugindispatch.ErrUnavailable) {
				t.Fatal("fixture")
			}
		})
	}
}

func TestWebhookManifestNeedsExplicitHostAndGrantIsSeparate(t *testing.T) {
	_, m, _ := webhookHostFixture(t, true, false, nil)
	m.OutboundHosts = nil
	if ValidateManifest(m) == nil {
		t.Fatal("send without allowlist")
	}
	m.OutboundHosts = []string{"example.com"}
	m.Capabilities = []string{"settings.read"}
	if ValidateManifest(m) == nil {
		t.Fatal("unrelated capability allowed outbound hosts")
	}
}

func TestWebhookUpgradeExpandedHostsRevokesSendGrant(t *testing.T) {
	m, manifest, _ := webhookHostFixture(t, true, true, nil)
	code, err := os.ReadFile(filepath.Join(m.root, manifest.Slug, "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	target := captureForTest(t, m, manifest.Slug)
	manifest.OutboundHosts = append(manifest.OutboundHosts, "new.example.com")
	release, archive := upgradeRelease(t, manifest, code, "1.1.0")
	if err = m.updatePackage(t.Context(), archive, release, target, nil); err != nil {
		t.Fatal(err)
	}
	_, cfg, _, err := m.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(cfg.Grants, "http.send") {
		t.Fatal("expanded send targets inherited old consent")
	}
	if err = m.SetEnabled(manifest.Slug, true); err == nil {
		t.Fatal("expanded hosts enabled without review")
	}
}
