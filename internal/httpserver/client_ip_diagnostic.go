package httpserver

import (
	"fmt"
	"net/http"
	"strings"
)

type clientIPDiagnostic struct {
	Method, Detail, Address, Trust string
	Reliable                       bool
}

func clientIPSourceLabel(source string) string {
	labels := map[string]string{"tcp": "直接连接", "xff": "可信代理转发", "proxy": "代理来源未确认", "cloudflare": "Cloudflare", "cloudflare-ipv6": "Cloudflare · 已还原 IPv6", "esa": "阿里云 ESA", "edgeone": "腾讯云 EdgeOne"}
	if label := labels[source]; label != "" {
		return label
	}
	return "来源方式未记录"
}

func clientIPIssueLabel(issue string) string {
	labels := map[string]string{
		"invalid_peer":            "无法读取连接地址。",
		"missing_forwarded_chain": "可信代理没有提供 X-Forwarded-For，请检查代理覆盖配置。",
		"invalid_forwarded_chain": "X-Forwarded-For 格式异常或超出长度限制，未采用。",
		"all_hops_trusted":        "代理链中没有可确认的访客地址，请收窄可信网段并检查转发方式。",
		"missing_cdn_header":      "已验证 CDN 回源网段，但缺少该服务的真实 IP 标头。ESA 需开启添加 Ali-Real-Client-IP。",
		"invalid_cdn_header":      "CDN 真实 IP 标头格式异常或包含多个地址，未采用。",
		"invalid_pseudo_ipv6":     "Cloudflare 伪 IPv4 缺少有效的 CF-Connecting-IPv6，未采用。",
		"nonpublic_cdn_client":    "CDN 返回了非公网地址，未作为访客来源。",
		"worker_subrequest":       "Cloudflare Worker 子请求无法在此确认原始访客地址。",
		"upstream_proxy":          "CDN 返回的是另一可信代理地址，需要在外层代理正确还原访客来源。",
		"ambiguous_cdn":           "回源网段对应多个服务，无法确定可信标头。",
		"origin_auth_failed":      "CDN 回源校验未通过，请核对由 CDN 覆盖设置的回源令牌。",
	}
	if label := labels[issue]; label != "" {
		return label
	}
	if issue != "" {
		return "来源未通过验证。"
	}
	return ""
}

func (s *Server) clientIPDiagnostic(r *http.Request, showAddress bool) clientIPDiagnostic {
	result := s.clientIPs.resolve(r)
	view := clientIPDiagnostic{Method: clientIPSourceLabel(result.Source), Reliable: result.Reliable, Detail: clientIPIssueLabel(result.Issue)}
	if result.Reliable {
		view.Detail = "当前请求通过已配置的来源规则；这只核对当前访问路径。"
		if result.Source == "tcp" {
			view.Detail = "当前使用直接连接地址。经过 CDN 时，请先配置对应的回源网段；仅凭标头不会识别 CDN。"
		}
		if showAddress {
			view.Address = result.Address.String()
		}
	}
	parts := []string{fmt.Sprintf("本地代理 %d 段", len(s.config.TrustedProxyCIDRs))}
	for _, proxy := range s.config.CDNProxies {
		detail := fmt.Sprintf("%s %d 段", clientIPSourceLabel(proxy.Provider), len(proxy.CIDRs))
		if proxy.OriginToken != "" {
			detail += "（含回源校验）"
		}
		parts = append(parts, detail)
	}
	view.Trust = strings.Join(parts, " · ")
	return view
}
