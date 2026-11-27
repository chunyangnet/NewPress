package cdn

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/safehttp"
)

type BenchmarkRequest struct {
	URL            string   `json:"url"`
	Candidates     []string `json:"candidates"`
	SampleCount    int      `json:"sample_count"`
	IPv6           bool     `json:"ipv6"`
	Rounds         int      `json:"rounds"`
	Concurrency    int      `json:"concurrency"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	DownloadBytes  int64    `json:"download_bytes"`
	MinimumSuccess int      `json:"minimum_success"`
}
type NodeResult struct {
	IP          string  `json:"ip"`
	Family      string  `json:"family"`
	Colo        string  `json:"colo"`
	Success     int     `json:"success"`
	Rounds      int     `json:"rounds"`
	SuccessRate float64 `json:"success_rate"`
	TCPMS       float64 `json:"tcp_ms"`
	TLSMS       float64 `json:"tls_ms"`
	TTFBP50     float64 `json:"ttfb_p50"`
	TTFBP95     float64 `json:"ttfb_p95"`
	Mbps        float64 `json:"mbps"`
	Bytes       int64   `json:"bytes"`
	StatusCode  int     `json:"status_code"`
	Eligible    bool    `json:"eligible"`
	Error       string  `json:"error,omitempty"`
}
type Benchmark struct {
	Started       time.Time    `json:"started"`
	Finished      time.Time    `json:"finished"`
	URL           string       `json:"url"`
	Scope         string       `json:"scope"`
	IPListETag    string       `json:"ip_list_etag"`
	IPListFetched time.Time    `json:"ip_list_fetched"`
	Bytes         int64        `json:"bytes"`
	Results       []NodeResult `json:"results"`
}
type Diagnostic struct {
	URL        string            `json:"url"`
	StatusCode int               `json:"status_code"`
	DurationMS int64             `json:"duration_ms"`
	TLSExpires time.Time         `json:"tls_expires"`
	Headers    map[string]string `json:"headers"`
	Findings   []string          `json:"findings"`
}

func validateBenchmark(p Profile, b BenchmarkRequest) error {
	u, e := targetURL(p, b.URL)
	if e != nil {
		return e
	}
	if u.RawQuery != "" || !publicPath(u.Path) {
		return errors.New("测速 URL 必须是此连接下无查询参数的公开 HTTPS 资源")
	}
	if len(b.Candidates) > 64 || b.SampleCount < 1 || b.SampleCount > 64 || b.Rounds < 1 || b.Rounds > 5 || b.Concurrency < 1 || b.Concurrency > 4 || b.TimeoutSeconds < 2 || b.TimeoutSeconds > 15 || b.DownloadBytes < 0 || b.DownloadBytes > 8<<20 || b.MinimumSuccess < 50 || b.MinimumSuccess > 100 {
		return errors.New("优选范围：候选最多 64、轮数 1–5、并发 1–4、超时 2–15 秒、下载 0–8 MiB / IP、最低成功率 50–100%")
	}
	for _, candidate := range b.Candidates {
		if ip, err := netip.ParseAddr(strings.TrimSpace(candidate)); err != nil || !safehttp.PublicIP(ip) || ip.Is4In6() {
			return errors.New("候选列表每行一个 IPv4 / IPv6 地址；不接受 CIDR 或任意地址段扫描")
		}
	}
	return nil
}
func fetchCFRanges(ctx context.Context, client *http.Client) ([]netip.Prefix, string, error) {
	r := request(ctx, http.MethodGet, "https://api.cloudflare.com/client/v4/ips", nil, "")
	resp, e := client.Do(r)
	if e != nil {
		return nil, "", &OperationError{"无法获取 Cloudflare 官方 IP 列表，本次测速已停止", true, 0}
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if e != nil || resp.StatusCode != 200 || len(b) > 65536 {
		return nil, "", errors.New("Cloudflare IP list unavailable")
	}
	var out struct {
		Success bool `json:"success"`
		Result  struct {
			V4   []string `json:"ipv4_cidrs"`
			V6   []string `json:"ipv6_cidrs"`
			ETag string   `json:"etag"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &out) != nil || !out.Success || len(out.Result.V4)+len(out.Result.V6) > 128 || len(out.Result.V4) == 0 {
		return nil, "", errors.New("Cloudflare IP list invalid")
	}
	result := []netip.Prefix{}
	for _, raw := range append(out.Result.V4, out.Result.V6...) {
		p, e := netip.ParsePrefix(raw)
		if e != nil || !safehttp.PublicPrefix(p) {
			return nil, "", errors.New("Cloudflare IP prefix invalid")
		}
		result = append(result, p.Masked())
	}
	return result, out.Result.ETag, nil
}
func containsIP(ranges []netip.Prefix, ip netip.Addr) bool {
	if !safehttp.PublicIP(ip) || ip.Is4In6() {
		return false
	}
	for _, p := range ranges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
func candidates(b BenchmarkRequest, ranges []netip.Prefix) ([]netip.Addr, error) {
	list := []netip.Addr{}
	if len(b.Candidates) > 0 {
		for _, raw := range b.Candidates {
			a, e := netip.ParseAddr(strings.TrimSpace(raw))
			if e != nil || !containsIP(ranges, a) {
				return nil, errors.New("候选地址不在实时获取的 Cloudflare 官方网段内")
			}
			if !slices.Contains(list, a) {
				list = append(list, a)
			}
		}
		return list, nil
	}
	pool := []netip.Prefix{}
	for _, p := range ranges {
		if p.Addr().Is6() == b.IPv6 {
			pool = append(pool, p)
		}
	}
	if len(pool) == 0 {
		return nil, errors.New("所选地址族没有可用网段")
	}
	for attempts := 0; len(list) < b.SampleCount && attempts < 512; attempts++ {
		p := pool[attempts%len(pool)]
		bytes := p.Addr().AsSlice()
		random := make([]byte, len(bytes))
		if _, e := rand.Read(random); e != nil {
			return nil, e
		}
		for bit := p.Bits(); bit < len(bytes)*8; bit++ {
			mask := byte(1 << (7 - bit%8))
			bytes[bit/8] = (bytes[bit/8] &^ mask) | (random[bit/8] & mask)
		}
		a, ok := netip.AddrFromSlice(bytes)
		if ok && containsIP(ranges, a) && !slices.Contains(list, a) {
			list = append(list, a)
		}
	}
	return list, nil
}

type probeSample struct {
	TCP, TLS, TTFB float64
	Colo           string
	Code           int
	Bytes          int64
	Mbps           float64
}

var rayPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8,32}-([a-z]{3})$`)

func pinnedClient(host string, ip netip.Addr, timeout time.Duration) *http.Client {
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: true, TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		h, p, e := net.SplitHostPort(address)
		if e != nil || !strings.EqualFold(h, host) || p != "443" {
			return nil, errors.New("probe destination changed")
		}
		return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), "443"))
	}
	return &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func probe(ctx context.Context, raw string, ip netip.Addr, timeout time.Duration, budget int64) (probeSample, error) {
	u, _ := url.Parse(raw)
	client := pinnedClient(u.Hostname(), ip, timeout)
	defer client.CloseIdleConnections()
	var sample probeSample
	start := time.Now()
	var tcpStart, tlsStart, bodyStart time.Time
	trace := &httptrace.ClientTrace{ConnectStart: func(string, string) { tcpStart = time.Now() }, ConnectDone: func(string, string, error) { sample.TCP = float64(time.Since(tcpStart).Microseconds()) / 1000 }, TLSHandshakeStart: func() { tlsStart = time.Now() }, TLSHandshakeDone: func(tls.ConnectionState, error) { sample.TLS = float64(time.Since(tlsStart).Microseconds()) / 1000 }, GotFirstResponseByte: func() { sample.TTFB = float64(time.Since(start).Microseconds()) / 1000; bodyStart = time.Now() }}
	r := request(httptrace.WithClientTrace(ctx, trace), http.MethodGet, raw, nil, "")
	r.Header.Set("Accept-Encoding", "identity")
	r.Header.Set("Range", fmt.Sprintf("bytes=0-%d", budget-1))
	resp, e := client.Do(r)
	if e != nil {
		return sample, &OperationError{"连接 / TLS 校验 / 响应超时", false, 0}
	}
	defer resp.Body.Close()
	sample.Code = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return sample, &OperationError{fmt.Sprintf("HTTP %d（不跟随跳转）", resp.StatusCode), false, 0}
	}
	ray := rayPattern.FindStringSubmatch(resp.Header.Get("CF-Ray"))
	if len(ray) != 2 {
		return sample, &OperationError{"响应缺少有效 CF-Ray，不能确认 Cloudflare 接入", false, 0}
	}
	sample.Colo = strings.ToUpper(ray[1])
	n, e := io.Copy(io.Discard, io.LimitReader(resp.Body, budget))
	sample.Bytes = n
	if e != nil {
		return sample, &OperationError{"响应内容读取失败或超时", false, 0}
	}
	if budget >= 64<<10 && n >= 64<<10 && time.Since(bodyStart) > 0 {
		sample.Mbps = float64(n) * 8 / time.Since(bodyStart).Seconds() / 1e6
	}
	return sample, nil
}
func percentile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	v = slices.Clone(v)
	sort.Float64s(v)
	return v[max(0, min(len(v)-1, int(math.Ceil(q*float64(len(v))))-1))]
}
func rank(results []NodeResult, minimum int) {
	for i := range results {
		n := &results[i]
		n.SuccessRate = float64(n.Success) * 100 / float64(max(n.Rounds, 1))
		n.Eligible = n.Success > 0 && n.SuccessRate >= float64(minimum)
	}
	slices.SortStableFunc(results, func(a, b NodeResult) int {
		if a.Eligible != b.Eligible {
			if a.Eligible {
				return -1
			}
			return 1
		}
		if a.SuccessRate != b.SuccessRate {
			if a.SuccessRate > b.SuccessRate {
				return -1
			}
			return 1
		}
		if a.TTFBP95 != b.TTFBP95 {
			if a.TTFBP95 < b.TTFBP95 {
				return -1
			}
			return 1
		}
		if a.Mbps != b.Mbps {
			if a.Mbps > b.Mbps {
				return -1
			}
			return 1
		}
		return strings.Compare(a.IP, b.IP)
	})
}
func runBenchmark(ctx context.Context, p Profile, b BenchmarkRequest) (Benchmark, error) {
	result := Benchmark{Started: time.Now().UTC(), URL: b.URL, Scope: "本站服务器当前出口网络 → Cloudflare Anycast 地址；不代表访客线路，也不能固定全球机房", Results: []NodeResult{}}
	if e := validateBenchmark(p, b); e != nil {
		return result, e
	}
	client := safehttp.Client(15 * time.Second)
	defer client.CloseIdleConnections()
	ranges, etag, e := fetchCFRanges(ctx, client)
	if e != nil {
		return result, e
	}
	result.IPListETag = etag
	result.IPListFetched = time.Now().UTC()
	ips, e := candidates(b, ranges)
	if e != nil {
		return result, &OperationError{e.Error(), false, 0}
	}
	results := make([]NodeResult, len(ips))
	next := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < b.Concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				ip := ips[i]
				n := NodeResult{IP: ip.String(), Family: "IPv4", Rounds: b.Rounds}
				if ip.Is6() {
					n.Family = "IPv6"
				}
				var tcp, tlsTimes, ttfb []float64
				colos := []string{}
				for round := 0; round < b.Rounds && ctx.Err() == nil; round++ {
					s, e := probe(ctx, b.URL, ip, time.Duration(b.TimeoutSeconds)*time.Second, 8192)
					n.Bytes += s.Bytes
					n.StatusCode = s.Code
					if e != nil {
						n.Error = PublicError(e)
						continue
					}
					n.Success++
					tcp = append(tcp, s.TCP)
					tlsTimes = append(tlsTimes, s.TLS)
					ttfb = append(ttfb, s.TTFB)
					if !slices.Contains(colos, s.Colo) {
						colos = append(colos, s.Colo)
					}
				}
				n.TCPMS = percentile(tcp, .5)
				n.TLSMS = percentile(tlsTimes, .5)
				n.TTFBP50 = percentile(ttfb, .5)
				n.TTFBP95 = percentile(ttfb, .95)
				n.Colo = strings.Join(colos, ",")
				if n.Success > 0 && b.DownloadBytes > 0 && ctx.Err() == nil {
					s, e := probe(ctx, b.URL, ip, time.Duration(b.TimeoutSeconds)*time.Second, b.DownloadBytes)
					n.Bytes += s.Bytes
					if e == nil {
						n.Mbps = s.Mbps
						if n.Mbps == 0 {
							n.Error = "测速资源不足 64 KiB，吞吐量不可用"
						}
					} else {
						n.Error = "下载测速失败：" + PublicError(e)
					}
				}
				results[i] = n
			}
		}()
	}
	for i := range ips {
		if ctx.Err() != nil {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	rank(results, b.MinimumSuccess)
	result.Results = results
	for _, n := range results {
		result.Bytes += n.Bytes
	}
	result.Finished = time.Now().UTC()
	return result, nil
}
func diagnose(ctx context.Context, client *http.Client, p Profile, raw string) (Diagnostic, error) {
	result := Diagnostic{URL: raw, Headers: map[string]string{}, Findings: []string{}}
	if _, e := normalizeTargets(p, "diagnose", []string{raw}); e != nil {
		return result, e
	}
	start := time.Now()
	r := request(ctx, http.MethodGet, raw, nil, "")
	r.Header.Set("Range", "bytes=0-8191")
	resp, e := client.Do(r)
	if e != nil {
		return result, e
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
	result.DurationMS = time.Since(start).Milliseconds()
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		result.TLSExpires = resp.TLS.PeerCertificates[0].NotAfter
		if time.Until(result.TLSExpires) < 14*24*time.Hour {
			result.Findings = append(result.Findings, "证书将在 14 天内到期")
		}
	}
	for _, key := range []string{"Cache-Control", "CDN-Cache-Control", "Age", "ETag", "Vary", "Server", "CF-Cache-Status", "CF-Ray", "X-Cache", "X-Cache-Hits", "Via", "Content-Type", "Content-Length"} {
		if v := resp.Header.Get(key); v != "" {
			if len(v) > 512 {
				v = v[:512]
			}
			result.Headers[key] = v
		}
	}
	if len(resp.Header.Values("Set-Cookie")) > 0 {
		result.Findings = append(result.Findings, "响应设置 Cookie，不应强制缓存共享 HTML")
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		result.Findings = append(result.Findings, "目标返回重定向；诊断未访问跳转后的地址")
	}
	if strings.Contains(resp.Header.Get("Cache-Control"), "no-store") || strings.Contains(resp.Header.Get("Cache-Control"), "private") {
		result.Findings = append(result.Findings, "源站要求禁止共享缓存，请保留此策略")
	}
	result.Findings = append(result.Findings, "此结果来自本站服务器的一次有限 GET，请结合 CDN 控制台及其他网络重复验证")
	return result, nil
}
