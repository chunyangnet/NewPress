package httpserver

import (
	"net/http"
	"strconv"

	"newpress.local/newpress/internal/coreui"
	"newpress.local/newpress/internal/presentation"
)

func statusPresentation(code int, message string) presentation.StatusPage {
	v := presentation.StatusPage{Code: code, Kind: "error", Title: "页面暂时无法打开", Message: message, Icon: "info"}
	switch code {
	case 400:
		v.Title = "请求信息不正确"
	case 401:
		v.Title, v.Icon = "请先登录", "user-round"
	case 403:
		v.Title, v.Icon = "没有访问权限", "lock-keyhole"
	case 404:
		v.Title, v.Icon = "页面没有找到", "search"
	case 405:
		v.Title = "不支持此操作"
	case 409:
		v.Title = "内容状态已发生变化"
	case 410:
		v.Title = "内容已移除"
	case 422:
		v.Title = "请检查提交的信息"
	case 429:
		v.Title, v.Icon = "请求过于频繁", "clock-3"
	case 500:
		v.Title, v.Icon = "页面暂时出现问题", "refresh-cw"
	case 502, 503, 504:
		v.Title, v.Icon = "服务暂时不可用", "refresh-cw"
	}
	if code == http.StatusNotFound && message == v.Title {
		v.Message = "链接可能已失效，或内容已被移走。"
	}
	if v.Message == "" {
		v.Message = "请稍后再试，或返回首页继续浏览。"
	}
	return v
}

// Service states are supplied explicitly by the owning handler, never inferred
// from an error string. The caller retains the existing HTTP/API semantics.
func (s *Server) renderServiceStatus(w http.ResponseWriter, code int, service, kind string) {
	message := "此服务暂时无法使用，请稍后再来。"
	if kind == "disabled" {
		message = "站点尚未开放这项服务。你可以先浏览其他内容。"
	}
	s.renderStatus(w, code, message, service, kind)
}

func (s *Server) renderStatus(w http.ResponseWriter, code int, message, service, kind string) {
	v := statusPresentation(code, message)
	page := strconv.Itoa(code)
	if service != "" {
		page, v.Kind, v.Service, v.Icon = "service-unavailable", kind, service, "pause"
		v.Title = service + "服务暂时不可用"
		if kind == "disabled" {
			v.Title = service + "服务未启用"
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	admin := false
	if state, ok := responseContext(w).Value(siteRequestKey{}).(siteRequestState); ok {
		admin = adminRequestPath(state.path)
	}
	if !s.themes.HasTemplate(page) {
		page = "error"
	}
	if admin || (!s.themes.HasTemplate(page) && code >= 500) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		_ = coreui.Error(w, v.Title, message)
		return
	}
	if !s.themes.HasTemplate(page) {
		page = "404"
	}
	data := s.baseViewFor(w, v.Title, message, "")
	data.Status, data.BodyClass = v, "is-error"
	data.Site.Robots = "noindex, nofollow, noarchive"
	s.render(w, code, page, data)
}
