package safehttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestApprovedEndpointValidation(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:11434/v1", "http://localhost:1234/v1", "https://192.168.1.12:8443/api", "http://[::1]:1234/v1", "http://[fd01::12]/v1", "http://provider.example/v1"} {
		if _, err := ValidateEndpoint(raw); err != nil {
			t.Errorf("approved %s: %v", raw, err)
		}
		if _, err := ValidateURL(raw); err == nil {
			t.Errorf("default policy accepted %s", raw)
		}
	}
	for _, raw := range []string{"file:///etc/passwd", "ftp://localhost/", "http://user:secret@localhost/", "http://localhost/?key=secret", "http://localhost/#fragment", "http://localhost:/v1", "http://localhost:0/v1", "http://localhost:65536/v1", "http://localhost./v1", "http://localhost/v1/../admin", "http://localhost/v1/%2e%2e/admin", "http://localhost/v1/%252e%252e/admin", "http://0.0.0.0/v1", "http://169.254.169.254/v1", "http://100.100.100.200/v1", "http://168.63.129.16/v1", "http://[fd00:ec2::254]/v1", "http://[fe80::1]/v1", "http://[::]/v1", "http://[ff02::1]/v1", "http://[::ffff:169.254.169.254]/v1", "http://192.0.2.1/v1"} {
		if _, err := ValidateEndpoint(raw); err == nil {
			t.Errorf("accepted reserved or malformed %s", raw)
		}
	}
}

func TestApprovedEndpointClientScopeRedirectsAndProxy(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/v1/redirect" {
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			t.Errorf("escaped base path: %s", r.URL.Path)
		}
		w.Write([]byte("ok"))
	}))
	defer server.Close()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	client, err := EndpointClient(server.URL+"/v1", time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/models?page=2", "/v1/redirect"} {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if path == "/v1/redirect" && resp.StatusCode != 302 {
			t.Fatal("followed redirect")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected redirect request")
	}
	for _, target := range []string{server.URL + "/admin", server.URL + "/v10/models", server.URL + "/v1/../admin", server.URL + "/v1/%2e%2e/admin", server.URL + "/v1/%252e%252e/admin", strings.Replace(server.URL, "127.0.0.1", "localhost", 1) + "/v1/models", "http://127.0.0.1:1/v1/models", strings.Replace(server.URL, "http:", "https:", 1) + "/v1/models"} {
		resp, err := client.Get(target)
		if err == nil {
			resp.Body.Close()
			t.Errorf("escaped scope: %s", target)
		}
	}
	req, _ := http.NewRequest("GET", server.URL+"/v1/models", nil)
	req.Host = "other-service.example"
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
		t.Error("Host override accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("rejected request reached server")
	}
}

func TestApprovedEndpointDNSChecksAndDirectDial(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("checked IP")) }))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	client, _ := EndpointClient("http://unresolvable.example:"+port+"/v1", time.Second, time.Second)
	tr := client.Transport.(*endpointTransport).transport
	lookupCalls := 0
	lookup := func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		lookupCalls++
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	tr.DialContext = endpointDialer("unresolvable.example", port, lookup)
	resp, err := client.Get("http://unresolvable.example:" + port + "/v1/models")
	if err != nil {
		t.Fatal("checked IP must be dialed directly", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "checked IP" || lookupCalls != 1 {
		t.Fatal("incorrect DNS/dial sequence")
	}
	tr.DialContext = endpointDialer("unresolvable.example", port, func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("169.254.169.254")}, nil
	})
	if _, err := client.Get("http://unresolvable.example:" + port + "/v1/models"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatal("mixed DNS answers not blocked", err)
	}
}

func TestApprovedEndpointRetainsTLSVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS certificate accepted") }))
	defer server.Close()
	client, _ := EndpointClient(server.URL+"/v1", 3*time.Second, 3*time.Second)
	if resp, err := client.Get(server.URL + "/v1/models"); err == nil {
		resp.Body.Close()
		t.Fatal("TLS verification bypassed")
	}
}
