package httpserver

import (
	"net/http"
	"strconv"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/pluginshared"
)

const sharedReviewUserHeader = "X-NewPress-Review-User"

type pluginSharedReviewView struct {
	Available bool
	Plugin    string
	Job       string
}

func (s *Server) adminPluginSharedReview(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Add("Vary", "Cookie")
	if readOnly || p.UserID < 1 {
		s.renderError(w, http.StatusForbidden, "请使用持久管理员账号登录后核对插件记录")
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"plugin": true, "job": true})
	if !ok {
		return
	}
	q, err := pluginshared.NormalizeQuery(pluginshared.Query{Plugin: values.Get("plugin"), JobID: values.Get("job")})
	if err != nil {
		s.renderError(w, http.StatusBadRequest, "插件或任务筛选参数无效")
		return
	}
	_, available := s.pluginTasks.(pluginshared.Reader)
	data := s.baseViewFor(w, "插件结果核对", "核对共享授权、原内容操作结果与隔离修复记录。", "/admin/plugin-records/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-plugin-records"
	data.Admin = s.adminView("plugins", p, false)
	data.Admin.DetailTitle = "结果核对"
	data.PluginSharedReview = pluginSharedReviewView{Available: available, Plugin: q.Plugin, Job: q.JobID}
	s.render(w, http.StatusOK, "admin-plugin-records", data)
}

// The optional identity header binds a read to the account that opened the
// page. Existing API clients can still read without the page-specific header.
func (s *Server) requireSharedReviewIdentity(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, readOnly, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return auth.Principal{}, false
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if readOnly || p.UserID < 1 {
		s.writeAPIError(w, r, http.StatusForbidden, "plugins.authentication", "请使用持久管理员账号登录后查看维护记录", nil)
		return auth.Principal{}, false
	}
	if expected := r.Header.Values(sharedReviewUserHeader); len(expected) > 0 && (len(expected) != 1 || expected[0] != strconv.FormatInt(p.UserID, 10)) {
		s.writeAPIError(w, r, http.StatusConflict, "plugins.review_identity_changed", "登录账号已变化，请重新打开核对页面", nil)
		return auth.Principal{}, false
	}
	w.Header().Set(sharedReviewUserHeader, strconv.FormatInt(p.UserID, 10))
	return p, true
}

// Recheck after reading sensitive records: ordinary request-local identity reuse
// must not turn a post-read authorization barrier into a stale permission check.
func (s *Server) recheckSharedReviewIdentity(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	current, ok := s.freshSessionRequest(w, r)
	if !ok {
		return auth.Principal{}, false
	}
	return s.requireSharedReviewIdentity(w, current)
}

// This lightweight read lets the visible review page recheck permissions
// without repeatedly scanning the retained record collections.
func (s *Server) pluginSharedReviewSession(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireSharedReviewIdentity(w, r)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, map[string]bool{}); !ok {
		return
	}
	// A visible page performs at most 40 scheduled checks per ten-minute
	// window, plus one confirmation for each permitted record query.
	if allowed, _ := s.accessRate.allow("plugin-shared-review-session:"+strconv.FormatInt(p.UserID, 10), 120, time.Now()); !allowed {
		s.writeAPIError(w, r, http.StatusTooManyRequests, "plugins.rate", "查询过于频繁，请稍后再试", nil)
		return
	}
	_, available := s.pluginTasks.(pluginshared.Reader)
	writeJSON(w, http.StatusOK, struct {
		UserID    string `json:"user_id"`
		Available bool   `json:"available"`
	}{strconv.FormatInt(p.UserID, 10), available})
}
