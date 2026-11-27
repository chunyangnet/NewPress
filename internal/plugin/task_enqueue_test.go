package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnqueueHostRequiresGrantAndBoundAdmin(t *testing.T) {
	code, err := os.ReadFile(filepath.Join("testdata", "host-bridge.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Schema: 1, Slug: "enqueue-test", Name: "Enqueue test", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm", Capabilities: []string{"jobs.enqueue", "render.fragment"}, Hooks: map[string][]string{"admin.action": {"np_handle"}, "task.execute.v1": {"np_handle"}, "content.render": {"np_handle"}}}
	m := NewManager(t.TempDir())
	if _, err = m.InstallPackage(createPluginArchive(t, manifest, code)); err != nil {
		t.Fatal(err)
	}
	if err = WritePrivate(filepath.Join(m.root, manifest.Slug, ".official.json"), Release{Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	_, cfg, _, err := m.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Grants = manifest.Capabilities
	if err = m.Configure(manifest.Slug, cfg); err != nil {
		t.Fatal(err)
	}
	if err = m.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	enqueue := func(_ context.Context, key string, value json.RawMessage) (any, error) {
		calls++
		if key != "stable" || string(value) != `{"x":1}` {
			t.Fatal(key, string(value))
		}
		return map[string]string{"id": "safe-id"}, nil
	}
	input := BusinessInput{Event: "admin.action", Payload: json.RawMessage(`{"method":"jobs.enqueue","key":"stable","value":{"x":1}}`)}
	check := func(result BusinessResult, err error, want bool) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			OK bool `json:"ok"`
		}
		if err = json.Unmarshal(result.Data, &response); err != nil || response.OK != want {
			t.Fatal(string(result.Data), err)
		}
	}
	out, err := m.DispatchAdmin(t.Context(), manifest.Slug, input, nil, enqueue)
	check(out, err, true)
	out, err = m.Dispatch(t.Context(), manifest.Slug, input, nil)
	check(out, err, false)
	input.Event = "content.render"
	out, err = m.Dispatch(t.Context(), manifest.Slug, input, nil)
	check(out, err, false)
	_, cfg, _, err = m.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Grants = []string{"render.fragment"}
	if err = m.Configure(manifest.Slug, cfg); err != nil {
		t.Fatal(err)
	}
	input.Event = "admin.action"
	out, err = m.DispatchAdmin(t.Context(), manifest.Slug, input, nil, enqueue)
	check(out, err, false)
	if calls != 1 {
		t.Fatalf("host calls=%d", calls)
	}
}
