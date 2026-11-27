package config

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"

	"newpress.local/newpress/internal/safehttp"
)

// CDNProxy binds a provider's header semantics to its actual origin egress
// networks. These networks must not be put in the generic XFF proxy list.
type CDNProxy struct {
	Provider    string
	CIDRs       []netip.Prefix
	OriginToken string `json:"-"`
}

const maximumCDNListBytes = 128 << 10
const maximumCDNPrefixes = 2048

func loadCDNProxies() ([]CDNProxy, error) {
	var proxies []CDNProxy
	for _, provider := range []string{"cloudflare", "esa", "edgeone"} {
		key := "NEWPRESS_CDN_" + strings.ToUpper(provider) + "_CIDRS"
		value, err := boundedCDNEnvOrFile(key, maximumCDNListBytes)
		if err != nil {
			return nil, err
		}
		prefixes, err := parseCDNCIDRs(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		tokenKey := "NEWPRESS_CDN_" + strings.ToUpper(provider) + "_ORIGIN_TOKEN"
		token, err := boundedCDNEnvOrFile(tokenKey, 512)
		if err != nil {
			return nil, err
		}
		token = strings.TrimSpace(token)
		if token != "" {
			if len(token) < 32 || strings.IndexFunc(token, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
				return nil, fmt.Errorf("%s: use 32–512 printable ASCII characters without spaces", tokenKey)
			}
			if len(prefixes) == 0 {
				return nil, fmt.Errorf("%s requires configured CDN CIDRs", tokenKey)
			}
		}
		if len(prefixes) > 0 {
			proxies = append(proxies, CDNProxy{Provider: provider, CIDRs: prefixes, OriginToken: token})
		}
	}
	return proxies, nil
}

func boundedCDNEnvOrFile(key string, maximum int64) (string, error) {
	value, exists := os.LookupEnv(key)
	if !exists && strings.TrimSpace(os.Getenv(key+"_FILE")) != "" {
		file, err := os.Open(strings.TrimSpace(os.Getenv(key + "_FILE")))
		if err != nil {
			return "", fmt.Errorf("%s_FILE: cannot open file", key)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s_FILE: must be a regular file", key)
		}
		data, err := io.ReadAll(io.LimitReader(file, maximum+1))
		if err != nil {
			return "", fmt.Errorf("%s_FILE: cannot read file", key)
		}
		value = string(data)
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%s_FILE: file is empty", key)
		}
	}
	if int64(len(value)) > maximum {
		return "", fmt.Errorf("%s exceeds %d bytes", key, maximum)
	}
	return value, nil
}

func parseCDNCIDRs(value string) ([]netip.Prefix, error) {
	if len(value) > maximumCDNListBytes {
		return nil, errors.New("CIDR list exceeds 128 KiB")
	}
	var result []netip.Prefix
	seen := map[netip.Prefix]bool{}
	count := 0
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, raw := range strings.Split(line, ",") {
			count++
			if count > maximumCDNPrefixes {
				return nil, errors.New("CIDR list exceeds 2048 entries")
			}
			prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
			if err != nil || !safehttp.PublicPrefix(prefix) {
				return nil, errors.New("each CDN CIDR must contain only public addresses (IPv4 /8 or narrower, IPv6 /16 or narrower)")
			}
			prefix = prefix.Masked()
			if !seen[prefix] {
				seen[prefix] = true
				result = append(result, prefix)
			}
		}
	}
	return result, nil
}

func validateCDNOverlap(proxies []CDNProxy, local []netip.Prefix) error {
	for i, proxy := range proxies {
		for _, prefix := range proxy.CIDRs {
			for _, other := range local {
				if prefix.Overlaps(other) {
					return fmt.Errorf("%s CDN CIDRs overlap NEWPRESS_TRUSTED_PROXY_CIDRS; keep CDN and local proxy networks separate", proxy.Provider)
				}
			}
			for _, otherProxy := range proxies[:i] {
				for _, other := range otherProxy.CIDRs {
					if prefix.Overlaps(other) {
						return fmt.Errorf("%s and %s CDN CIDRs overlap", proxy.Provider, otherProxy.Provider)
					}
				}
			}
		}
	}
	return nil
}
