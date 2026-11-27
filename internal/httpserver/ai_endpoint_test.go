package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"newpress.local/newpress/internal/aieditor"
)

func TestAIApprovedEndpointAdminProbeAndAuthorUsage(t *testing.T) {
	s, handler, manager, _, owner, principal := newPersistentAdminTestServer(t)
	_, author, authorPrincipal := createAndLoginTestUser(t, manager, principal, "local-ai-author", "local-ai-author@example.test", "作者", "author")
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken}
	authorHeaders := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": authorPrincipal.CSRFToken}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer local-test-secret" {
			t.Error("saved key did not reach approved endpoint")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"本地 AI 正文。"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	probe, _ := json.Marshal(map[string]any{"base_url": upstream.URL, "api_key": "local-test-secret", "allow_insecure": true})
	for _, who := range []struct {
		cookie  *http.Cookie
		headers map[string]string
		want    int
	}{{author, authorHeaders, 403}, {owner, headers, 200}} {
		response := serveContentAPI(handler, who.cookie, "POST", "https://example.test/api/v1/admin/ai/models", string(probe), who.headers)
		if response.Code != who.want || strings.Contains(response.Body.String(), "local-test-secret") {
			t.Fatalf("probe: %d %s", response.Code, response.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatal("unauthorized probe reached local service")
	}
	unapproved := strings.Replace(string(probe), `"allow_insecure":true`, `"allow_insecure":false`, 1)
	response := serveContentAPI(handler, owner, "POST", "https://example.test/api/v1/admin/ai/models", unapproved, headers)
	if response.Code != 400 {
		t.Fatal("unapproved local address accepted", response.Code)
	}
	settings := s.aiSettings.Snapshot()
	settings.DefaultProvider = "local"
	settings.Providers = []aieditor.Provider{{ID: "local", Name: "本地", BaseURL: upstream.URL, APIKey: "local-test-secret", AllowInsecure: true, Models: []string{"local-model"}}}
	encoded, _ := json.Marshal(settings)
	response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/settings", string(encoded), authorHeaders)
	if response.Code != 403 {
		t.Fatal("author approved endpoint", response.Code)
	}
	response = serveContentAPI(handler, owner, "POST", "https://example.test/api/v1/admin/ai/settings", string(encoded), headers)
	if response.Code != 200 || strings.Contains(response.Body.String(), "local-test-secret") {
		t.Fatalf("save: %d %s", response.Code, response.Body.String())
	}
	response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/models", `{"provider_id":"local"}`, authorHeaders)
	if response.Code != 200 {
		t.Fatalf("saved probe: %d %s", response.Code, response.Body.String())
	}
	generation := `{"provider_id":"local","model":"local-model","prompt_id":"article","brief":"写作主题","tone":"natural","length":"short","language":"zh-CN"}`
	response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/generate", generation, authorHeaders)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"complete":true`) {
		t.Fatalf("generation: %d %s", response.Code, response.Body.String())
	}
	before := calls.Load()
	for _, input := range []string{
		strings.Replace(generation, `"provider_id":"local"`, `"provider_id":"local","allow_insecure":true`, 1),
		strings.Replace(generation, `"provider_id":"local"`, `"provider_id":"local","base_url":"http://localhost:1234/v1"`, 1),
		strings.Replace(generation, `"provider_id":"local"`, `"base_url":"`+upstream.URL+`","api_key":"key","allow_insecure":true`, 1),
	} {
		response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/generate", input, authorHeaders)
		if response.Code != 400 {
			t.Fatalf("untrusted generation: %d %s", response.Code, response.Body.String())
		}
	}
	response = serveContentAPI(handler, owner, "POST", "https://example.test/api/v1/admin/ai/models", `{"provider_id":"local","allow_insecure":true}`, headers)
	if response.Code != 400 {
		t.Fatal("ambiguous approval accepted")
	}
	if calls.Load() != before {
		t.Fatal("rejected requests reached local service")
	}
}
