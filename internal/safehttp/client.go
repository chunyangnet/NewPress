// Package safehttp implements bounded, HTTPS-only public Internet requests.
package safehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var blocked = func() []netip.Prefix {
	var p []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10",
		"168.63.129.16/32", // Azure platform virtual IP.
		"192.0.0.0/24", "192.0.2.0/24",
		"192.88.99.0/24", // Deprecated 6to4 relay anycast.
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"::/96", "fec0::/10", "2001:db8::/32", "2001::/23", "2002::/16", "3fff::/20", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "100:0:0:1::/64", "5f00::/16",
	} {
		p = append(p, netip.MustParsePrefix(s))
	}
	return p
}()

func PublicIP(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.Zone() != "" {
		return false
	}
	// IPv6 global-unicast space is currently allocated from 2000::/3.
	// IsGlobalUnicast alone also accepts unallocated and special-purpose space.
	if a.Is6() && !netip.MustParsePrefix("2000::/3").Contains(a) {
		return false
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// PublicPrefix is deliberately narrower than PublicIP: origin trust lists may
// not include private/reserved space hidden inside a broad public prefix.
func PublicPrefix(p netip.Prefix) bool {
	if !p.IsValid() || p.Addr().Is4In6() || !PublicIP(p.Masked().Addr()) || (p.Addr().Is4() && p.Bits() < 8) || (p.Addr().Is6() && p.Bits() < 16) {
		return false
	}
	for _, reserved := range blocked {
		if p.Overlaps(reserved) {
			return false
		}
	}
	// These IPv4 blocks can be hidden inside an otherwise public /8.
	return !p.Overlaps(netip.MustParsePrefix("172.16.0.0/12")) && !p.Overlaps(netip.MustParsePrefix("192.168.0.0/16")) && !p.Overlaps(netip.MustParsePrefix("169.254.0.0/16"))
}

func ValidateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || strings.Contains(raw, "#") || u.Hostname() == "" || (u.Port() != "" && u.Port() != "443") || strings.HasSuffix(u.Hostname(), ".") || strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("仅支持公网 HTTPS 地址（443 端口），不接受凭据或片段")
	}
	if a, err := netip.ParseAddr(u.Hostname()); err == nil && !PublicIP(a) {
		return nil, errors.New("不能访问本地、内网或保留地址")
	}
	return u, nil
}

type publicTransport struct {
	transport *http.Transport
}

func (t *publicTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r == nil || r.URL == nil {
		if r != nil && r.Body != nil {
			r.Body.Close()
		}
		return nil, errors.New("missing public request URL")
	}
	reject := func(err error) (*http.Response, error) {
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, err
	}
	if _, err := ValidateURL(r.URL.String()); err != nil {
		return reject(err)
	}
	if r.Host != "" && !strings.EqualFold(r.Host, r.URL.Host) {
		return reject(errors.New("public request Host override rejected"))
	}
	return t.transport.RoundTrip(r)
}

func (t *publicTransport) CloseIdleConnections() { t.transport.CloseIdleConnections() }

func Client(timeout time.Duration) *http.Client {
	return ClientWithHeaderTimeout(timeout, timeout)
}

// SingleAttemptClient uses fresh HTTP/1 connections with no connection reuse.
// It retains the public DNS/URL and redirect policy. Callers must also avoid a
// replayable request body and must never retry Do after an ambiguous response.
func SingleAttemptClient(timeout time.Duration) *http.Client {
	c := Client(timeout)
	t := c.Transport.(*publicTransport).transport
	t.DisableKeepAlives = true
	t.DisableCompression = true
	t.Protocols = new(http.Protocols)
	t.Protocols.SetHTTP1(true)
	return c
}

// ClientWithHeaderTimeout retains the public request policy while allowing a
// streaming caller to bound response headers separately from the full request.
func ClientWithHeaderTimeout(timeout, headerTimeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if headerTimeout <= 0 || headerTimeout > timeout {
		headerTimeout = timeout
	}
	tr := &http.Transport{TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: headerTimeout, MaxResponseHeaderBytes: 64 << 10, MaxIdleConns: 8, MaxConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" {
			return nil, errors.New("invalid remote address")
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("remote DNS unavailable")
		}
		for _, a := range addresses {
			if !PublicIP(a) {
				return nil, errors.New("nonpublic remote address rejected")
			}
		}
		for _, a := range addresses {
			conn, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(a.String(), port))
			if e == nil {
				return conn, nil
			}
		}
		return nil, errors.New("remote connection unavailable")
	}
	return &http.Client{Transport: &publicTransport{transport: tr}, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
