package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestDeviceMockupProxyRequiresReliableLoopbackForLocalScope(t *testing.T) {
	scopes := make(chan string, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/health" {
			_, _ = io.WriteString(w, `{"ready":true,"service":"device-mockup","version":6}`)
			return
		}
		scopes <- r.Header.Get("X-NewPress-Mockup-Scope")
		_, _ = io.WriteString(w, `{"screenshots":[]}`)
	}))
	defer worker.Close()
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	s.config.PublicURL = "http://localhost:8080"
	s.config.DeviceMockupURL = worker.URL
	for _, tc := range []struct {
		name, peer, trusted, forwarded, host, scope string
	}{
		{name: "direct local", peer: "127.0.0.1:1234"},
		{name: "verified local through proxy", peer: "172.30.0.2:1234", trusted: "172.30.0.0/24", forwarded: "127.0.0.1"},
		{name: "missing chain", peer: "127.0.0.1:1234", trusted: "127.0.0.0/8", scope: "public"},
		{name: "malformed chain", peer: "127.0.0.1:1234", trusted: "127.0.0.0/8", forwarded: "invalid, 198.51.100.1", scope: "public"},
		{name: "all hops trusted", peer: "127.0.0.1:1234", trusted: "127.0.0.0/8", forwarded: "127.0.0.2", scope: "public"},
		{name: "oversized chain", peer: "127.0.0.1:1234", trusted: "127.0.0.0/8", forwarded: strings.Repeat("198.51.100.1,", 33) + "127.0.0.2", scope: "public"},
		{name: "verified external through proxy", peer: "127.0.0.1:1234", trusted: "127.0.0.0/8", forwarded: "198.51.100.1", scope: "public"},
		{name: "untrusted forwarding", peer: "198.51.100.1:1234", forwarded: "127.0.0.1", scope: "public"},
		{name: "rebound Host", peer: "127.0.0.1:1234", host: "attacker.test:8080", scope: "public"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var prefixes []netip.Prefix
			if tc.trusted != "" {
				prefixes = append(prefixes, netip.MustParsePrefix(tc.trusted))
			}
			s.clientIPs = newClientIPResolver(prefixes)
			r := httptest.NewRequest(http.MethodPost, s.config.PublicURL+deviceMockupAPIPrefix+"render", strings.NewReader(`{"url":"http://127.0.0.1:8080"}`))
			r.RemoteAddr = tc.peer
			if tc.host != "" {
				r.Host = tc.host
			}
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			r.AddCookie(cookie)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", s.config.PublicURL)
			r.Header.Set("X-CSRF-Token", principal.CSRFToken)
			r.Header.Set("X-NewPress-Mockup-Scope", "local")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("mockup proxy status=%d body=%s", w.Code, w.Body)
			}
			select {
			case got := <-scopes:
				if got != tc.scope {
					t.Errorf("worker scope=%q, want %q", got, tc.scope)
				}
			default:
				t.Fatal("request did not reach the fixture worker")
			}
		})
	}
}
