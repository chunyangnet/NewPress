package httpserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/netip"
	"strings"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/safehttp"
)

const (
	maximumForwardedForBytes = 4096
	maximumForwardedForHops  = 32
)

// clientIPResolver accepts X-Forwarded-For only across an explicitly trusted
// TCP boundary. It strictly parses the bounded chain, removes trusted hops
// from right to left and returns the nearest untrusted address, so a
// client-controlled value cannot override the first untrusted hop.
type clientIPResolver struct {
	trustedProxyCIDRs []netip.Prefix
	cdnProxies        []config.CDNProxy
}

type clientIPResolution struct {
	Address  netip.Addr
	Source   string
	Issue    string
	Reliable bool
}

func newClientIPResolver(prefixes []netip.Prefix, cdns ...config.CDNProxy) clientIPResolver {
	resolver := clientIPResolver{trustedProxyCIDRs: append([]netip.Prefix(nil), prefixes...)}
	for _, cdn := range cdns {
		resolver.cdnProxies = append(resolver.cdnProxies, config.CDNProxy{Provider: cdn.Provider, CIDRs: append([]netip.Prefix(nil), cdn.CIDRs...), OriginToken: cdn.OriginToken})
	}
	return resolver
}

func (resolver clientIPResolver) string(request *http.Request) string {
	if address, ok := resolver.address(request); ok {
		return address.String()
	}
	return "unknown"
}

func (resolver clientIPResolver) address(request *http.Request) (netip.Addr, bool) {
	result := resolver.resolve(request)
	return result.Address, result.Address.IsValid()
}

func (resolver clientIPResolver) resolve(request *http.Request) clientIPResolution {
	direct, ok := directPeerAddress(request)
	if !ok {
		return clientIPResolution{Issue: "invalid_peer"}
	}
	if provider := resolver.provider(direct); provider != "" {
		return resolver.fromCDN(request, direct, provider)
	}
	if !resolver.trusted(direct) {
		return clientIPResolution{Address: direct, Source: "tcp", Reliable: true}
	}

	fallback := clientIPResolution{Address: direct, Source: "proxy", Issue: "invalid_forwarded_chain"}
	values := request.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		fallback.Issue = "missing_forwarded_chain"
		return fallback
	}
	if len(values) > maximumForwardedForHops {
		return fallback
	}
	parts := make([]string, 0, len(values))
	totalBytes := 0
	for _, value := range values {
		totalBytes += len(value)
		if totalBytes > maximumForwardedForBytes {
			return fallback
		}
		entries := strings.Split(value, ",")
		if len(parts)+len(entries) > maximumForwardedForHops {
			return fallback
		}
		parts = append(parts, entries...)
	}
	if len(parts) == 0 {
		return fallback
	}

	addresses := make([]netip.Addr, len(parts))
	for index, part := range parts {
		candidate, err := parseForwardedAddress(part)
		if err != nil {
			return fallback
		}
		addresses[index] = candidate
	}

	// Walk from the application toward the client. The nearest untrusted hop
	// is authoritative; values before it may have been supplied by that client.
	for index := len(addresses) - 1; index >= 0; index-- {
		candidate := addresses[index]
		if provider := resolver.provider(candidate); provider != "" {
			return resolver.fromCDN(request, candidate, provider)
		}
		if !resolver.trusted(candidate) {
			return clientIPResolution{Address: candidate, Source: "xff", Reliable: true}
		}
	}
	fallback.Issue = "all_hops_trusted"
	return fallback
}

func (resolver clientIPResolver) provider(address netip.Addr) string {
	provider := ""
	for _, cdn := range resolver.cdnProxies {
		for _, prefix := range cdn.CIDRs {
			if prefix.Contains(address) {
				if provider != "" && provider != cdn.Provider {
					return "ambiguous"
				}
				provider = cdn.Provider
			}
		}
	}
	return provider
}

func (resolver clientIPResolver) fromCDN(request *http.Request, peer netip.Addr, provider string) clientIPResolution {
	result := clientIPResolution{Address: peer, Source: provider, Issue: "invalid_cdn_header"}
	for _, proxy := range resolver.cdnProxies {
		if proxy.Provider != provider || proxy.OriginToken == "" {
			continue
		}
		values := request.Header.Values("X-NewPress-Origin-Token")
		if len(values) != 1 || len(values[0]) > 512 {
			result.Issue = "origin_auth_failed"
			return result
		}
		provided, expected := sha256.Sum256([]byte(values[0])), sha256.Sum256([]byte(proxy.OriginToken))
		if subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			result.Issue = "origin_auth_failed"
			return result
		}
	}
	header := ""
	switch provider {
	case "cloudflare":
		header = "CF-Connecting-IP"
		// Worker subrequests may contain a synthetic or script-controlled IP.
		if len(request.Header.Values("CF-Worker")) > 0 {
			result.Issue = "worker_subrequest"
			return result
		}
	case "esa":
		header = "Ali-Real-Client-IP"
	case "edgeone":
		header = "EO-Connecting-IP"
	default:
		result.Issue = "ambiguous_cdn"
		return result
	}
	address, err := singleClientAddress(request.Header, header)
	if err != nil {
		if len(request.Header.Values(header)) == 0 {
			result.Issue = "missing_cdn_header"
		}
		return result
	}
	if provider == "cloudflare" {
		if netip.MustParsePrefix("240.0.0.0/4").Contains(address) {
			address, err = singleClientAddress(request.Header, "CF-Connecting-IPv6")
			if err != nil || !address.Is6() || address.Is4In6() {
				result.Issue = "invalid_pseudo_ipv6"
				return result
			}
			result.Source = "cloudflare-ipv6"
		}
		if address == netip.MustParseAddr("2a06:98c0:3600::103") {
			result.Issue = "worker_subrequest"
			return result
		}
	}
	if !safehttp.PublicIP(address) {
		result.Issue = "nonpublic_cdn_client"
		return result
	}
	if resolver.provider(address) != "" || resolver.trusted(address) {
		result.Issue = "upstream_proxy"
		return result
	}
	result.Address, result.Reliable, result.Issue = address, true, ""
	return result
}

func singleClientAddress(header http.Header, name string) (netip.Addr, error) {
	values := header.Values(name)
	if len(values) != 1 || len(values[0]) > 64 {
		return netip.Addr{}, errInvalidForwardedAddress
	}
	return parseForwardedAddress(values[0])
}

func (resolver clientIPResolver) trusted(address netip.Addr) bool {
	for _, prefix := range resolver.trustedProxyCIDRs {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func directPeerAddress(request *http.Request) (netip.Addr, bool) {
	if request == nil {
		return netip.Addr{}, false
	}
	value := strings.TrimSpace(request.RemoteAddr)
	if addressPort, err := netip.ParseAddrPort(value); err == nil {
		return normalizeIPAddress(addressPort.Addr())
	}
	if len(value) >= 2 && value[0] == '[' && value[len(value)-1] == ']' {
		value = value[1 : len(value)-1]
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, false
	}
	return normalizeIPAddress(address)
}

func parseForwardedAddress(value string) (netip.Addr, error) {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '[' && value[len(value)-1] == ']' {
		value = value[1 : len(value)-1]
	}
	address, err := netip.ParseAddr(value)
	if err != nil || address.Zone() != "" {
		return netip.Addr{}, errInvalidForwardedAddress
	}
	normalized, ok := normalizeIPAddress(address)
	if !ok {
		return netip.Addr{}, errInvalidForwardedAddress
	}
	return normalized, nil
}

func normalizeIPAddress(address netip.Addr) (netip.Addr, bool) {
	if !address.IsValid() {
		return netip.Addr{}, false
	}
	return address.WithZone("").Unmap(), true
}

var errInvalidForwardedAddress = errors.New("invalid X-Forwarded-For address")
