package plugin

const CDN = "cdn"

func CDNManifest() Manifest {
	return Manifest{Schema: 1, Slug: CDN, Name: "CDN 加速", Version: "1.1.1", Description: "六类 CDN 缓存管理与测速，新增 Cloudflare SaaS 接入、阿里云/DNSPod 地区分流、健康回退和可预览的 DNS 回滚。", Category: "integration", Tags: []string{"CDN", "缓存", "Cloudflare", "节点优选"}, Author: "NewPress 官方", License: "Apache-2.0", NewPress: "builtin/cdn-v2", Runtime: "builtin/cdn-v2", Entry: "builtin.json", Capabilities: []string{"settings.read", "settings.write", "http.outbound", "cache", "jobs.enqueue"}}
}
