package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"newpress.local/newpress/internal/config"
)

func cdnTestResolver() clientIPResolver {
	// Explicit fixture networks; these are not a published CDN trust list.
	return newClientIPResolver([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("172.30.0.0/24")},
		config.CDNProxy{Provider: "cloudflare", CIDRs: []netip.Prefix{netip.MustParsePrefix("173.245.48.0/20")}},
		config.CDNProxy{Provider: "esa", CIDRs: []netip.Prefix{netip.MustParsePrefix("47.246.0.0/16")}},
		config.CDNProxy{Provider: "edgeone", CIDRs: []netip.Prefix{netip.MustParsePrefix("43.174.0.0/16")}},
	)
}

func TestCDNClientIPTrustAndFallback(t *testing.T) {
	for _, test := range []struct {
		name, peer, xff, header, value, want, source, issue string
	}{
		{"direct forgery", "9.9.9.9", "173.245.48.1", "CF-Connecting-IP", "8.8.8.8", "9.9.9.9", "tcp", ""},
		{"Cloudflare", "173.245.48.1", "1.1.1.1", "CF-Connecting-IP", "8.8.8.8", "8.8.8.8", "cloudflare", ""},
		{"ESA", "47.246.1.1", "1.1.1.1", "Ali-Real-Client-IP", "8.8.8.8", "8.8.8.8", "esa", ""},
		{"EdgeOne", "43.174.1.1", "1.1.1.1", "EO-Connecting-IP", "8.8.8.8", "8.8.8.8", "edgeone", ""},
		{"local proxy verifies CDN", "172.30.0.2", "1.1.1.1, 173.245.48.1", "CF-Connecting-IP", "8.8.8.8", "8.8.8.8", "cloudflare", ""},
		{"multiple local proxies", "127.0.0.1", "1.1.1.1, 43.174.1.1, 172.30.0.2", "EO-Connecting-IP", "8.8.8.8", "8.8.8.8", "edgeone", ""},
		{"closest untrusted hop", "172.30.0.2", "173.245.48.1, 9.9.9.9", "CF-Connecting-IP", "8.8.8.8", "9.9.9.9", "xff", ""},
		{"wrong provider header", "47.246.1.1", "8.8.8.8", "CF-Connecting-IP", "8.8.8.8", "47.246.1.1", "esa", "missing_cdn_header"},
		{"do not recover from XFF", "43.174.1.1", "8.8.8.8", "EO-Connecting-IP", "bogus", "43.174.1.1", "edgeone", "invalid_cdn_header"},
		{"CDN private client", "43.174.1.1", "", "EO-Connecting-IP", "127.0.0.1", "43.174.1.1", "edgeone", "nonpublic_cdn_client"},
		{"Worker synthetic address", "173.245.48.1", "", "CF-Connecting-IP", "2a06:98c0:3600::103", "173.245.48.1", "cloudflare", "worker_subrequest"},
		{"stacked providers", "43.174.1.1", "8.8.8.8", "EO-Connecting-IP", "173.245.48.1", "43.174.1.1", "edgeone", "upstream_proxy"},
		{"unrelated True-Client-IP ignored", "47.246.1.1", "", "True-Client-IP", "8.8.8.8", "47.246.1.1", "esa", "missing_cdn_header"},
		{"mapped visitor", "47.246.1.1", "", "Ali-Real-Client-IP", "::ffff:8.8.8.8", "8.8.8.8", "esa", ""},
		{"loopback missing XFF", "127.0.0.1", "", "", "", "127.0.0.1", "proxy", "missing_forwarded_chain"},
		{"all local hops", "127.0.0.1", "172.30.0.2", "", "", "127.0.0.1", "proxy", "all_hops_trusted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
			r.RemoteAddr = test.peer + ":443"
			if test.xff != "" {
				r.Header.Set("X-Forwarded-For", test.xff)
			}
			if test.header != "" {
				r.Header.Set(test.header, test.value)
			}
			got := cdnTestResolver().resolve(r)
			if got.Address.String() != test.want || got.Source != test.source || got.Issue != test.issue || got.Reliable != (test.issue == "") {
				t.Fatalf("resolution=%+v, want %s/%s/%s", got, test.want, test.source, test.issue)
			}
		})
	}
}

func TestCDNRejectsMalformedHeadersAndPseudoIPv4(t *testing.T) {
	resolver := cdnTestResolver()
	for _, values := range [][]string{{"8.8.8.8", "1.1.1.1"}, {"8.8.8.8, 1.1.1.1"}, {"8.8.8.8:443"}, {"fe80::1%eth0"}, {""}} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "173.245.48.1:443"
		for _, value := range values {
			r.Header.Add("CF-Connecting-IP", value)
		}
		if got := resolver.resolve(r); got.Reliable {
			t.Fatalf("accepted malformed header %q: %+v", values, got)
		}
	}
	for _, ipv6 := range []string{"", "8.8.8.8", "::ffff:8.8.8.8", "::1", "2001:db8::1", "2a06:98c0:3600::103", "2606:4700:4700::1111"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "173.245.48.1:443"
		r.Header.Set("CF-Connecting-IP", "240.12.34.56")
		if ipv6 != "" {
			r.Header.Set("CF-Connecting-IPv6", ipv6)
		}
		got := resolver.resolve(r)
		if ipv6 == "2606:4700:4700::1111" {
			if !got.Reliable || got.Address.String() != ipv6 || got.Source != "cloudflare-ipv6" {
				t.Fatalf("IPv6 lost: %+v", got)
			}
		} else if got.Reliable {
			t.Fatalf("accepted bad pseudo IPv6 %s: %+v", ipv6, got)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "173.245.48.1:443"
	r.Header.Set("CF-Connecting-IP", "8.8.8.8")
	r.Header.Set("CF-Worker", "example.test")
	if got := resolver.resolve(r); got.Reliable || got.Issue != "worker_subrequest" {
		t.Fatalf("trusted Worker: %+v", got)
	}
}

func TestUnreliableLoopbackProxyCannotOpenAdminPreview(t *testing.T) {
	s := &Server{clientIPs: cdnTestResolver()}
	for _, xff := range []string{"", "bad", "127.0.0.1", "8.8.8.8,,127.0.0.1"} {
		r := httptest.NewRequest("GET", "/admin/", nil)
		r.Host = "localhost"
		r.RemoteAddr = "127.0.0.1:8000"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		if s.isLoopbackRequest(r) {
			t.Fatalf("preview allowed for unreliable XFF %q", xff)
		}
	}
	s.clientIPs = newClientIPResolver(nil)
	r := httptest.NewRequest("GET", "/admin/", nil)
	r.Host = "localhost"
	r.RemoteAddr = "127.0.0.1:8000"
	if !s.isLoopbackRequest(r) {
		t.Fatal("direct loopback preview broken")
	}
}

func TestCDNOriginTokenRequiresBothNetworkAndSecret(t *testing.T) {
	resolver := cdnTestResolver()
	const token = "fixture-origin-token-32-bytes-long"
	resolver.cdnProxies[2].OriginToken = token
	for _, test := range []struct {
		name, peer string
		tokens     []string
		reliable   bool
		source     string
	}{
		{"missing", "43.174.1.1", nil, false, "edgeone"},
		{"incorrect", "43.174.1.1", []string{"bad"}, false, "edgeone"},
		{"duplicate", "43.174.1.1", []string{token, token}, false, "edgeone"},
		{"valid", "43.174.1.1", []string{token}, true, "edgeone"},
		{"untrusted sender with secret", "9.9.9.9", []string{token}, true, "tcp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = test.peer + ":80"
			r.Header.Set("EO-Connecting-IP", "8.8.8.8")
			for _, value := range test.tokens {
				r.Header.Add("X-NewPress-Origin-Token", value)
			}
			got := resolver.resolve(r)
			if got.Reliable != test.reliable || got.Source != test.source {
				t.Fatalf("resolution=%+v", got)
			}
			if !test.reliable && got.Issue != "origin_auth_failed" {
				t.Fatalf("missing reason: %+v", got)
			}
			if test.source == "tcp" && got.Address.String() != test.peer {
				t.Fatal("token bypassed network boundary")
			}
		})
	}
}
