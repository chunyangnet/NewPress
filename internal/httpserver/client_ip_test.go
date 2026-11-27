package httpserver

import (
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestClientIPResolverTrustBoundaryAndForwardedChain(t *testing.T) {
	resolver := newClientIPResolver([]netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.30.0.0/24"),
		netip.MustParsePrefix("fd00::/8"),
	})
	tests := []struct {
		name       string
		remote     string
		forwarded  []string
		realIP     string
		useDefault bool
		want       string
	}{
		{name: "direct request", remote: "198.51.100.7:4000", want: "198.51.100.7"},
		{name: "untrusted peer cannot forge", remote: "198.51.100.7:4000", forwarded: []string{"192.0.2.9"}, want: "198.51.100.7"},
		{name: "untrusted real IP ignored", remote: "198.51.100.7:4000", realIP: "192.0.2.9", want: "198.51.100.7"},
		{name: "trusted edge", remote: "172.30.0.2:4000", forwarded: []string{"198.51.100.8"}, want: "198.51.100.8"},
		{
			name: "rightmost trusted hops stripped", remote: "172.30.0.4:4000",
			forwarded: []string{"192.0.2.250, 198.51.100.20, 172.30.0.3"}, want: "198.51.100.20",
		},
		{
			name: "multiple header fields preserve order", remote: "172.30.0.4:4000",
			forwarded: []string{"192.0.2.250", "198.51.100.20, 172.30.0.3"}, want: "198.51.100.20",
		},
		{name: "all forwarded hops trusted", remote: "172.30.0.4:4000", forwarded: []string{"10.2.0.8, 172.30.0.3"}, want: "172.30.0.4"},
		{name: "IPv6 chain", remote: "[fd00::4]:4000", forwarded: []string{"[2001:db8::7], fd00::3"}, want: "2001:db8::7"},
		{name: "mapped IPv4 normalized", remote: "172.30.0.2:4000", forwarded: []string{"::ffff:198.51.100.9"}, want: "198.51.100.9"},
		{name: "malformed chain falls back", remote: "172.30.0.2:4000", forwarded: []string{"not-an-ip, 198.51.100.8"}, want: "172.30.0.2"},
		{name: "empty chain member falls back", remote: "172.30.0.2:4000", forwarded: []string{"198.51.100.8,,172.30.0.3"}, want: "172.30.0.2"},
		{name: "address with port rejected", remote: "172.30.0.2:4000", forwarded: []string{"198.51.100.8:1234"}, want: "172.30.0.2"},
		{name: "zone rejected", remote: "172.30.0.2:4000", forwarded: []string{"fe80::1%eth0"}, want: "172.30.0.2"},
		{name: "bracketed peer without port", remote: "[2001:db8::20]", want: "2001:db8::20"},
		{name: "default resolver trusts nobody", remote: "172.30.0.2:4000", forwarded: []string{"198.51.100.8"}, useDefault: true, want: "172.30.0.2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := resolver
			if test.useDefault {
				current = newClientIPResolver(nil)
			}
			request := httptest.NewRequest("GET", "http://example.test/", nil)
			request.RemoteAddr = test.remote
			for _, value := range test.forwarded {
				request.Header.Add("X-Forwarded-For", value)
			}
			if test.realIP != "" {
				request.Header.Set("X-Real-IP", test.realIP)
			}
			if got := current.string(request); got != test.want {
				t.Fatalf("client address = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClientIPResolverBoundsForwardedChain(t *testing.T) {
	resolver := newClientIPResolver([]netip.Prefix{netip.MustParsePrefix("172.30.0.0/24")})
	for name, forwarded := range map[string]string{
		"too many hops":  strings.Repeat("198.51.100.8,", maximumForwardedForHops) + "198.51.100.9",
		"too many bytes": strings.Repeat(" ", maximumForwardedForBytes) + "198.51.100.9",
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://example.test/", nil)
			request.RemoteAddr = "172.30.0.2:4000"
			request.Header.Set("X-Forwarded-For", forwarded)
			if got := resolver.string(request); got != "172.30.0.2" {
				t.Fatalf("bounded address = %q", got)
			}
		})
	}
}

func TestClientIPResolverControlsLoopbackPreview(t *testing.T) {
	server := &Server{clientIPs: newClientIPResolver([]netip.Prefix{netip.MustParsePrefix("172.30.0.0/24")})}
	external := httptest.NewRequest("GET", "http://example.test/admin/", nil)
	external.RemoteAddr = "172.30.0.2:4000"
	external.Header.Set("X-Forwarded-For", "198.51.100.8")
	if server.isLoopbackRequest(external) {
		t.Fatal("external client behind a trusted local proxy was treated as loopback")
	}
	local := httptest.NewRequest("GET", "http://localhost/admin/", nil)
	local.RemoteAddr = "172.30.0.2:4000"
	local.Header.Set("X-Forwarded-For", "127.0.0.1")
	if !server.isLoopbackRequest(local) {
		t.Fatal("trusted proxy loopback client was not treated as loopback")
	}
}
