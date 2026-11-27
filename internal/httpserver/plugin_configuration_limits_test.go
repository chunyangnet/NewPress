package httpserver

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"newpress.local/newpress/internal/plugin"
)

func TestPluginConfigurationHTTPRejectsUnreadableJSON(t *testing.T) {
	s, handler, _, cookie, principal := newCommentHTTPTestServer(t)
	root := t.TempDir()
	s.plugins = plugin.NewManager(root)
	manifest := plugin.Manifest{Schema: 1, Slug: "configuration-boundary", Name: "Configuration boundary", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm",
		Capabilities: []string{"settings.read", "settings.write"}, Hooks: map[string][]string{"admin.action": {"np_handle"}},
	}
	for i := range 3 {
		manifest.Settings = append(manifest.Settings, plugin.Setting{Key: fmt.Sprintf("field_%d", i), Label: "Text", Type: "text", Default: "", Max: 4000})
	}
	code, err := os.ReadFile(filepath.Join("..", "plugin", "testdata", "host-bridge.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, data := range map[string][]byte{"plugin.json": definition, "plugin.wasm": code} {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "configuration-boundary.zip")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.plugins.InstallPackage(archivePath); err != nil {
		t.Fatal(err)
	}
	write := func(config plugin.Configuration) *httptest.ResponseRecorder {
		t.Helper()
		var payload bytes.Buffer
		encoder := json.NewEncoder(&payload)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(map[string]any{"configuration": config, "expected_user_id": principal.UserID}); err != nil {
			t.Fatal(err)
		}
		if payload.Len() >= 64<<10 {
			t.Fatalf("fixture must pass request body limit: %d bytes", payload.Len())
		}
		t.Logf("configuration request: %d bytes", payload.Len())
		return serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, "https://example.test/api/v1/admin/plugins/"+manifest.Slug+"/configuration", payload.String(), "", "https://example.test")
	}
	_, initial, _, err := s.plugins.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	initial.Grants = manifest.Capabilities
	if response := write(initial); response.Code != http.StatusOK {
		t.Fatalf("initial save: %d %s", response.Code, response.Body.String())
	}
	_, baseline, _, err := s.plugins.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, manifest.Slug, "data", "runtime.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	candidate := baseline
	candidate.Values = map[string]any{}
	for _, field := range manifest.Settings {
		candidate.Values[field.Key] = strings.Repeat("&", 4000)
	}
	response := write(candidate)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "64 KiB") {
		t.Errorf("expected explicit size rejection: %d %s", response.Code, response.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Errorf("rejected request replaced configuration: bytes=%d err=%v", len(after), err)
	}
	s.plugins = plugin.NewManager(root)
	_, reloaded, _, err := s.plugins.Configuration(manifest.Slug)
	if err != nil {
		t.Fatalf("new manager cannot read previous configuration: %v", err)
	}
	if !reflect.DeepEqual(reloaded, baseline) {
		t.Fatal("rejected save changed values, permissions or version")
	}
	reloaded.Values["field_0"] = "shortened"
	if response := write(reloaded); response.Code != http.StatusOK {
		t.Fatalf("valid retry: %d %s", response.Code, response.Body.String())
	}
}
