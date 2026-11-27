package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHostCapabilitiesNamespacesAndReadOnlyRendering(t *testing.T) {
	code, e := os.ReadFile(filepath.Join("testdata", "host-bridge.wasm"))
	if e != nil {
		t.Fatal(e)
	}
	manifest := Manifest{Schema: 1, Slug: "host-check", Name: "Host check", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm", Capabilities: []string{"settings.read", "settings.write", "content.read", "render.fragment", "cache", "http.outbound"}, OutboundHosts: []string{"example.com"}, Hooks: map[string][]string{"admin.action": {"np_handle"}, "content.render": {"np_handle"}}, Settings: []Setting{{Key: "count", Label: "Count", Type: "number", Default: 0, Min: 0, Max: 10}}}
	m := NewManager(t.TempDir())
	manifest, e = m.InstallPackage(createPluginArchive(t, manifest, code))
	if e != nil {
		t.Fatal(e)
	}
	slug := manifest.Slug
	if e = WritePrivate(filepath.Join(m.root, slug, ".official.json"), Release{Manifest: manifest}); e != nil {
		t.Fatal(e)
	}
	_, config, _, _ := m.Configuration(slug)
	config.Grants = manifest.Capabilities
	if e = m.Configure(slug, config); e != nil {
		t.Fatal(e)
	}
	if e = m.SetEnabled(slug, true); e != nil {
		t.Fatal(e)
	}
	call := func(event, payload string, want bool) json.RawMessage {
		t.Helper()
		out, e := m.Dispatch(t.Context(), slug, BusinessInput{Event: event, Payload: json.RawMessage(payload)}, func(_ context.Context, id int64) (any, error) { return map[string]int64{"id": id}, nil })
		if e != nil {
			t.Fatal(e)
		}
		var response struct {
			OK    bool            `json:"ok"`
			Data  json.RawMessage `json:"data"`
			Error string          `json:"error"`
		}
		if e = json.Unmarshal(out.Data, &response); e != nil || response.OK != want {
			t.Fatalf("%s: %s %v", payload, out.Data, e)
		}
		return response.Data
	}
	call("admin.action", `{"method":"cache.set","key":"value","value":{"count":3}}`, true)
	if data := call("admin.action", `{"method":"cache.get","key":"value"}`, true); string(data) != `{"count":3}` {
		t.Fatal(string(data))
	}
	call("content.render", `{"method":"cache.set","key":"value","value":4}`, false)
	call("content.render", `{"method":"settings.set","value":{"count":3}}`, false)
	call("admin.action", `{"method":"cache.set","key":"../../escape","value":4}`, false)
	call("admin.action", `{"method":"settings.set","value":{"count":4}}`, true)
	if data := call("admin.action", `{"method":"settings.get"}`, true); string(data) != `{"count":4}` {
		t.Fatal(string(data))
	}
	call("admin.action", `{"method":"settings.set","value":{"count":100}}`, false)
	if data := call("admin.action", `{"method":"content.get","id":1}`, true); string(data) != `{"id":1}` {
		t.Fatal(string(data))
	}
	for _, url := range []string{"https://127.0.0.1/", "https://other.example.com/", "http://example.com/", "https://example.com:8080/"} {
		raw, _ := json.Marshal(map[string]string{"method": "http.get", "url": url})
		call("admin.action", string(raw), false)
	}
	_, config, _, _ = m.Configuration(slug)
	config.Grants = []string{"render.fragment"}
	if e = m.Configure(slug, config); e != nil {
		t.Fatal(e)
	}
	call("admin.action", `{"method":"cache.get","key":"value"}`, false)
	call("admin.action", `{"method":"content.get","id":1}`, false)
	if e = m.Uninstall(slug); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(m.root, slug, "data", "kv.json")); !os.IsNotExist(e) {
		t.Fatal("KV survives uninstall")
	}
}
