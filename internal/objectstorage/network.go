package objectstorage

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"newpress.local/newpress/internal/safehttp"
)

func validateStorageEndpoint(p Profile) (*url.URL, error) {
	u, err := safehttp.ValidateEndpoint(p.Endpoint)
	if err != nil {
		return nil, errors.New("存储地址不合法，不接受凭据、查询参数或保留地址")
	}
	if u.Scheme != "https" && !p.AllowHTTP {
		return nil, errors.New("默认要求 HTTPS；自建服务可明确启用 HTTP")
	}
	if ip, e := netip.ParseAddr(u.Hostname()); e == nil && !safehttp.PublicIP(ip) && !p.AllowPrivate {
		return nil, errors.New("内网存储需要明确允许访问私有网络")
	}
	return u, nil
}

type storageTransport struct {
	base   *http.Transport
	hosts  map[string]bool
	scheme string
}

func (t *storageTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u := r.URL
	if u == nil || !t.hosts[strings.ToLower(u.Host)] || u.Scheme != t.scheme || u.User != nil || u.Opaque != "" || u.Fragment != "" || (r.Host != "" && !strings.EqualFold(r.Host, u.Host)) {
		return nil, errors.New("存储请求超出已配置的服务范围")
	}
	resp, err := t.base.RoundTrip(r)
	if err == nil && resp.StatusCode >= 300 && resp.Body != nil {
		resp.Body = &limitedResponseBody{Reader: io.LimitReader(resp.Body, 64<<10), Closer: resp.Body}
	}
	return resp, err
}

type limitedResponseBody struct {
	io.Reader
	io.Closer
}

func (t *storageTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }

func permittedStorageIP(a netip.Addr, allowPrivate bool) bool {
	a = a.Unmap()
	if a == netip.MustParseAddr("168.63.129.16") || a == netip.MustParseAddr("fd00:ec2::254") || a.Zone() != "" {
		return false
	}
	return safehttp.PublicIP(a) || allowPrivate && (a.IsPrivate() || a.IsLoopback())
}

// No ambient proxies or automatic redirects: credentials are bound to the
// configured endpoint. DNS is validated before dialing the actual selected IP.
func newStorageHTTPClient(p Profile) (*http.Client, error) {
	u, err := validateStorageEndpoint(p)
	if err != nil {
		return nil, err
	}
	hosts := map[string]bool{strings.ToLower(u.Host): true}
	if p.Bucket != "" && !p.PathStyle {
		hosts[strings.ToLower(p.Bucket+"."+u.Host)] = true
	}
	timeout := time.Duration(p.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	tr := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConns: 16, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 6, ExpectContinueTimeout: time.Second, ForceAttemptHTTP2: true, DisableCompression: true}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, errors.New("存储地址不正确")
		}
		expectedPort := u.Port()
		if expectedPort == "" {
			expectedPort = "443"
			if u.Scheme == "http" {
				expectedPort = "80"
			}
		}
		approved := strings.EqualFold(host, u.Hostname()) || p.Bucket != "" && !p.PathStyle && strings.EqualFold(host, p.Bucket+"."+u.Hostname())
		if !approved || port != expectedPort {
			return nil, errors.New("存储连接地址未授权")
		}
		addresses, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil || len(addresses) == 0 {
			return nil, errors.New("存储 DNS 解析失败")
		}
		for _, a := range addresses {
			if !permittedStorageIP(a, p.AllowPrivate) {
				return nil, errors.New("存储域名解析到未授权或保留地址")
			}
		}
		for _, a := range addresses {
			c, e := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(a.String(), port))
			if e == nil {
				return c, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, errors.New("存储连接失败")
	}
	return &http.Client{Transport: &storageTransport{base: tr, hosts: hosts, scheme: u.Scheme}, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
