package httpserver

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"newpress.local/newpress/internal/community"
)

// Circle owners and moderators can use this workspace without acquiring any
// site-wide administrator capabilities. Every command still checks its circle.
func (s *Server) circlesAdminPage(w http.ResponseWriter, r *http.Request) {
	if (!s.config.CommunityEnabled || s.circles == nil) && s.renderModuleRecovery(w, r, "community") {
		return
	}
	if !s.circleEnabled(w, r) {
		return
	}
	if !s.moduleEnabled("community") {
		s.renderError(w, http.StatusNotFound, "圈子功能已暂停")
		return
	}
	principal, signedIn := s.currentAdmin(r)
	readOnly := false
	if !signedIn {
		if s.auth.Configured() {
			http.Redirect(w, r, "/admin/login/?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		var ok bool
		principal, readOnly, ok = s.requireAdminPage(w, r)
		if !ok {
			return
		}
	}
	v, err := s.circleProjection(r)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, community.ErrNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, community.ErrInvalid) {
			status = http.StatusBadRequest
		}
		s.renderError(w, status, "圈子工作台暂时不可用")
		return
	}
	if v.Directory {
		if !readOnly && !v.CanManage {
			s.renderError(w, http.StatusForbidden, "当前账号没有可管理的圈子")
			return
		}
		items := v.Items[:0]
		for _, item := range v.Items {
			if item.Manage {
				items = append(items, item)
			}
		}
		v.Items, v.Total = items, len(items)
	} else if !v.Circle.Manage {
		s.renderError(w, http.StatusForbidden, "只有圈主、版主或站点管理员可以管理此圈子")
		return
	}

	// All pagination, filters and command completion stay inside the workspace.
	publicBase := v.Circle.URL
	adminBase := v.Circle.ManageURL
	workspaceURL := func(path string) string {
		if publicBase != "" && strings.HasPrefix(path, publicBase) {
			return adminBase + strings.TrimPrefix(path, publicBase)
		}
		return path
	}
	for _, path := range []*string{&v.Previous, &v.Next, &v.MemberPrevious, &v.MemberNext, &v.ReportPrevious, &v.ReportNext, &v.HistoryPrevious, &v.HistoryNext, &v.Topic.URL} {
		*path = workspaceURL(*path)
	}
	for i := range v.Topics {
		v.Topics[i].URL = workspaceURL(v.Topics[i].URL)
	}
	for i := range v.Reports {
		v.Reports[i].URL = workspaceURL(v.Reports[i].URL)
	}
	// Posting and reporting remain in the public discussion interface.
	v.Circle.CanPost, v.Circle.CanReport = false, false
	data := s.baseViewFor(w, "圈子工作台", "管理圈子、成员申请、内容与举报。", r.URL.Path)
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-circles"
	data.Admin = s.adminView("circles", principal, readOnly)
	data.Admin.CanManageCircles = v.CanManage || v.Circle.Manage
	data.Circles = v
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	s.render(w, http.StatusOK, "admin-circles", data)
}
