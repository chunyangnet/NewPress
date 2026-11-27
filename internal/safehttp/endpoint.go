package safehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// endpointIP permits explicitly approved local services, but not link-local,
// unspecified, multicast, reserved, or known cloud platform addresses.
func endpointIP(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.Zone() != "" || a == netip.MustParseAddr("fd00:ec2::254") || a == netip.MustParseAddr("168.63.129.16") {
		return false
	}
	return PublicIP(a) || a.IsLoopback() || a.IsPrivate()
}

func endpointPath(path string) bool {
	if strings.ContainsAny(path, "\\%") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

// ValidateEndpoint is only for an endpoint explicitly approved by an
// administrator. Ordinary outbound requests must keep using ValidateURL.
func ValidateEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Hostname() == "" || strings.HasSuffix(u.Hostname(), ".") || strings.HasSuffix(u.Host, ":") || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || !endpointPath(u.Path) {
		return nil, errors.New("invalid approved endpoint")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("invalid endpoint port")
		}
	}
	if a, err := netip.ParseAddr(u.Hostname()); err == nil && !endpointIP(a) {
		return nil, errors.New("reserved endpoint address rejected")
	}
	return u, nil
}

type endpointTransport struct {
	base      *url.URL
	transport *http.Transport
}

func (t *endpointTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u := r.URL
	prefix := strings.TrimRight(t.base.Path, "/")
	if u.Scheme != t.base.Scheme || !strings.EqualFold(u.Host, t.base.Host) || (r.Host != "" && !strings.EqualFold(r.Host, t.base.Host)) || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawPath != "" || !endpointPath(u.Path) || (u.Path != prefix && !strings.HasPrefix(u.Path, prefix+"/")) {
		return nil, errors.New("request outside approved endpoint")
	}
	return t.transport.RoundTrip(r)
}

// EndpointClient is confined to one approved scheme, host, port and path.
// It has no proxy or redirects, checks DNS results before dialing the checked
// IPs directly, and retains normal TLS certificate verification. Short-lived
// transports do not retain connection pools after a response is closed.
func EndpointClient(raw string, timeout, headerTimeout time.Duration) (*http.Client, error) {
	u, err := ValidateEndpoint(raw)
	if err != nil {
		return nil, err
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	tr := &http.Transport{TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: headerTimeout, DisableKeepAlives: true}
	tr.DialContext = endpointDialer(u.Hostname(), port, net.DefaultResolver.LookupNetIP)
	return &http.Client{Transport: &endpointTransport{base: u, transport: tr}, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func endpointDialer(expectedHost, expectedPort string, lookup func(context.Context, string, string) ([]netip.Addr, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, remotePort, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(host, expectedHost) || remotePort != expectedPort {
			return nil, errors.New("unapproved remote address")
		}
		addresses, err := lookup(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("remote DNS unavailable")
		}
		for _, a := range addresses {
			if !endpointIP(a) {
				return nil, errors.New("reserved remote address rejected")
			}
		}
		for _, a := range addresses {
			conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(a.String(), remotePort))
			if err == nil {
				return conn, nil
			}
		}
		return nil, errors.New("remote connection unavailable")
	}
}
