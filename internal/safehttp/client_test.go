package safehttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestAddressAndURLBoundary(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "::ffff:10.0.0.1", "169.254.169.254", "100.64.0.1", "168.63.129.16", "192.88.99.1", "192.0.2.3", "240.0.0.1", "2001:db8::1", "64:ff9b::a00:1", "fc00::1", "100::1", "100:0:0:1::1", "2001:2::1", "2001:20::1", "3fff::1", "5f00::1", "4000::1", "6000::1", "8000::1", "fec0::1", "::192.168.1.1"} {
		if PublicIP(netip.MustParseAddr(ip)) {
			t.Fatal(ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !PublicIP(netip.MustParseAddr(ip)) {
			t.Fatal(ip)
		}
	}
	for _, raw := range []string{"http://ipwho.is/", "https://127.0.0.1/", "https://[::1]/", "https://168.63.129.16/", "https://192.88.99.1/", "https://user:secret@ipwho.is/", "https://example.com:8443/", "https://example.com:/", "https://example.com/#", "https://example.com/#secret"} {
		if _, err := ValidateURL(raw); err == nil {
			t.Fatal(raw)
		}
	}
	if PublicPrefix(netip.MustParsePrefix("168.63.129.16/32")) {
		t.Fatal("Azure platform address accepted as a public origin prefix")
	}
	if PublicPrefix(netip.MustParsePrefix("192.88.99.0/24")) {
		t.Fatal("deprecated 6to4 relay prefix accepted as a public origin prefix")
	}
}

type responseTransport func(*http.Request) (*http.Response, error)

func (f responseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicClientValidatesEveryRequestBeforeTransport(t *testing.T) {
	client := Client(time.Second)
	transport := client.Transport.(*publicTransport).transport
	forwarded := 0
	probe := responseTransport(func(r *http.Request) (*http.Response, error) {
		forwarded++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})
	transport.RegisterProtocol("https", probe)
	transport.RegisterProtocol("http", probe)
	transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		t.Error("validation regression attempted a network connection")
		return nil, errors.New("network disabled in test")
	}
	for _, raw := range []string{
		"http://example.com:443/plaintext", "https://user:secret@example.com/", "https://example.com:8443/",
		"https://example.com./", "https://example.com/#token",
		"https://127.0.0.1/", "https://[4000::1]/", "https://169.254.169.254/",
	} {
		if response, err := client.Get(raw); err == nil {
			response.Body.Close()
			t.Errorf("invalid target accepted: %s", raw)
		}
	}
	r, _ := http.NewRequest("GET", "https://example.com/", nil)
	r.Host = "metadata.google.internal"
	if response, err := client.Do(r); err == nil {
		response.Body.Close()
		t.Error("Host override accepted")
	}
	if forwarded != 0 {
		t.Fatalf("%d rejected requests reached the transport", forwarded)
	}
	for _, raw := range []string{"https://example.com/", "https://example.com:443/path?q=allowed"} {
		response, err := client.Get(raw)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
	if forwarded != 2 {
		t.Fatalf("valid requests forwarded: %d", forwarded)
	}
	client.CloseIdleConnections()
}

type closeProbe struct {
	io.Reader
	closed bool
}

func (p *closeProbe) Close() error { p.closed = true; return nil }

func TestPublicClientClosesRejectedBody(t *testing.T) {
	body := &closeProbe{Reader: strings.NewReader("sensitive payload")}
	r, _ := http.NewRequest("POST", "http://example.com:443/", body)
	if _, err := Client(time.Second).Do(r); err == nil {
		t.Fatal("plaintext request accepted")
	}
	if !body.closed {
		t.Fatal("rejected request leaked its body")
	}
}

func FuzzPublicURLBoundary(f *testing.F) {
	for _, raw := range []string{"https://example.com/", "http://example.com:443", "https://user:secret@example.com", "https://[::ffff:127.0.0.1]/", "https://example.com:", "https://[4000::1]/"} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		u, err := ValidateURL(raw)
		if err != nil {
			return
		}
		if u.Scheme != "https" || u.User != nil || u.Opaque != "" || strings.Contains(raw, "#") || (u.Port() != "" && u.Port() != "443") {
			t.Fatal("accepted URL escaped public HTTPS policy")
		}
		if a, err := netip.ParseAddr(u.Hostname()); err == nil && !PublicIP(a) {
			t.Fatal("accepted URL addresses a nonpublic endpoint")
		}
		if _, err := ValidateURL(u.String()); err != nil {
			t.Fatal("URL normalization changed validation result")
		}
	})
}
