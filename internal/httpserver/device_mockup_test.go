package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/coreui"
	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

func mockupTestServer(t *testing.T, worker string) *Server {
	t.Helper()
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	return New(config.Config{SiteTitle: "NewPress", PublicURL: "https://example.test", ActiveTheme: "chunnel", DeviceMockupURL: worker}, content.NewMemoryRepository(false), themes, plugin.NewManager(t.TempDir()))
}

func TestDeviceMockupAdminPageAndPublicRemoval(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	page := "https://example.test/admin/tools/device-mockup/"
	w := serveCookieRequest(handler, http.MethodGet, page, cookie, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("admin screenshot page = %d %s", w.Code, w.Body)
	}
	for _, marker := range []string{`data-device-mockup`, `data-csrf="` + principal.CSRFToken + `"`, `id="admin-sidebar"`, `<h1>宣传截图</h1>`, `href="/admin/tools/"`, coreui.AssetURL("css/newpress-device-mockup.css"), coreui.AssetURL("js/newpress-device-mockup.js")} {
		if !strings.Contains(w.Body.String(), marker) {
			t.Errorf("admin screenshot page missing %s", marker)
		}
	}
	if strings.Count(w.Body.String(), "<main ") != 1 || strings.Contains(w.Body.String(), `id="primary-nav"`) || strings.Contains(w.Body.String(), `href="/tools/"`) {
		t.Error("screenshot page did not use the administration shell")
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") || !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") {
		t.Error("administration page must not be cached or indexed")
	}
	for _, path := range []string{"/tools/", "/tools/device-mockup/", "/api/v1/tools/device-mockup/health"} {
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, nil, nil)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "data-device-mockup") {
			t.Errorf("retired public route %s = %d", path, response.Code)
		}
	}
	for _, endpoint := range []string{"live", "live/control", "render"} {
		response := serveCookieRequest(handler, http.MethodPost, "https://example.test/api/v1/tools/device-mockup/"+endpoint, cookie, strings.NewReader(`{}`))
		if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
			t.Errorf("retired public API %s = %d", endpoint, response.Code)
		}
	}
	home := serveCookieRequest(handler, http.MethodGet, "https://example.test/", nil, nil)
	if strings.Contains(home.Body.String(), `href="/tools/"`) || strings.Contains(home.Body.String(), "newpress-device-mockup") {
		t.Error("public homepage still exposes or loads the screenshot tool")
	}
	// Administration rendering must not depend on the active public theme.
	themeRoot := t.TempDir()
	if err := extension.CopyTree(t.Context(), filepath.Join("..", "..", "themes", "starter"), filepath.Join(themeRoot, "starter")); err != nil {
		t.Fatal(err)
	}
	s.themes = theme.NewManager(themeRoot, "starter", theme.WithSettingsDir(t.TempDir()))
	if err := s.themes.Load(); err != nil {
		t.Fatal(err)
	}
	w = serveCookieRequest(s.routes(), http.MethodGet, page, cookie, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="admin-sidebar"`) || !strings.Contains(w.Body.String(), coreui.AssetURL("js/newpress-device-mockup.js")) {
		t.Fatalf("theme-independent admin page = %d", w.Code)
	}
}

func TestDeviceMockupSavedPublicNavigationIsHidden(t *testing.T) {
	s := mockupTestServer(t, "")
	menu := navigation.Menu{Items: []navigation.Item{
		{ID: 1, URL: "/tools/"},
		{ID: 2, URL: "https://example.test/tools/device-mockup/?url=https://example.com"},
		{ID: 3, ParentID: 1, URL: "/explore/"},
		{ID: 4, URL: "/explore/"},
		{ID: 5, URL: "https://other.test/tools/"},
	}}
	filtered := s.publicNavigationMenu(menu)
	if len(filtered.Items) != 2 || filtered.Items[0].ID != 4 || filtered.Items[1].ID != 5 || len(menu.Items) != 5 {
		t.Fatalf("retired screenshot links were not filtered without rewriting saved menus: %#v", filtered.Items)
	}
	sources, err := s.navigationSources(httptest.NewRequest(http.MethodGet, "https://example.test/admin/navigation/", nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range sources {
		for _, source := range group.Sources {
			if source.URL == "/tools/" || source.URL == "/tools/device-mockup/" {
				t.Error("retired screenshot page remains in navigation choices")
			}
		}
	}
}

func TestDeviceMockupRequiresAdministratorSessionAndCSRF(t *testing.T) {
	var hits atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"ready":true,"service":"device-mockup","version":6}`)
	}))
	defer worker.Close()
	s, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	s.config.DeviceMockupURL = worker.URL
	_, editorCookie, editor := createAndLoginTestUser(t, manager, owner, "mockup-editor", "mockup-editor@example.test", "编辑", "editor")
	_, authorCookie, author := createAndLoginTestUser(t, manager, owner, "mockup-author", "mockup-author@example.test", "作者", "author")
	_, subscriberCookie, subscriber := createAndLoginTestUser(t, manager, owner, "mockup-reader", "mockup-reader@example.test", "读者", "subscriber")
	for _, tc := range []struct {
		name      string
		cookie    *http.Cookie
		principal auth.Principal
		pageCode  int
		apiCode   int
	}{
		{"anonymous", nil, auth.Principal{}, http.StatusSeeOther, http.StatusUnauthorized},
		{"editor", editorCookie, editor, http.StatusForbidden, http.StatusForbidden},
		{"author", authorCookie, author, http.StatusForbidden, http.StatusForbidden},
		{"subscriber", subscriberCookie, subscriber, http.StatusForbidden, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/device-mockup/", tc.cookie, nil)
			if page.Code != tc.pageCode {
				t.Fatalf("page = %d, want %d", page.Code, tc.pageCode)
			}
			for _, endpoint := range []string{"health", "live", "live/control", "render"} {
				method := http.MethodPost
				if endpoint == "health" {
					method = http.MethodGet
				}
				r := httptest.NewRequest(method, "https://example.test"+deviceMockupAPIPrefix+endpoint, strings.NewReader(`{}`))
				r.Header.Set("Origin", "https://example.test")
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-CSRF-Token", tc.principal.CSRFToken)
				if tc.cookie != nil {
					r.AddCookie(tc.cookie)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != tc.apiCode {
					t.Errorf("%s = %d, want %d", endpoint, w.Code, tc.apiCode)
				}
			}
		})
	}
	for _, endpoint := range []string{"live", "live/control", "render"} {
		for _, tokens := range [][]string{nil, {"wrong-token"}, {owner.CSRFToken, owner.CSRFToken}} {
			r := httptest.NewRequest(http.MethodPost, "https://example.test"+deviceMockupAPIPrefix+endpoint, strings.NewReader(`{}`))
			r.AddCookie(ownerCookie)
			r.Header.Set("Origin", "https://example.test")
			r.Header.Set("Content-Type", "application/json")
			for _, token := range tokens {
				r.Header.Add("X-CSRF-Token", token)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "auth.csrf_validation_failed") {
				t.Errorf("%s accepted invalid CSRF headers: %d", endpoint, w.Code)
			}
		}
	}
	if hits.Load() != 0 {
		t.Fatal("unauthorized requests reached the screenshot worker")
	}
	w := serveCookieRequest(handler, http.MethodGet, "https://example.test"+deviceMockupAPIPrefix+"health", ownerCookie, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ready":true`) || hits.Load() != 1 {
		t.Fatalf("administrator health = %d %s", w.Code, w.Body)
	}
	manager.RevokeSession(ownerCookie.Value)
	w = serveCookieRequest(handler, http.MethodGet, "https://example.test"+deviceMockupAPIPrefix+"health", ownerCookie, nil)
	if w.Code != http.StatusUnauthorized || hits.Load() != 1 {
		t.Fatal("revoked administrator session reached worker")
	}
}

func TestDeviceMockupRejectsUnconfiguredLoopbackPreview(t *testing.T) {
	var hits atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer worker.Close()
	s := mockupTestServer(t, worker.URL)
	s.config.PublicURL = "http://localhost:8080"
	handler := s.routes()
	for _, path := range []string{"/admin/tools/device-mockup/", deviceMockupAPIPrefix + "health", deviceMockupAPIPrefix + "live", deviceMockupAPIPrefix + "live/control", deviceMockupAPIPrefix + "render"} {
		method := http.MethodGet
		if strings.HasPrefix(path, deviceMockupAPIPrefix) && !strings.HasSuffix(path, "health") {
			method = http.MethodPost
		}
		r := httptest.NewRequest(method, "http://localhost:8080"+path, strings.NewReader(`{}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Origin", "http://localhost:8080")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("unconfigured loopback %s = %d", path, w.Code)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("unconfigured preview reached worker")
	}
}

func TestDeviceMockupProxyBoundaries(t *testing.T) {
	var version atomic.Int32
	version.Store(6)
	var hits atomic.Int32
	var received http.Header
	var receivedBody string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "service": "device-mockup", "version": version.Load()})
			return
		}
		hits.Add(1)
		received = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"screenshots":[]}`))
	}))
	defer worker.Close()
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	s.config.DeviceMockupURL = worker.URL
	post := func(origin, contentType, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://example.test"+deviceMockupAPIPrefix+"render", strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("Cookie", "private-site-cookie=secret")
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", principal.CSRFToken)
		r.Header.Set("Authorization", "Bearer secret")
		r.Header.Set("X-NewPress-Mockup-Scope", "local")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	payload := `{"url":"https://example.com"}`
	for _, tc := range []struct {
		origin, kind, body string
		want               int
	}{
		{"https://other.test", "application/json", payload, 403},
		{"", "application/json", payload, 403},
		{"https://example.test", "text/plain", payload, 415},
		{"https://example.test", "application/json", "{", 400},
		{"https://example.test", "application/json", strings.Repeat("a", 4097), 413},
	} {
		if w := post(tc.origin, tc.kind, tc.body); w.Code != tc.want {
			t.Fatalf("request boundary: got %d, want %d", w.Code, tc.want)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("invalid request reached worker")
	}
	w := post("https://example.test", "application/json", payload)
	if w.Code != 200 || receivedBody != payload {
		t.Fatalf("valid proxy = %d %s", w.Code, w.Body)
	}
	if received.Get("Cookie") != "" || received.Get("Authorization") != "" || received.Get("X-CSRF-Token") != "" || received.Get("Origin") != worker.URL || received.Get("X-NewPress-Mockup-Scope") != "public" {
		t.Fatalf("unexpected forwarded headers: %v", received)
	}
	version.Store(5)
	w = post("https://example.test", "application/json", payload)
	if w.Code != 503 || hits.Load() != 1 {
		t.Fatalf("old worker received target: %d, hits %d", w.Code, hits.Load())
	}
	worker.Close()
	w = post("https://example.test", "application/json", payload)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "截图服务") {
		t.Fatalf("unavailable worker = %d", w.Code)
	}
}

func TestDeviceMockupLocalScopeRequiresLocalSiteAndClient(t *testing.T) {
	s := mockupTestServer(t, "")
	for _, tc := range []struct {
		publicURL, remote string
		want              bool
	}{
		{"https://example.test", "127.0.0.1:1234", false},
		{"http://localhost:8080", "127.0.0.1:1234", true},
		{"http://127.0.0.1:8080", "192.0.2.1:1234", false},
		{"http://127.0.0.1:8080", "[::1]:1234", true},
	} {
		s.config.PublicURL = tc.publicURL
		r := httptest.NewRequest("POST", tc.publicURL, nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		if got := s.deviceMockupLocalRequest(r); got != tc.want {
			t.Errorf("%s from %s = %t", tc.publicURL, tc.remote, got)
		}
	}
}

func TestDeviceMockupStreamFlushesAndCancels(t *testing.T) {
	canceled := make(chan struct{})
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			_, _ = w.Write([]byte(`{"ready":true,"service":"device-mockup","version":6}`))
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, "{\"type\":\"session\",\"id\":\"stream-check\"}\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer worker.Close()
	s, _, _, _, cookie, principal := newPersistentAdminTestServer(t)
	s.config.DeviceMockupURL = worker.URL
	proxy := httptest.NewServer(s.routes())
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "POST", proxy.URL+deviceMockupAPIPrefix+"live", strings.NewReader(`{"url":"https://example.com"}`))
	r.Header.Set("Origin", "https://example.test")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", principal.CSRFToken)
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, "stream-check") {
		t.Fatalf("stream was buffered: %q, %v", line, err)
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("client disconnect did not cancel worker")
	}
}
