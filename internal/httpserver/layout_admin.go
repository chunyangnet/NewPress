package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/layout"
	"newpress.local/newpress/internal/theme"
)

type layoutField struct {
	Key, Value       string
	Boolean, Checked bool
}

func (s *Server) registerLayoutRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/appearance/layout/{$}", s.adminLayout)
	mux.HandleFunc("POST /admin/appearance/layout/{$}", s.adminLayoutSave)
}
func (s *Server) adminLayout(w http.ResponseWriter, r *http.Request) {
	p, ro, ok := s.requireAdminPageCapability(w, r, auth.CapabilityAppearanceManage)
	if !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"notice": true})
	if !ok {
		return
	}
	notice := adminNotice{}
	if q.Get("notice") == "saved" {
		notice = adminNotice{"布局已保存并应用到首页。", "success"}
	}
	s.renderLayout(w, r, 200, p, ro, s.themes.SettingsSnapshot(), nil, notice)
}
func (s *Server) renderLayout(w http.ResponseWriter, r *http.Request, status int, p auth.Principal, ro bool, snapshot theme.SettingsSnapshot, fields map[string]string, notice adminNotice) {
	if !s.themes.Active().Supports(theme.HomeLayoutV1) {
		s.renderFeatureRecovery(w, r, "layout", "", s.adminRecoveryIdentity("layout", p, ro), 501, "")
		return
	}
	data := s.baseViewFor(w, "可视化布局", "拖动模块，自定义站点首页", "/admin/appearance/layout/")
	data.IsAdmin, data.BodyClass, data.ActiveNav = true, "is-admin is-admin-layout", "account"
	data.Admin = s.adminView("layout", p, ro)
	data.Admin.Settings = snapshot
	data.Admin.FieldErrors, data.Admin.Notice, data.Admin.NoticeTone = fields, notice.Message, notice.Tone
	data.HomeEditor = s.homeLayoutEditor(snapshot.Values)
	data.HomeCustomBlocks = layout.EditorCustomJSON(snapshot.Values)
	layoutKeys := map[string]bool{"use_home_layout": true, "sidebar_enabled": true, "home_module_gap": true, "home_sidebar_width": true}
	for _, region := range layout.Catalog() {
		layoutKeys["home_"+region.Key+"_order"] = true
		for _, b := range region.Modules {
			if b.Key != "custom" {
				layoutKeys["home_"+b.Key+"_visibility"] = true
			}
		}
	}
	data.HomeModuleSettings = homeSettingsFor(snapshot.Groups)
	for _, panel := range data.HomeModuleSettings {
		for _, field := range panel.Fields {
			layoutKeys[field.Key] = true
		}
	}
	data.LayoutFields = hiddenThemeFields(snapshot.Values, layoutKeys)
	for _, group := range snapshot.Groups {
		for _, field := range group.Fields {
			if field.Key == "use_home_layout" || field.Key == "sidebar_enabled" || field.Key == "home_module_gap" || field.Key == "home_sidebar_width" {
				data.LayoutOptions = append(data.LayoutOptions, field)
			}
		}
	}
	s.render(w, status, "admin-layout", data)
}

// Carry settings edited on other pages through full-form saves and previews.
func hiddenThemeFields(values map[string]any, visible map[string]bool) []layoutField {
	fields := []layoutField{}
	for key, value := range values {
		if visible[key] {
			continue
		}
		field := layoutField{Key: key, Value: fmt.Sprint(value)}
		field.Checked, field.Boolean = value.(bool)
		fields = append(fields, field)
	}
	slices.SortFunc(fields, func(a, b layoutField) int { return strings.Compare(a.Key, b.Key) })
	return fields
}

func (s *Server) adminLayoutSave(w http.ResponseWriter, r *http.Request) {
	p, ro, ok := s.requireAdminPageCapability(w, r, auth.CapabilityAppearanceManage)
	if !ok {
		return
	}
	if !s.themes.Active().Supports(theme.HomeLayoutV1) {
		s.renderError(w, http.StatusNotImplemented, "当前主题尚未提供首页布局契约，可在外观设置中配置主题。")
		return
	}
	if ro || !s.sameOriginRequest(r) {
		s.writeAPIError(w, r, 403, "auth.write_forbidden", "当前不能保存布局", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	if r.URL.RawQuery != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || r.ParseForm() != nil {
		s.writeAPIError(w, r, 400, "layout.invalid_form", "布局表单格式不正确", nil)
		return
	}
	form := r.PostForm
	if len(form["csrf_token"]) != 1 || !s.auth.CheckCSRF(p, form.Get("csrf_token")) {
		s.writeAPIError(w, r, 403, "auth.csrf_validation_failed", "CSRF 校验失败", nil)
		return
	}
	current := s.themes.SettingsSnapshot()
	if len(form["revision"]) != 1 || form.Get("revision") == "" {
		s.writeAPIError(w, r, 400, "layout.revision_required", "请重新载入布局后保存", nil)
		return
	}
	for key, entries := range form {
		_, known := current.Values[key]
		if len(entries) != 1 || (!known && key != "csrf_token" && key != "revision") {
			s.writeAPIError(w, r, 400, "layout.invalid_form", "布局字段未知或重复", nil)
			return
		}
	}
	values, fields := s.themes.ParseSettingsForm(form)
	if len(fields) > 0 {
		s.renderLayout(w, r, 422, p, false, current, fields, adminNotice{"布局未保存，请检查模块位置与字段。", "error"})
		return
	}
	snapshot, fields, err := s.themes.SaveSettingsRevision(form.Get("revision"), values)
	if errors.Is(err, theme.ErrSettingsRevisionConflict) {
		s.renderLayout(w, r, 409, p, false, current, nil, adminNotice{"主题已在其他窗口更新，请核对最新布局再保存。", "error"})
		return
	}
	if err != nil {
		s.renderLayout(w, r, 500, p, false, current, fields, adminNotice{"布局保存失败，请稍后重试。", "error"})
		return
	}
	if len(fields) > 0 {
		s.renderLayout(w, r, 422, p, false, current, fields, adminNotice{"布局引用的资源不可用，请检查标记字段。", "error"})
		return
	}
	_ = snapshot
	http.Redirect(w, r, "/admin/appearance/layout/?notice=saved", http.StatusSeeOther)
}

// Appearance writes require the revision shown by the editor.
func (s *Server) saveAppearanceSettings(revision string, values map[string]any) (theme.SettingsSnapshot, map[string]string, error) {
	return s.themes.SaveSettingsRevision(revision, values)
}
