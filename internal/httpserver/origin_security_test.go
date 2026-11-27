package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"newpress.local/newpress/internal/config"
)

func TestRequestOriginRejectsAmbiguousAndForeignSources(t *testing.T) {
	s := &Server{config: config.Config{PublicURL: "https://example.test"}}
	for _, tc := range []struct {
		name    string
		headers http.Header
		want    bool
	}{
		{"non-browser", http.Header{}, true},
		{"same-origin", http.Header{"Origin": {"https://example.test"}}, true},
		{"referer fallback", http.Header{"Referer": {"https://example.test/admin/"}}, true},
		{"foreign referer", http.Header{"Referer": {"https://evil.test/"}}, false},
		{"duplicate origin", http.Header{"Origin": {"https://example.test", "https://evil.test"}}, false},
		{"duplicate referer", http.Header{"Referer": {"https://example.test/", "https://evil.test/"}}, false},
		{"origin credentials", http.Header{"Origin": {"https://evil@example.test"}}, false},
		{"origin path", http.Header{"Origin": {"https://example.test/path"}}, false},
		{"origin query", http.Header{"Origin": {"https://example.test?secret"}}, false},
		{"origin fragment", http.Header{"Origin": {"https://example.test#secret"}}, false},
		{"null origin", http.Header{"Origin": {"null"}}, false},
		{"cross-site", http.Header{"Origin": {"https://example.test"}, "Sec-Fetch-Site": {"cross-site"}}, false},
		{"duplicate fetch metadata", http.Header{"Origin": {"https://example.test"}, "Sec-Fetch-Site": {"same-origin", "cross-site"}}, false},
		{"combined fetch metadata", http.Header{"Origin": {"https://example.test"}, "Sec-Fetch-Site": {"same-origin, cross-site"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://example.test/admin/login/", nil)
			r.Header = tc.headers
			if got := s.sameOriginRequest(r); got != tc.want {
				t.Fatalf("sameOriginRequest = %t, want %t", got, tc.want)
			}
			if !tc.want && s.sameOriginPublicWrite(r) {
				t.Fatal("public writes accepted an invalid source")
			}
		})
	}
}

func TestLoopbackPreviewRejectsReboundHost(t *testing.T) {
	s := &Server{config: config.Config{PublicURL: "https://example.test"}, clientIPs: newClientIPResolver(nil)}
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"example.test", true}, {"localhost:8080", true}, {"127.0.0.1:8080", true}, {"[::1]:8080", true},
		{"attacker.test:8080", false}, {"localhost.attacker.test:8080", false}, {"attacker@localhost:8080", false},
	} {
		r := httptest.NewRequest("GET", "https://example.test/api/v1/system", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Host = tc.host
		r.Header.Set("X-Forwarded-Host", "example.test")
		if got := s.isLoopbackRequest(r); got != tc.want {
			t.Errorf("host %q: got %t, want %t", tc.host, got, tc.want)
		}
	}
}

func TestAdminLoginRejectsQueryCredentialsAndAmbiguousForms(t *testing.T) {
	s, handler, _, _, _, _ := newPersistentAdminTestServer(t)
	valid := url.Values{"username": {"owner"}, "password": {"Persistent owner password 2026"}}.Encode()
	for _, tc := range []struct{ name, query, body, contentType string }{
		{"query credentials", valid, "", "application/x-www-form-urlencoded"},
		{"mixed query", "password=secret", "username=owner", "application/x-www-form-urlencoded"},
		{"duplicate password", "", valid + "&password=other-password", "application/x-www-form-urlencoded"},
		{"unexpected field", "", valid + "&admin=true", "application/x-www-form-urlencoded"},
		{"invalid UTF8", "", "username=owner&password=%ff", "application/x-www-form-urlencoded"},
		{"wrong media type", "", valid, "text/plain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", s.config.PublicURL+"/admin/login/?"+tc.query, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			r.Header.Set("Origin", s.config.PublicURL)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest && w.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("invalid login form reached authentication: status %d", w.Code)
			}
			if len(w.Result().Cookies()) != 0 {
				t.Fatal("invalid login issued a cookie")
			}
		})
	}
}

func TestLoopbackPreviewHTTPRejectsForeignHost(t *testing.T) {
	s := mockupTestServer(t, "")
	handler := s.routes()
	for _, path := range []string{"/admin/", "/api/v1/system", "/api/v1/admin/session"} {
		for _, host := range []string{"attacker.test", "example.test"} {
			r := httptest.NewRequest("GET", "https://"+host+path, nil)
			r.RemoteAddr = "127.0.0.1:1234"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := http.StatusOK
			if host == "attacker.test" {
				want = http.StatusNotFound
			}
			if w.Code != want {
				t.Errorf("%s%s: HTTP %d, want %d", host, path, w.Code, want)
			}
		}
	}
}

func TestAdminLogoutRejectsQueryTokenWithoutRevokingSession(t *testing.T) {
	_, handler, manager, _, cookie, principal := newPersistentAdminTestServer(t)
	r := httptest.NewRequest("POST", "https://example.test/admin/logout/?csrf_token="+url.QueryEscape(principal.CSRFToken), nil)
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://example.test")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("query token accepted: HTTP %d", w.Code)
	}
	if _, ok := manager.ValidateSessionContext(t.Context(), cookie.Value); !ok {
		t.Fatal("invalid logout revoked the session")
	}
}

func TestAdminAuthRejectsAmbiguousMediaTypeAndOversizeBodies(t *testing.T) {
	_, handler, manager, _, cookie, principal := newPersistentAdminTestServer(t)
	for _, path := range []string{"/admin/login/", "/admin/logout/"} {
		for _, tc := range []struct {
			name         string
			body         string
			contentTypes []string
			status       int
		}{
			{"duplicate media types", "csrf_token=" + url.QueryEscape(principal.CSRFToken), []string{"application/x-www-form-urlencoded", "text/plain"}, http.StatusUnsupportedMediaType},
			{"oversize body", "password=" + strings.Repeat("a", 32<<10), []string{"application/x-www-form-urlencoded"}, http.StatusRequestEntityTooLarge},
		} {
			t.Run(path+tc.name, func(t *testing.T) {
				r := httptest.NewRequest("POST", "https://example.test"+path, strings.NewReader(tc.body))
				r.Header.Set("Origin", "https://example.test")
				for _, value := range tc.contentTypes {
					r.Header.Add("Content-Type", value)
				}
				if path == "/admin/logout/" {
					r.AddCookie(cookie)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != tc.status {
					t.Fatalf("HTTP %d, want %d", w.Code, tc.status)
				}
				if len(w.Result().Cookies()) != 0 {
					t.Fatal("invalid form changed session cookies")
				}
				if _, ok := manager.ValidateSessionContext(t.Context(), cookie.Value); !ok {
					t.Fatal("invalid form revoked the valid session")
				}
			})
		}
	}
}
