package httpserver

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/cdn"
	"newpress.local/newpress/internal/plugin"
)

type cdnAdminView struct {
	StateJSON string
	Groups    []plugin.SettingsGroup
}

func (s *Server) SetCDN(service *cdn.Service) { s.cdn = service }
func (s *Server) registerCDNRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/plugins/cdn/{$}", s.adminCDN)
	mux.HandleFunc("GET /api/v1/admin/plugins/cdn/status", s.cdnStatus)
	mux.HandleFunc("PUT /api/v1/admin/plugins/cdn/settings", s.cdnSave)
	mux.HandleFunc("POST /api/v1/admin/plugins/cdn/action", s.cdnAction)
	mux.HandleFunc("GET /api/v1/admin/plugins/cdn/export", s.cdnExport)
	mux.HandleFunc("GET /api/v1/admin/plugins/cdn/routing", s.cdnRoutingStatus)
	mux.HandleFunc("PUT /api/v1/admin/plugins/cdn/routing/settings", s.cdnRoutingSave)
	mux.HandleFunc("POST /api/v1/admin/plugins/cdn/routing/action", s.cdnRoutingAction)
}
func (s *Server) cdnError(w http.ResponseWriter, r *http.Request, e error) {
	status, code, message := http.StatusBadRequest, "cdn.invalid", e.Error()
	if errors.Is(e, cdn.ErrConflict) {
		status, code = http.StatusConflict, "cdn.conflict"
	}
	if errors.Is(e, plugin.ErrInactive) {
		status, code, message = http.StatusConflict, "cdn.inactive", "CDN 插件未安装、未启用或状态已变化"
	}
	s.writeAPIError(w, r, status, code, message, nil)
}
func (s *Server) adminCDN(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if _, ok = s.strictAPIQuery(w, r, map[string]bool{}); !ok {
		return
	}
	state := cdn.Status{Settings: cdn.SettingsView{Settings: cdn.Defaults()}, Counts: map[string]int{}, Jobs: []cdn.Job{}, Capabilities: cdn.Capabilities()}
	if !readOnly {
		if s.cdn == nil {
			s.renderError(w, 503, "CDN 服务暂不可用")
			return
		}
		var e error
		state, e = s.cdn.Status()
		if e != nil {
			s.renderError(w, 404, "请先安装 CDN 加速插件")
			return
		}
	}
	b, _ := json.Marshal(state)
	page, _ := plugin.SettingsPageFor(plugin.CDNManifest())
	data := s.baseViewFor(w, "CDN 加速", "管理缓存、诊断线路和 Cloudflare IP 优选。", "/admin/plugins/cdn/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-cdn"
	data.Admin = s.adminView("plugins", p, readOnly)
	data.Admin.DetailTitle = "CDN 加速"
	data.Admin.PluginSlug = plugin.CDN
	data.CDN = cdnAdminView{string(b), page.Groups}
	s.render(w, 200, "admin-cdn", data)
}
func (s *Server) cdnRead(w http.ResponseWriter, r *http.Request) (cdn.Status, bool) {
	_, readOnly, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return cdn.Status{}, false
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if readOnly {
		s.writeAPIError(w, r, 403, "cdn.authentication", "请登录后查看 CDN 连接与任务", nil)
		return cdn.Status{}, false
	}
	if s.cdn == nil {
		s.writeAPIError(w, r, 503, "cdn.unavailable", "CDN 服务暂不可用", nil)
		return cdn.Status{}, false
	}
	state, e := s.cdn.Status()
	if e != nil {
		s.cdnError(w, r, e)
		return state, false
	}
	return state, true
}
func (s *Server) cdnStatus(w http.ResponseWriter, r *http.Request) {
	state, ok := s.cdnRead(w, r)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, map[string]bool{}); ok {
		writeJSON(w, 200, state)
	}
}
func (s *Server) cdnSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var body struct {
		Settings       cdn.Settings `json:"settings"`
		ClearTokens    []string     `json:"clear_tokens"`
		ExpectedUserID int64        `json:"expected_user_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 128<<10, "128 KiB"); !ok || !s.pluginIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	if s.cdn == nil {
		s.writeAPIError(w, r, 503, "cdn.unavailable", "CDN 服务暂不可用", nil)
		return
	}
	v, e := s.cdn.Save(body.Settings, body.ClearTokens)
	if e != nil {
		s.cdnError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "settings": v})
}
func (s *Server) cdnAction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var body struct {
		cdn.EnqueueRequest
		Action         string `json:"action"`
		JobID          string `json:"job_id"`
		ExpectedUserID int64  `json:"expected_user_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 256<<10, "256 KiB"); !ok || !s.pluginIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	if s.cdn == nil {
		s.writeAPIError(w, r, 503, "cdn.unavailable", "CDN 服务暂不可用", nil)
		return
	}
	if allowed, _ := s.accessRate.allow("cdn-action:"+strconv.FormatInt(p.UserID, 10), 20, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "cdn.rate", "操作过于频繁，请稍后重试", nil)
		return
	}
	if body.Action == "cancel" || body.Action == "retry" {
		if e := s.cdn.JobAction(body.JobID, body.Action, body.ExpectedVersion); e != nil {
			s.cdnError(w, r, e)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if body.Action != "enqueue" {
		s.cdnError(w, r, errors.New("不支持该任务操作"))
		return
	}
	j, e := s.cdn.Enqueue(body.EnqueueRequest, p.Username, "manual")
	if e != nil {
		s.cdnError(w, r, e)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "job": j})
}
func (s *Server) cdnExport(w http.ResponseWriter, r *http.Request) {
	state, ok := s.cdnRead(w, r)
	if !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"kind": true, "job": true, "format": true})
	if !ok {
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="newpress-cdn-export.json"`)
	switch q.Get("kind") {
	case "routing", "routing-plans":
		routing, e := s.cdn.RoutingStatus()
		if e != nil {
			s.cdnError(w, r, e)
			return
		}
		if q.Get("kind") == "routing" {
			writeJSON(w, 200, routing.Config)
		} else {
			writeJSON(w, 200, routing.Plans)
		}
	case "settings":
		writeJSON(w, 200, state.Settings.Settings)
	case "jobs":
		writeJSON(w, 200, state.Jobs)
	case "rules":
		w.Header().Set("Content-Disposition", `attachment; filename="newpress-cdn-policy.txt"`)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "NewPress CDN 缓存策略核对单（需在服务商控制台按实际规则配置）\n\n静态资源域名：%s\n静态缓存建议：%d 秒\n仅公开静态资源 /theme-assets/* 可设置边缘 TTL。\n尊重源站 Cache-Control: private / no-store / no-cache。\n绕过 /admin*、/api*、/account*、/preview*、/login*、/logout*、/billing*、/payment*、/oauth*。\n绕过 Authorization、所有 Cookie、Set-Cookie、非 GET/HEAD 请求。\n不要对文章、会员、私密媒体或支付响应强制 Cache Everything。\n保留查询参数，尤其资源版本 v；不要把签名参数从缓存键中移除。\n此文件是可审核的建议，不会自动修改云端规则。\n", state.Settings.AssetBase, state.Settings.StaticTTL)
	case "benchmark":
		var result *cdn.Benchmark
		for _, j := range state.Jobs {
			if j.ID == q.Get("job") {
				result = j.Benchmark
				break
			}
		}
		if result == nil {
			s.writeAPIError(w, r, 404, "cdn.result", "测速结果不存在或尚未完成", nil)
			return
		}
		switch q.Get("format") {
		case "json", "":
			writeJSON(w, 200, result)
		case "csv":
			w.Header().Set("Content-Disposition", `attachment; filename="newpress-cloudflare-nodes.csv"`)
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			writer := csv.NewWriter(w)
			_ = writer.Write([]string{"IP", "Family", "Colo", "SuccessPercent", "TCPms", "TLSms", "TTFBp50ms", "TTFBp95ms", "Mbps", "Bytes", "Eligible"})
			for _, n := range result.Results {
				_ = writer.Write([]string{n.IP, n.Family, n.Colo, fmt.Sprintf("%.1f", n.SuccessRate), fmt.Sprintf("%.2f", n.TCPMS), fmt.Sprintf("%.2f", n.TLSMS), fmt.Sprintf("%.2f", n.TTFBP50), fmt.Sprintf("%.2f", n.TTFBP95), fmt.Sprintf("%.3f", n.Mbps), strconv.FormatInt(n.Bytes, 10), strconv.FormatBool(n.Eligible)})
			}
			writer.Flush()
		case "hosts":
			w.Header().Set("Content-Disposition", `attachment; filename="newpress-cloudflare-hosts.txt"`)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			u, _ := url.Parse(result.URL)
			fmt.Fprintln(w, "# 仅供手动测试的 hosts 片段，不自动修改系统或 DNS。")
			fmt.Fprintln(w, "# 来自本站服务器出口；Anycast 路由会变化。不要用这些 IP 替换橙云记录的源站地址。")
			for _, n := range result.Results {
				if n.Eligible {
					fmt.Fprintf(w, "%s %s\n", n.IP, u.Hostname())
					return
				}
			}
			fmt.Fprintln(w, "# 本次没有达到成功率阈值的地址。")
		default:
			s.cdnError(w, r, errors.New("不支持此导出格式"))
		}
	default:
		s.cdnError(w, r, errors.New("不支持此导出类型"))
	}
}
func (s *Server) prepareCDNDelivery(w http.ResponseWriter, data *pageView) {
	if data.IsAdmin || data.IsPreview {
		return
	}
	base, _ := s.cdn.Delivery()
	if base == "" {
		return
	}
	data.CDNAssetBase = base
	parts := strings.Split(w.Header().Get("Content-Security-Policy"), ";")
	for _, directive := range []string{"script-src", "style-src", "img-src", "font-src"} {
		found := false
		for i, part := range parts {
			f := strings.Fields(part)
			if len(f) > 0 && f[0] == directive {
				parts[i] = strings.TrimSpace(part) + " " + base
				found = true
			}
		}
		if !found {
			parts = append(parts, directive+" 'self' "+base)
		}
	}
	w.Header().Set("Content-Security-Policy", strings.Join(parts, ";"))
}
