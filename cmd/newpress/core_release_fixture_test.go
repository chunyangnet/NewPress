package main

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
)

// The release smoke test runs the real CLI, supervisor, HTTP handlers and backup
// command. Only the test binary maps the fixed private provider to a loopback fixture.
type releaseFixtureTransport struct {
	base      *url.URL
	transport http.RoundTripper
}

func (f releaseFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme == "https" && r.URL.Host == "gitee.com" {
		copy := r.Clone(r.Context())
		copy.URL.Scheme = f.base.Scheme
		copy.URL.Host = f.base.Host
		copy.Host = f.base.Host
		return f.transport.RoundTrip(copy)
	}
	return f.transport.RoundTrip(r)
}
func TestMain(m *testing.M) {
	if endpoint := os.Getenv("NEWPRESS_TEST_CORE_RELEASE_FIXTURE"); endpoint != "" {
		base, err := url.Parse(endpoint)
		if err != nil || base.Scheme != "http" || !net.ParseIP(base.Hostname()).IsLoopback() {
			os.Exit(2)
		}
		http.DefaultTransport = releaseFixtureTransport{base, http.DefaultTransport}
		main()
		return
	}
	os.Exit(m.Run())
}
