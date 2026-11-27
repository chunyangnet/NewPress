package httpserver

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"newpress.local/newpress/internal/plugin"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

func installTaskFixture(t *testing.T, s *Server, module ...string) {
	t.Helper()
	root := t.TempDir()
	s.plugins = plugin.NewManager(root)
	manifest := plugin.Manifest{Schema: 1, Slug: "task-fixture", Name: "Task fixture", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm", Capabilities: []string{"settings.read"}, Hooks: map[string][]string{"task.execute.v1": {"np_handle"}, "event.deliver.v1": {"np_handle"}}}
	moduleName := "host-bridge.wasm"
	if len(module) > 0 {
		moduleName = module[0]
	}
	code, err := os.ReadFile(filepath.Join("..", "plugin", "testdata", moduleName))
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
		f, e := writer.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write(data); e != nil {
			t.Fatal(e)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.zip")
	if err = os.WriteFile(path, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.plugins.InstallPackage(path); err != nil {
		t.Fatal(err)
	}
	if err = plugin.WritePrivate(filepath.Join(root, manifest.Slug, ".official.json"), plugin.Release{Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	_, cfg, _, err := s.plugins.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Grants = manifest.Capabilities
	if err = s.plugins.Configure(manifest.Slug, cfg); err != nil {
		t.Fatal(err)
	}
	if err = s.plugins.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
}

func TestPluginTaskHTTPEnqueueReplay(t *testing.T) {
	s, handler, _, cookie, principal := newCommentHTTPTestServer(t)
	installTaskFixture(t, s)
	backend, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	queue := &pluginjobs.Queue{Backend: backend}
	s.SetPluginTaskStore(queue)
	url := "https://example.test/api/v1/admin/plugins/task-fixture/tasks"
	payload := fmt.Sprintf(`{"expected_user_id":%d,"key":"stable-request","payload":{}}`, principal.UserID)
	for range 2 {
		response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, url, payload, "", "https://example.test")
		if response.Code != 202 {
			t.Fatalf("enqueue: %d %s", response.Code, response.Body.String())
		}
	}
	state, err := backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 1 {
		t.Fatalf("dedup: %v %v", len(state.Jobs), err)
	}
	for _, job := range state.Jobs {
		if job.ActorID != principal.UserID || job.Kind != "task.execute.v1" {
			t.Fatal("host identity not bound")
		}
	}
	response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, url, strings.Replace(payload, `"payload":{}`, `"payload":{"different":true}`, 1), "", "https://example.test")
	if response.Code != 409 {
		t.Fatalf("fingerprint conflict: %d %s", response.Code, response.Body.String())
	}
}

func TestPluginTaskHTTPListAndCancel(t *testing.T) {
	s, handler, _, cookie, principal := newCommentHTTPTestServer(t)
	backend, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	queue := &pluginjobs.Queue{Backend: backend}
	s.SetPluginTaskStore(queue)
	job, err := queue.Enqueue(t.Context(), pluginjobs.Input{Plugin: "task-fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "private-idempotency-key", ActorID: principal.UserID, Payload: json.RawMessage(`{"secret":"never expose"}`)}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	base := "https://example.test/api/v1/admin/plugins/task-fixture/tasks"
	response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, base, "", "", "https://example.test")
	if response.Code != 200 {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	var page pluginjobs.Page
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || page.Total != 1 {
		t.Fatalf("page: %+v %v", page, err)
	}
	for _, secret := range []string{"never expose", "private-idempotency-key", "payload", "lease_token", "actor_id", job.Epoch} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	for _, query := range []string{"?offset=-1", "?limit=101", "?status=invalid", "?unknown=1", "?limit=oops"} {
		response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, base+query, "", "", "https://example.test")
		if response.Code < 400 || response.Code >= 500 {
			t.Fatalf("invalid query %s: %d", query, response.Code)
		}
	}
	payload := fmt.Sprintf(`{"expected_user_id":%d,"version":%d,"action":"cancel"}`, principal.UserID, job.Version)
	response = serveAdminCommentAPI(handler, cookie, "", http.MethodPost, base+"/"+job.ID, payload, "", "https://example.test")
	if response.Code != 403 {
		t.Fatalf("missing CSRF: %d", response.Code)
	}
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, strings.Replace(base, "task-fixture", "other-plugin", 1)+"/"+job.ID, payload, "", "https://example.test")
	if response.Code != 409 {
		t.Fatalf("cross-plugin change: %d", response.Code)
	}
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, base+"/"+job.ID, payload, "", "https://example.test")
	if response.Code != 200 {
		t.Fatalf("cancel: %d %s", response.Code, response.Body.String())
	}
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, base+"/"+job.ID, payload, "", "https://example.test")
	if response.Code != 409 {
		t.Fatalf("stale cancel: %d", response.Code)
	}
	page, err = queue.List(t.Context(), pluginjobs.Query{Plugin: job.Plugin})
	if err != nil || page.Items[0].Status != "cancelled" {
		t.Fatalf("cancel not persisted: %+v %v", page, err)
	}
}
