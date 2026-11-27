package cdn

import (
	"context"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"
)

func validationRecords(r Route, h SaaSHostname, line string) ([]DNSRecord, error) {
	result := []DNSRecord{}
	add := func(name, kind, value string) error {
		name = cleanHost(name)
		if name != "_cf-custom-hostname."+r.Hostname && name != "_acme-challenge."+r.Hostname {
			return routeError("Cloudflare 验证记录名称超出当前主机名范围")
		}
		if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00") {
			return invalidRemote()
		}
		if kind == "CNAME" {
			value = cleanHost(value)
			if !validHost(value) || value == name {
				return invalidRemote()
			}
		}
		d := DNSRecord{Name: name, Type: kind, Value: value, Line: line, TTL: r.TTL, Status: "ENABLE"}
		if !slices.Contains(result, d) {
			result = append(result, d)
		}
		return nil
	}
	if h.Ownership.Name != "" {
		if !strings.EqualFold(h.Ownership.Type, "txt") {
			return nil, routeError("当前所有权验证类型暂不支持自动写入")
		}
		if e := add(h.Ownership.Name, "TXT", h.Ownership.Value); e != nil {
			return nil, e
		}
	}
	if len(h.SSL.Validation)+len(h.SSL.Delegation) > 16 {
		return nil, invalidRemote()
	}
	for _, v := range append(slices.Clone(h.SSL.Validation), h.SSL.Delegation...) {
		if v.TXTName != "" {
			if e := add(v.TXTName, "TXT", v.TXTValue); e != nil {
				return nil, e
			}
		}
		if v.CNAME != "" {
			if e := add(v.CNAME, "CNAME", v.CNAMETarget); e != nil {
				return nil, e
			}
		}
	}
	return result, nil
}

func (s *Service) inspectRoute(ctx context.Context, cf Profile, dns DNSProfile, r Route) (RouteReport, error) {
	out := RouteReport{RouteID: r.ID, Checked: time.Now().UTC(), Warnings: []string{}, Mode: "unmanaged"}
	var e error
	out.ZoneName, out.Fallback, out.Hostname, e = s.api.saasState(ctx, cf, r)
	if e != nil {
		return out, e
	}
	if e = s.api.validateProxied(ctx, cf, r); e != nil {
		return out, e
	}
	out.DNS, e = s.api.dnsZone(ctx, dns)
	if e != nil {
		return out, e
	}
	out.Validation, e = validationRecords(r, out.Hostname, out.DNS.DefaultLine)
	if e != nil {
		return out, e
	}
	names := []string{r.Hostname}
	for _, v := range out.Validation {
		if !slices.Contains(names, v.Name) {
			names = append(names, v.Name)
		}
	}
	for _, name := range names {
		records, e := s.api.dnsRecords(ctx, dns, name)
		if e != nil {
			return out, e
		}
		out.Records = append(out.Records, records...)
	}
	nsCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	ns, err := net.DefaultResolver.LookupNS(nsCtx, dns.Zone)
	cancel()
	if err != nil {
		out.Warnings = append(out.Warnings, "无法从当前服务器查询权威 NS，请检查子域委派和 DNSSEC")
	} else {
		for _, v := range ns {
			out.ActualNS = append(out.ActualNS, cleanHost(v.Host))
		}
		expected := slices.Clone(out.DNS.Nameservers)
		slices.Sort(expected)
		slices.Sort(out.ActualNS)
		out.Delegated = slices.Equal(slices.Compact(expected), slices.Compact(out.ActualNS))
	}
	if !out.Delegated {
		out.Warnings = append(out.Warnings, "当前权威 NS 与此 DNS 连接的分配结果不同；请先在原服务商完成 NS 委派及 DNSSEC 配置")
	}
	if r.TTL < out.DNS.MinTTL {
		out.Warnings = append(out.Warnings, "所配置 TTL 小于套餐要求，生成计划前需要调高")
	}
	if out.Hostname.ID == "" {
		out.Warnings = append(out.Warnings, "尚未创建 SaaS 自定义主机名")
		return out, nil
	}
	if out.Hostname.Status != "active" || out.Hostname.SSL.Status != "active" {
		out.Warnings = append(out.Warnings, "主机名或证书尚未 active；完成验证后再预览切流")
	}
	if out.Hostname.CustomOrigin != "" && cleanHost(out.Hostname.CustomOrigin) != r.FallbackOrigin {
		out.Warnings = append(out.Warnings, "此主机名使用单独的 custom_origin_server，和方案回源不同")
	}
	if expires, e := time.Parse(time.RFC3339, out.Hostname.SSL.ExpiresOn); e == nil && time.Until(expires) < 30*24*time.Hour {
		out.Warnings = append(out.Warnings, "云端证书距离过期不足 30 天，请检查续期验证记录")
	}
	for _, v := range out.Hostname.SSL.Validation {
		if v.HTTPURL != "" {
			out.Warnings = append(out.Warnings, "云端返回 HTTP 验证要求，请检查 Cloudflare 控制台；当前不会代写 HTTP 验证文件")
			break
		}
	}
	out.Mode = routeMode(r, out.Records, out.DNS.DefaultLine)
	ranges, _, err := fetchCFRanges(ctx, s.api.client)
	if err != nil {
		out.Warnings = append(out.Warnings, "无法获取官方 IP 网段，跳过健康探测与自动切换")
		return out, nil
	}
	out.Normal = probeRouteTarget(ctx, r, r.Target, ranges)
	out.Preferred = probeRouteTarget(ctx, r, r.PreferredTarget, ranges)
	for _, health := range []RouteHealth{out.Normal, out.Preferred} {
		if !health.TLSExpires.IsZero() && time.Until(health.TLSExpires) < 30*24*time.Hour {
			out.Warnings = append(out.Warnings, "实际 TLS 证书将在 30 天内到期，请核对续期状态")
			break
		}
	}
	return out, nil
}
func routeMode(r Route, records []DNSRecord, defaultLine string) string {
	if len(r.PreferredLines) == 0 {
		return "unmanaged"
	}
	normal, preferred := 0, 0
	defaults := 0
	for _, d := range records {
		if d.Name != r.Hostname || d.Status != "ENABLE" {
			continue
		}
		if d.Line == defaultLine && d.Type == "CNAME" && d.Value == r.Target {
			defaults++
		}
		if slices.Contains(r.PreferredLines, d.Line) && d.Type == "CNAME" {
			if d.Value == r.PreferredTarget {
				preferred++
			}
			if d.Value == r.Target {
				normal++
			}
		}
	}
	if defaults != 1 {
		return "unmanaged"
	}
	if preferred == len(r.PreferredLines) && normal == 0 {
		return "preferred"
	}
	if normal == len(r.PreferredLines) && preferred == 0 {
		return "fallback"
	}
	return "mixed"
}

// DNS resolution only supplies candidate addresses. Every dial is pinned after
// checking the live CF ranges; Host/SNI stays the visitor hostname, with normal
// TLS verification. No redirects, credentials, cookie jar or environment proxy.
func probeRouteTarget(ctx context.Context, r Route, target string, ranges []netip.Prefix) RouteHealth {
	h := RouteHealth{Checked: time.Now().UTC(), Colos: []string{}}
	lookupCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	ips, e := net.DefaultResolver.LookupNetIP(lookupCtx, "ip", target)
	cancel()
	if e != nil || len(ips) == 0 {
		h.Error = "目标域名解析失败"
		return h
	}
	ips = slices.Compact(ips)
	for _, ip := range ips {
		if !containsIP(ranges, ip) {
			h.Error = "目标解析含非 Cloudflare 官方地址，未发送探测"
			return h
		}
	}
	for _, ip := range ips[:min(len(ips), 3)] {
		if ctx.Err() != nil {
			break
		}
		h.Samples++
		client := pinnedClient(r.Hostname, ip, 5*time.Second)
		req := request(ctx, "GET", "https://"+r.Hostname+r.HealthPath, nil, "")
		req.Header.Set("Accept-Encoding", "identity")
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			client.CloseIdleConnections()
			h.Error = "HTTPS 连接或证书校验失败"
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		resp.Body.Close()
		client.CloseIdleConnections()
		ray := rayPattern.FindStringSubmatch(resp.Header.Get("CF-Ray"))
		if readErr != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 || len(ray) != 2 || r.ExpectedBody != "" && !strings.Contains(string(body), r.ExpectedBody) {
			h.Error = "响应未通过 HTTP 2xx / CF-Ray / 内容校验（最多读取 16 KiB）"
			continue
		}
		h.Success++
		h.LatencyMS += time.Since(start).Milliseconds()
		if !slices.Contains(h.Colos, strings.ToUpper(ray[1])) {
			h.Colos = append(h.Colos, strings.ToUpper(ray[1]))
		}
		if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
			expiration := resp.TLS.PeerCertificates[0].NotAfter
			if h.TLSExpires.IsZero() || expiration.Before(h.TLSExpires) {
				h.TLSExpires = expiration
			}
		}
	}
	h.Healthy = h.Success > 0
	if h.Healthy {
		h.LatencyMS /= int64(h.Success)
		if h.Success == h.Samples {
			h.Error = ""
		}
	}
	return h
}
