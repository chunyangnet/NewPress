package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/aieditor"
)

func TestAISettingsAuthorizationPersistenceAndSavedGeneration(t *testing.T) {
	s, handler, manager, _, cookie, principal := newPersistentAdminTestServer(t)
	path := filepath.Join(t.TempDir(), "ai-settings.json")
	store, err := aieditor.NewSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAISettings(store)
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken}
	_, author, authorPrincipal := createAndLoginTestUser(t, manager, principal, "settings-author", "settings-author@example.test", "作者", "author")
	_, reader, _ := createAndLoginTestUser(t, manager, principal, "settings-reader", "settings-reader@example.test", "读者", "subscriber")
	for _, test := range []struct {
		cookie *http.Cookie
		route  string
		status int
	}{
		{nil, "settings", 401}, {author, "settings", 403}, {reader, "connections", 403}, {author, "connections", 200},
	} {
		response := serveContentAPI(handler, test.cookie, "GET", "https://example.test/api/v1/admin/ai/"+test.route, "", nil)
		if response.Code != test.status {
			t.Fatalf("GET %s: %d", test.route, response.Code)
		}
	}
	for _, route := range []string{"settings", "connections"} {
		response := serveContentAPI(handler, cookie, "DELETE", "https://example.test/api/v1/admin/ai/"+route, "", headers)
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("unsupported method on %s returned %d", route, response.Code)
		}
	}
	input := aieditor.Settings{Revision: store.Snapshot().Revision, DefaultProvider: "primary", Providers: []aieditor.Provider{
		{ID: "primary", Name: "写作", BaseURL: "https://writer.example.test/v1", APIKey: "saved-private-key", Models: []string{"model-a", "model-b"}, DefaultModel: "model-a"},
		{ID: "other", Name: "备用", BaseURL: "https://second.example.test/v1", APIKey: "other-private-key", Models: []string{"model-c"}, DefaultModel: "model-c"},
	}}
	encoded, _ := json.Marshal(input)
	for _, test := range []struct {
		cookie        *http.Cookie
		token, origin string
	}{
		{author, authorPrincipal.CSRFToken, "https://example.test"}, {cookie, "", "https://example.test"}, {cookie, principal.CSRFToken, "https://foreign.test"},
	} {
		response := serveContentAPI(handler, test.cookie, "POST", "https://example.test/api/v1/admin/ai/settings", string(encoded), map[string]string{"Content-Type": "application/json", "Origin": test.origin, "X-CSRF-Token": test.token})
		if response.Code != 403 {
			t.Fatal("unauthorized AI settings saved", response.Code)
		}
	}
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/settings", string(encoded), headers)
	if response.Code != 200 || strings.Contains(response.Body.String(), "private-key") || strings.Contains(response.Body.String(), "api_key") {
		t.Fatal("save failed or credential leaked", response.Code)
	}
	reopened, err := aieditor.NewSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAISettings(reopened)
	for _, route := range []string{"/api/v1/admin/ai/settings", "/api/v1/admin/ai/connections", "/admin/settings/", "/admin/settings/export/"} {
		page := serveCookieRequest(handler, "GET", "https://example.test"+route, cookie, nil)
		if page.Code != 200 || strings.Contains(page.Body.String(), "private-key") {
			t.Fatal("settings unavailable or credential exposed", route, page.Code)
		}
		if route == "/admin/settings/" && (!strings.Contains(page.Body.String(), "site-settings-ai") || !strings.Contains(page.Body.String(), "chunnel-ai-settings.js")) {
			t.Fatal("AI settings UI missing")
		}
	}
	calls := 0
	setAIResponse(s, 200, "text/event-stream", aiFrame("<newpress-meta>{\"title\":\"测试文章\"}</newpress-meta>\n正文。", "stop")+"data: [DONE]\n\n", func(r *http.Request) {
		calls++
		if r.URL.Host != "writer.example.test" || r.Header.Get("Authorization") != "Bearer saved-private-key" {
			t.Error("wrong saved provider")
		}
		payload, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(payload), `"model":"model-b"`) || !strings.Contains(string(payload), "不靠同义词轮换") {
			t.Error("model or writing policy missing")
		}
	})
	generation := `{"provider_id":"primary","model":"model-b","prompt_id":"article","brief":"写作主题","tone":"natural","length":"short","language":"zh-CN","writing_style":"restrained"}`
	authorHeaders := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": authorPrincipal.CSRFToken}
	response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/generate", generation, authorHeaders)
	if response.Code != 200 || calls != 1 || !strings.Contains(response.Body.String(), "event: done") || strings.Contains(response.Body.String(), "private-key") {
		t.Fatal("saved generation failed", response.Code)
	}
	setAIResponse(s, 200, "application/json", `{"data":[{"id":"model-c"}]}`, func(r *http.Request) {
		if r.URL.Host != "second.example.test" || r.Header.Get("Authorization") != "Bearer other-private-key" {
			t.Error("discovery did not use selected provider")
		}
	})
	response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/models", `{"provider_id":"other"}`, authorHeaders)
	if response.Code != 200 || strings.Contains(response.Body.String(), "private-key") {
		t.Fatal("saved discovery failed")
	}
	setAIResponse(s, 200, "application/json", "{}", func(*http.Request) { t.Error("invalid saved request reached provider") })
	for _, body := range []string{strings.Replace(generation, "model-b", "unknown", 1), strings.Replace(generation, "primary", "removed", 1), strings.Replace(generation, `"provider_id":"primary"`, `"provider_id":"primary","base_url":"https://attacker.example"`, 1)} {
		response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/generate", body, authorHeaders)
		if response.Code != 400 {
			t.Fatal("invalid provider request accepted", response.Code)
		}
	}
	response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/settings", string(encoded), headers)
	if response.Code != 409 {
		t.Fatal("stale save overwrote AI settings")
	}
}
