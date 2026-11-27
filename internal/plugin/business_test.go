package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func businessFixture(t *testing.T) (*Manager, Manifest, []byte) {
	t.Helper()
	source := filepath.Join("..", "..", "examples", "plugins", "content-age")
	manifest, e := readManifest(filepath.Join(source, "plugin.json"))
	if e != nil {
		t.Fatal(e)
	}
	// OTA scenarios use a fixed synthetic baseline independently of releases.
	manifest.Version = "1.0.0"
	code, e := os.ReadFile(filepath.Join(source, "plugin.wasm"))
	if e != nil {
		t.Fatal(e)
	}
	m := NewManager(t.TempDir())
	if _, e = m.InstallPackage(createPluginArchive(t, manifest, code)); e != nil {
		t.Fatal(e)
	}
	if e = WritePrivate(filepath.Join(m.root, manifest.Slug, ".official.json"), Release{Manifest: manifest}); e != nil {
		t.Fatal(e)
	}
	_, cfg, _, e := m.Configuration(manifest.Slug)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Grants = manifest.Capabilities
	if e = m.Configure(manifest.Slug, cfg); e != nil {
		t.Fatal(e)
	}
	if e = m.SetEnabled(manifest.Slug, true); e != nil {
		t.Fatal(e)
	}
	return m, manifest, code
}
func TestRealWasmBusinessSettingsAgeAndLifecycle(t *testing.T) {
	m, manifest, _ := businessFixture(t)
	slug := manifest.Slug
	input := BusinessInput{Event: "content.render", Post: map[string]any{"updated_at": time.Now().Add(-200 * 24 * time.Hour).Unix()}}
	out, e := m.Dispatch(t.Context(), slug, input, nil)
	if e != nil || out.Fragment == nil || out.Fragment.Title != "留意内容时效" {
		t.Fatal(out, e)
	}
	input.Post = map[string]any{"updated_at": time.Now().Unix()}
	out, e = m.Dispatch(t.Context(), slug, input, nil)
	if e != nil || out.Fragment != nil {
		t.Fatal(out, e)
	}
	_, cfg, _, _ := m.Configuration(slug)
	cfg.Values["title"] = `<script>alert(1)</script>`
	cfg.Values["message"] = "包含转义 \"days\": 0 与中文"
	if e = m.Configure(slug, cfg); e != nil {
		t.Fatal(e)
	}
	if e = m.Configure(slug, cfg); e != ErrConfigurationConflict {
		t.Fatal("stale config accepted", e)
	}
	out, e = m.Dispatch(t.Context(), slug, BusinessInput{Event: "admin.action", Payload: json.RawMessage(`{"action":"preview"}`)}, nil)
	if e != nil || out.Fragment == nil || out.Fragment.Title != cfg.Values["title"] || out.Fragment.Text != cfg.Values["message"] {
		t.Fatal(out, e)
	}
	_, cfg, _, _ = m.Configuration(slug)
	cfg.Grants = []string{"render.fragment"}
	if e = m.Configure(slug, cfg); e != nil {
		t.Fatal(e)
	}
	out, e = m.Dispatch(t.Context(), slug, BusinessInput{Event: "admin.action", Payload: json.RawMessage(`{"action":"preview"}`)}, nil)
	if e != nil || out.Fragment != nil {
		t.Fatal("ungranted settings exposed", out, e)
	}
	if e = m.SetEnabled(slug, false); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Dispatch(t.Context(), slug, input, nil); e == nil {
		t.Fatal("disabled plugin executed")
	}
	if e = m.Uninstall(slug); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(m.root, slug)); !os.IsNotExist(e) {
		t.Fatal("namespace remains", e)
	}
}
func TestBusinessIntegrityAndUninstallConcurrent(t *testing.T) {
	m, manifest, code := businessFixture(t)
	slug := manifest.Slug
	code = append(code, 0)
	if e := os.WriteFile(filepath.Join(m.root, slug, "plugin.wasm"), code, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Dispatch(t.Context(), slug, BusinessInput{Event: "admin.action"}, nil); e == nil || !strings.Contains(e.Error(), "文件已变化") {
		t.Fatal(e)
	}
	m, manifest, _ = businessFixture(t)
	slug = manifest.Slug
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			m.Dispatch(t.Context(), slug, BusinessInput{Event: "admin.action", Payload: json.RawMessage(`{"action":"preview"}`)}, nil)
		})
	}
	if e := m.Uninstall(slug); e != nil {
		t.Fatal(e)
	}
	wg.Wait()
	if _, e := os.Stat(filepath.Join(m.root, slug)); !os.IsNotExist(e) {
		t.Fatal("resurrected namespace")
	}
}
func TestBusinessRejectsUnsafeABIAndDeadline(t *testing.T) {
	_, manifest, code := businessFixture(t)
	// The first exported ABI function has no linear memory/allocator.
	if _, e := executeBusiness(t.Context(), abiV1Module(), manifest, "np_handle", []byte(`{}`), nil); e == nil {
		t.Fatal("legacy ABI accepted as business plugin")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := executeBusiness(ctx, code, manifest, "np_handle", []byte(`{}`), nil); e == nil {
		t.Fatal("cancelled execution succeeded")
	}
	manifest.Hooks = map[string][]string{"content.render": {"np_api_version"}}
	if _, e := executeBusiness(t.Context(), code, manifest, "", nil, nil); e == nil {
		t.Fatal("wrong signature accepted")
	}
	manifest.Hooks = map[string][]string{"content.render": {"np_handle"}}
	manifest.Capabilities = append(manifest.Capabilities, "content.write")
	if e := ValidateManifest(manifest); e == nil {
		t.Fatal("unsupported capability advertised")
	}
}
func TestBusinessBoundsAndHostValidation(t *testing.T) {
	_, manifest, code := businessFixture(t)
	// The fixed-buffer guest rejects an oversized request without touching memory.
	if _, e := executeBusiness(t.Context(), code, manifest, "np_handle", bytes.Repeat([]byte("x"), businessBytes+1), nil); e == nil {
		t.Fatal("oversized input succeeded")
	}
}
