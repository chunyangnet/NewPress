package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/cdn"
	"newpress.local/newpress/internal/plugin"
)

func cdnRoutingHTTPConfig() cdn.RoutingConfig {
	return cdn.RoutingConfig{Version: 1, DNS: []cdn.DNSProfile{{ID: "dns", Name: "分区 DNS", Provider: "aliyun", Zone: "customer.test", AccessKey: "test-access", Secret: "routing-secret-sentinel"}}, Routes: []cdn.Route{{ID: "site", Name: "访客接入", CFProfileID: "cloudflare", DNSProfileID: "dns", Hostname: "app.customer.test", FallbackOrigin: "origin.example.test", Target: "customers.example.test", PreferredTarget: "preferred.example.net", PreferredLines: []string{}, TTL: 600, HealthPath: "/health.txt", Interval: 300, FailureThreshold: 3, RecoveryThreshold: 3, Cooldown: 900}}}
}
func TestCDNRoutingAuthorizationPrivacyAndQueue(t *testing.T) {
	s, h, cookie, p := newCDNHTTP(t)
	if _, e := s.cdn.Save(cdnHTTPSettings(), nil); e != nil {
		t.Fatal(e)
	}
	config := cdnRoutingHTTPConfig()
	payload, _ := json.Marshal(map[string]any{"config": config, "cdn_version": 2, "expected_user_id": p.UserID})
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
		csrf   string
		origin string
		want   int
	}{{"session", &http.Cookie{Name: auth.CookieName}, p.CSRFToken, "https://example.test", 401}, {"csrf", cookie, "", "https://example.test", 403}, {"origin", cookie, p.CSRFToken, "https://other.test", 403}} {
		t.Run(tc.name, func(t *testing.T) {
			w := serveAdminCommentAPI(h, tc.cookie, tc.csrf, "PUT", "https://example.test/api/v1/admin/plugins/cdn/routing/settings", string(payload), "", tc.origin)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	w := serveAdminCommentAPI(h, cookie, p.CSRFToken, "PUT", "https://example.test/api/v1/admin/plugins/cdn/routing/settings", string(payload), "", "https://example.test")
	if w.Code != 200 || strings.Contains(w.Body.String(), "routing-secret-sentinel") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/admin/plugins/cdn/routing", "/api/v1/admin/plugins/cdn/export?kind=routing", "/api/v1/admin/plugins/cdn/export?kind=routing-plans", "/admin/plugins/cdn/"} {
		w = serveAdminCommentAPI(h, cookie, "", "GET", "https://example.test"+path, "", "", "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "routing-secret-sentinel") || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal(path, w.Code)
		}
		w = serveAdminCommentAPI(h, &http.Cookie{Name: auth.CookieName}, "", "GET", "https://example.test"+path, "", "", "")
		if w.Code == 200 && (strings.Contains(w.Body.String(), "customer.test") || path != "/admin/plugins/cdn/") {
			t.Fatal("unauthenticated routing leak", path)
		}
	}
	w = serveAdminCommentAPI(h, cookie, p.CSRFToken, "PUT", "https://example.test/api/v1/admin/plugins/cdn/routing/settings", string(payload), "", "https://example.test")
	if w.Code != 409 {
		t.Fatal("stale config", w.Code)
	}
	if e := s.plugins.SetEnabled(plugin.CDN, true); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		user int64
		csrf string
		want int
	}{{p.UserID + 1, p.CSRFToken, 409}, {p.UserID, "", 403}, {p.UserID, p.CSRFToken, 202}} {
		body, _ := json.Marshal(map[string]any{"action": "inspect", "route_id": "site", "expected_version": 2, "cdn_version": 2, "expected_user_id": tc.user})
		w = serveAdminCommentAPI(h, cookie, tc.csrf, "POST", "https://example.test/api/v1/admin/plugins/cdn/routing/action", string(body), "", "https://example.test")
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	v, e := s.cdn.RoutingStatus()
	if e != nil || len(v.Tasks) != 1 || v.Tasks[0].Status != "queued" {
		t.Fatal(v, e)
	}
}
