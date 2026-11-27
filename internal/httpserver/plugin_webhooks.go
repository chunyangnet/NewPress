package httpserver

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/pluginjobs"
)

type pluginWebhookView struct {
	Available, Enabled bool
	Plugin, Job        string
}

func (s *Server) SetPluginWebhookStore(store plugindispatch.Reviewer) { s.pluginWebhooks = store }

func (s *Server) adminPluginWebhooks(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Add("Vary", "Cookie")
	if readOnly || p.UserID < 1 {
		s.renderError(w, 403, "请使用持久管理员账号登录后核对 Webhook")
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"plugin": true, "job": true})
	if !ok {
		return
	}
	q, err := plugindispatch.NormalizeQuery(plugindispatch.Query{Plugin: values.Get("plugin"), JobID: values.Get("job")})
	if err != nil {
		s.renderError(w, 400, "插件或任务筛选参数无效")
		return
	}
	data := s.baseViewFor(w, "Webhook 结果核对", "核对外部发送事实并记录人员确认。", "/admin/plugin-webhooks/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-plugin-webhooks"
	data.Admin = s.adminView("plugins", p, false)
	data.Admin.DetailTitle = "Webhook 核对"
	data.PluginWebhooks = pluginWebhookView{Available: s.pluginWebhooks != nil, Enabled: s.config.PluginWebhooksEnabled, Plugin: q.Plugin, Job: q.JobID}
	s.render(w, 200, "admin-plugin-webhooks", data)
}

func (s *Server) pluginWebhookSession(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireSharedReviewIdentity(w, r)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, map[string]bool{}); !ok {
		return
	}
	if allowed, _ := s.accessRate.allow("plugin-webhook-session:"+strconv.FormatInt(p.UserID, 10), 120, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "plugins.rate", "查询过于频繁，请稍后再试", nil)
		return
	}
	writeJSON(w, 200, struct {
		UserID    string `json:"user_id"`
		CSRFToken string `json:"csrf_token"`
		Available bool   `json:"available"`
		Enabled   bool   `json:"enabled"`
	}{strconv.FormatInt(p.UserID, 10), p.CSRFToken, s.pluginWebhooks != nil, s.config.PluginWebhooksEnabled})
}

func (s *Server) pluginWebhookList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireSharedReviewIdentity(w, r)
	if !ok {
		return
	}
	if s.pluginWebhooks == nil {
		s.pluginWebhookError(w, r, plugindispatch.ErrUnavailable)
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"plugin": true, "job": true, "state": true, "limit": true, "offset": true})
	if !ok {
		return
	}
	q := plugindispatch.Query{Plugin: values.Get("plugin"), JobID: values.Get("job"), State: values.Get("state")}
	for key, target := range map[string]*int{"limit": &q.Limit, "offset": &q.Offset} {
		if value := values.Get(key); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || key == "limit" && n < 1 {
				s.pluginWebhookError(w, r, plugindispatch.ErrInvalid)
				return
			}
			*target = n
		}
	}
	q, err := plugindispatch.NormalizeQuery(q)
	if err != nil {
		s.pluginWebhookError(w, r, err)
		return
	}
	if allowed, _ := s.accessRate.allow("plugin-webhook-read:"+strconv.FormatInt(p.UserID, 10), 30, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "plugins.rate", "查询过于频繁，请稍后再试", nil)
		return
	}
	page, err := s.pluginWebhooks.List(r.Context(), q)
	if err != nil {
		s.pluginWebhookError(w, r, err)
		return
	}
	current, ok := s.recheckSharedReviewIdentity(w, r)
	if !ok {
		return
	}
	if current.UserID != p.UserID {
		s.writeAPIError(w, r, 409, "plugins.review_identity_changed", "登录账号已变化，请重新打开核对页面", nil)
		return
	}
	writeJSON(w, 200, page)
}

func (s *Server) pluginWebhookReview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var body struct {
		ExpectedUserID string `json:"expected_user_id"`
		Version        int64  `json:"version"`
		Decision       string `json:"decision"`
		Reason         string `json:"reason"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 4096, "4 KiB"); !ok {
		return
	}
	if p.UserID < 1 || body.ExpectedUserID != strconv.FormatInt(p.UserID, 10) {
		s.writeAPIError(w, r, 409, "plugins.review_identity_changed", "登录账号已变化，请重新打开核对页面", nil)
		return
	}
	if s.pluginWebhooks == nil {
		s.pluginWebhookError(w, r, plugindispatch.ErrUnavailable)
		return
	}
	if allowed, _ := s.accessRate.allow("plugin-webhook-review:"+strconv.FormatInt(p.UserID, 10), 30, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "plugins.rate", "核对操作过于频繁，请稍后再试", nil)
		return
	}
	_, err := s.pluginWebhooks.Review(r.Context(), plugindispatch.ReviewRequest{ID: r.PathValue("id"), Version: body.Version, ActorID: p.UserID, Decision: body.Decision, Reason: body.Reason})
	if err != nil {
		s.pluginWebhookError(w, r, err)
		return
	}
	current, ok := s.recheckSharedReviewIdentity(w, r)
	if !ok {
		return
	}
	if current.UserID != p.UserID {
		s.writeAPIError(w, r, 409, "plugins.review_identity_changed", "登录账号已变化，请重新打开核对页面", nil)
		return
	}
	writeJSON(w, 200, map[string]bool{"recorded": true})
}

func (s *Server) pluginWebhookError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 503, "plugins.webhooks_unavailable", "Webhook 记录暂不可用，请稍后重新查询"
	switch {
	case errors.Is(err, plugindispatch.ErrInvalid):
		status, code, message = 422, "plugins.webhooks_invalid", "Webhook 查询或核对参数无效"
	case errors.Is(err, plugindispatch.ErrConflict):
		status, code, message = 409, "plugins.webhooks_conflict", "记录已变化，请重新查询后核对"
	case errors.Is(err, pluginjobs.ErrAuthorization):
		status, code, message = 403, "plugins.webhooks_denied", "当前账号已无核对权限"
	case errors.Is(err, plugindispatch.ErrCorrupt):
		status, code, message = 503, "plugins.webhooks_corrupt", "Webhook 持久记录不一致，已阻止发送；请停机核对原数据"
	}
	s.writeAPIError(w, r, status, code, message, nil)
}
