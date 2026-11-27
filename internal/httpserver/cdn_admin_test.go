package httpserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/cdn"
	"newpress.local/newpress/internal/coreui"
	"newpress.local/newpress/internal/plugin"
)

func newCDNHTTP(t *testing.T) (*Server, http.Handler, *http.Cookie, auth.Principal) {
	t.Helper()
	s, h, _, cookie, p := newCommentHTTPTestServer(t)
	if e := s.plugins.InstallBundled(plugin.CDN); e != nil {
		t.Fatal(e)
	}
	s.SetCDN(cdn.New(s.plugins, "https://example.test"))
	t.Cleanup(s.cdn.Close)
	return s, h, cookie, p
}
func cdnHTTPSettings() cdn.Settings {
	c := cdn.Defaults()
	c.Profiles = []cdn.Profile{{ID: "cloudflare", Name: "主站 CF", Provider: "cloudflare", ResourceID: "zone123", Domains: []string{"example.test", "static.example.test"}, Secret: "cdn-secret-sentinel", AutoPurge: true}}
	return c
}
func TestCDNHTTPAuthorizationSecretsAndQueue(t *testing.T) {
	s, h, cookie, p := newCDNHTTP(t)
	c := cdnHTTPSettings()
	payload, _ := json.Marshal(map[string]any{"settings": c, "expected_user_id": p.UserID})
	for _, tc := range []struct {
		name         string
		cookie       *http.Cookie
		csrf, origin string
		want         int
	}{{"session", &http.Cookie{Name: auth.CookieName, Value: ""}, p.CSRFToken, "https://example.test", 401}, {"csrf", cookie, "", "https://example.test", 403}, {"origin", cookie, p.CSRFToken, "https://elsewhere.test", 403}} {
		t.Run(tc.name, func(t *testing.T) {
			r := serveAdminCommentAPI(h, tc.cookie, tc.csrf, "PUT", "https://example.test/api/v1/admin/plugins/cdn/settings", string(payload), "", tc.origin)
			if r.Code != tc.want {
				t.Fatal(r.Code, r.Body.String())
			}
		})
	}
	w := serveAdminCommentAPI(h, cookie, p.CSRFToken, "PUT", "https://example.test/api/v1/admin/plugins/cdn/settings", string(payload), "", "https://example.test")
	if w.Code != 200 || strings.Contains(w.Body.String(), "cdn-secret-sentinel") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/admin/plugins/cdn/status", "/api/v1/admin/plugins/cdn/export?kind=settings", "/api/v1/admin/plugins/cdn/export?kind=jobs", "/admin/plugins/cdn/"} {
		r := serveAdminCommentAPI(h, cookie, "", "GET", "https://example.test"+path, "", "", "")
		if r.Code != 200 || strings.Contains(r.Body.String(), "cdn-secret-sentinel") || !strings.Contains(r.Header().Get("Cache-Control"), "no-store") {
			t.Fatal(path, r.Code, r.Body.String()[:min(300, r.Body.Len())])
		}
	}
	w = serveAdminCommentAPI(h, cookie, p.CSRFToken, "PUT", "https://example.test/api/v1/admin/plugins/cdn/settings", string(payload), "", "https://example.test")
	if w.Code != 409 {
		t.Fatal("stale save", w.Code)
	}
	if e := s.plugins.SetEnabled(plugin.CDN, true); e != nil {
		t.Fatal(e)
	}
	action := map[string]any{"action": "enqueue", "operation": "purge_all", "profile_id": "cloudflare", "expected_version": 2, "expected_user_id": p.UserID}
	post := func() *httptest.ResponseRecorder {
		b, _ := json.Marshal(action)
		return serveAdminCommentAPI(h, cookie, p.CSRFToken, "POST", "https://example.test/api/v1/admin/plugins/cdn/action", string(b), "", "https://example.test")
	}
	if w = post(); w.Code != 400 {
		t.Fatal("missing confirmation", w.Code, w.Body.String())
	}
	action["confirm_broad"] = true
	if w = post(); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	action["expected_user_id"] = p.UserID + 999
	if w = post(); w.Code != 409 {
		t.Fatal("identity not enforced", w.Code, w.Body.String())
	}
	state, _ := s.cdn.Status()
	if len(state.Jobs) != 1 || state.Jobs[0].Status != "queued" {
		t.Fatal(state.Jobs)
	}
}
func TestCDNReadOnlyPreviewContainsNoConfiguration(t *testing.T) {
	s, h, _, _ := newCDNHTTP(t)
	if _, e := s.cdn.Save(cdnHTTPSettings(), nil); e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("GET", "http://localhost/admin/plugins/cdn/", nil)
	r.RemoteAddr = "127.0.0.1:49152"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 && (strings.Contains(w.Body.String(), "cdn-secret-sentinel") || strings.Contains(w.Body.String(), "zone123") || strings.Contains(w.Body.String(), "static.example.test")) {
		t.Fatal("unauthenticated preview leaked settings")
	}
	for _, path := range []string{"status", "export?kind=settings"} {
		r = httptest.NewRequest("GET", "http://localhost/api/v1/admin/plugins/cdn/"+path, nil)
		r.RemoteAddr = "127.0.0.1:49152"
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == 200 {
			t.Fatal("unauthenticated read succeeded")
		}
	}
}
func TestCDNAssetDeliveryCSPAndDisable(t *testing.T) {
	s, h, cookie, _ := newCDNHTTP(t)
	c := cdnHTTPSettings()
	c.AssetBase = "https://static.example.test"
	if _, e := s.cdn.Save(c, nil); e != nil {
		t.Fatal(e)
	}
	if e := s.plugins.SetEnabled(plugin.CDN, true); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.cdn.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if base, _ := s.cdn.Delivery(); base != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	page := serveCommentRequest(h, nil, "GET", "https://example.test/", "", "", "8.8.8.8:80")
	if !strings.Contains(page.Body.String(), `https://static.example.test/theme-assets/`) || !strings.Contains(page.Header().Get("Content-Security-Policy"), "style-src 'self' https://static.example.test") {
		t.Fatal("CDN page did not bind assets/CSP", page.Code)
	}
	admin := serveCommentRequest(h, cookie, "GET", "https://example.test/admin/plugins/cdn/", "", "", "8.8.8.8:80")
	if admin.Code != 200 || !strings.Contains(admin.Body.String(), "Cloudflare 优选") || !strings.Contains(admin.Body.String(), coreui.AssetURL("js/chunnel-cdn.js")) || strings.Contains(admin.Body.String(), `src="https://static.example.test/`) {
		t.Fatal("admin assets moved off-site")
	}
	asset := serveCommentRequest(h, nil, "GET", "https://example.test"+s.themes.AssetURL("js/chunnel-time.js"), "", "", "8.8.8.8:80")
	if asset.Code != 200 || asset.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("asset CORS", asset.Code)
	}
	if e := s.plugins.SetEnabled(plugin.CDN, false); e != nil {
		t.Fatal(e)
	}
	if base, _ := s.cdn.Delivery(); base != "" {
		t.Fatal("disabled plugin still delivers assets")
	}
}

// Opt-in browser fixture, isolated from the user's data and cloud accounts.
// It runs real authenticated routes and persistent queue operations, but never
// starts cloud workers. Launch with NEWPRESS_CDN_UI=1 and stop the test process.
func TestCDNInteractiveUI(t *testing.T) {
	if os.Getenv("NEWPRESS_CDN_UI") != "1" {
		t.Skip("interactive browser verification only")
	}
	s, _, cookie, _ := newCDNHTTP(t)
	s.config.PublicURL = "http://127.0.0.1:19086"
	if _, e := s.cdn.Save(cdnHTTPSettings(), nil); e != nil {
		t.Fatal(e)
	}
	if _, e := s.cdn.SaveRouting(cdnRoutingHTTPConfig(), nil, 2); e != nil {
		t.Fatal(e)
	}
	if e := s.plugins.SetEnabled(plugin.CDN, true); e != nil {
		t.Fatal(e)
	}
	h := s.routes()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__qa", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/plugins/cdn/", http.StatusFound)
	})
	mux.Handle("/", h)
	listener, e := net.Listen("tcp", "127.0.0.1:19086")
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	t.Log("CDN UI fixture listening on 127.0.0.1:19086; no cloud worker")
	select {
	case <-t.Context().Done():
	case <-time.After(15 * time.Minute):
	}
}
