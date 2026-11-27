package httpserver

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/module"
	"newpress.local/newpress/internal/site"
)

type routeRegistrar interface {
	HandleFunc(string, func(http.ResponseWriter, *http.Request))
	Handle(string, http.Handler)
}
type moduleRoute struct {
	Pattern string `json:"pattern"`
	Module  string `json:"module"`
}
type moduleMux struct {
	*http.ServeMux
	server *Server
}

func (m *moduleMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.Handle(pattern, http.HandlerFunc(handler))
}
func (m *moduleMux) Handle(pattern string, handler http.Handler) {
	id := module.Owner(pattern)
	m.server.moduleRoutes = append(m.server.moduleRoutes, moduleRoute{pattern, id})
	m.ServeMux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.server.moduleEnabled(id) {
			w.Header().Set("Cache-Control", "no-store")
			if m.server.renderModuleRecovery(w, r, id) {
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/") {
				m.server.writeAPIError(w, r, 404, "module.disabled", "此功能已由站点管理员停用", map[string]string{"module": id})
			} else {
				definition, _ := module.Lookup(id)
				m.server.renderServiceStatus(w, 404, definition.Title, "disabled")
			}
			return
		}
		handler.ServeHTTP(w, r)
	}))
}
func (v pageView) ModuleEnabled(id string) bool { return module.Allowed(v.ModuleOverrides, id) }

func (s *Server) moduleEnabled(id string) bool { return s.siteSettings.ModuleEnabled(id) }

type moduleView struct {
	module.Definition
	Enabled   bool     `json:"enabled"`
	Available bool     `json:"available"`
	State     string   `json:"state"`
	Status    string   `json:"status"`
	Reason    string   `json:"reason,omitempty"`
	Routes    []string `json:"routes"`
}
type modulePage struct {
	Items                       []moduleView
	Revision                    string
	Enabled, Reserved, Switches int
}

func (s *Server) moduleCatalog(overrides map[string]bool) []moduleView {
	result := []moduleView{}
	for _, definition := range module.Catalog() {
		v := moduleView{Definition: definition, Enabled: module.Allowed(overrides, definition.ID), State: "enabled", Status: "已启用", Routes: []string{}}
		switch definition.Control {
		case "core":
			v.Status = "基础模块"
		case "native":
			v.Status = "独立配置"
		case "reserved":
			v.State, v.Status, v.Reason = "reserved", "预留", definition.Description
		}
		if definition.Control == "switch" && !v.Enabled {
			v.State, v.Status, v.Reason = "disabled", "已暂停", "配置和数据已保留，可重新启用。"
		}
		if v.Enabled {
			for _, check := range s.moduleRuntimeChecks(definition.ID) {
				if check.State != "ready" {
					v.State, v.Status, v.Reason = "unavailable", "待配置", check.Reason
					if check.State == "error" {
						v.Status = "异常"
					}
					break
				}
			}
		}
		v.Available = v.Enabled && v.State == "enabled"
		for _, route := range s.moduleRoutes {
			if route.Module == definition.ID {
				v.Routes = append(v.Routes, route.Pattern)
			}
		}
		slices.Sort(v.Routes)
		result = append(result, v)
	}
	return result
}

func (s *Server) registerModuleRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/v1/admin/feature-status", s.adminFeatureStatusAPI)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/feature-status", s.adminAPIMethodNotAllowed("GET"))
	}
	mux.HandleFunc("GET /admin/modules/{$}", s.adminModules)
	mux.HandleFunc("POST /admin/modules/{$}", s.adminModulesSave)
	mux.HandleFunc("GET /api/v1/admin/modules", s.adminModulesAPI)
	mux.HandleFunc("PATCH /api/v1/admin/modules", s.adminModulesAPI)
	for _, method := range []string{"POST", "PUT", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/modules", s.adminAPIMethodNotAllowed("GET, PATCH"))
	}
}
func (s *Server) adminModules(w http.ResponseWriter, r *http.Request) {
	p, ro, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"notice": true})
	if !ok {
		return
	}
	notice := adminNotice{}
	if q.Get("notice") == "saved" {
		notice = adminNotice{"模块配置已保存。标为「待配置」的模块仍需完成卡片中的服务器配置才能使用。", "success"}
	}
	s.renderModules(w, r, 200, p, ro, s.siteSettings.Snapshot(), nil, notice)
}
func (s *Server) renderModules(w http.ResponseWriter, r *http.Request, status int, p auth.Principal, ro bool, snapshot site.SettingsSnapshot, fields map[string]string, notice adminNotice) {
	data := s.baseViewFor(w, "模块中心", "管理站点模块、依赖与扩展入口", "/admin/modules/")
	data.IsAdmin, data.BodyClass, data.ActiveNav = true, "is-admin is-admin-modules", "account"
	data.Admin = s.adminView("modules", p, ro)
	data.Admin.DetailTitle = "模块中心"
	data.Admin.FieldErrors, data.Admin.Notice, data.Admin.NoticeTone = fields, notice.Message, notice.Tone
	data.Modules = modulePage{Items: s.moduleCatalog(snapshot.Settings.Modules), Revision: snapshot.Revision}
	for i := range data.Modules.Items {
		v := &data.Modules.Items[i]
		if v.Capability != "" && !data.Admin.Can(v.Capability) {
			v.ConfigURL = ""
		}
		if v.Available {
			data.Modules.Enabled++
		}
		if v.Control == "reserved" {
			data.Modules.Reserved++
		}
		if v.Control == "switch" {
			data.Modules.Switches++
		}
	}
	s.render(w, status, "admin-modules", data)
}
func (s *Server) adminModulesSave(w http.ResponseWriter, r *http.Request) {
	p, ro, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if ro || !s.sameOriginRequest(r) {
		s.writeAPIError(w, r, 403, "auth.write_forbidden", "当前不能保存模块设置", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.URL.RawQuery != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || r.ParseForm() != nil {
		s.writeAPIError(w, r, 400, "request.invalid_form", "模块表单格式不正确", nil)
		return
	}
	form := r.PostForm
	if len(form["csrf_token"]) != 1 || !s.auth.CheckCSRF(p, form.Get("csrf_token")) {
		s.writeAPIError(w, r, 403, "auth.csrf_validation_failed", "CSRF 校验失败", nil)
		return
	}
	values, err := moduleForm(form)
	if err != nil {
		s.writeAPIError(w, r, 400, "request.invalid_form", "模块字段未知、重复或缺失", nil)
		return
	}
	current := s.siteSettings.Snapshot()
	current.Settings.Modules = values
	snapshot, fields, err := s.siteSettings.Save(form.Get("revision"), current.Settings)
	if err != nil || len(fields) > 0 {
		status, message := 422, "模块设置未通过校验"
		if errors.Is(err, site.ErrRevisionConflict) {
			status, message = 409, "配置已在其他窗口更新，请刷新后核对再保存。"
		} else if err != nil {
			status, message = 500, "模块配置保存失败，请保留当前输入后重试。"
		}
		s.renderModules(w, r, status, p, false, snapshot, fields, adminNotice{message, "error"})
		return
	}
	http.Redirect(w, r, "/admin/modules/?notice=saved", http.StatusSeeOther)
}
func moduleForm(form url.Values) (map[string]bool, error) {
	if len(form["revision"]) != 1 || form.Get("revision") == "" {
		return nil, site.ErrRevisionRequired
	}
	values := map[string]bool{}
	for _, d := range module.Catalog() {
		if d.Control == "switch" {
			values[d.ID] = false
		}
	}
	for key, items := range form {
		if len(items) != 1 {
			return nil, site.ErrInvalidForm
		}
		if key == "csrf_token" || key == "revision" {
			continue
		}
		id, found := strings.CutPrefix(key, "module.")
		if !found {
			return nil, site.ErrInvalidForm
		}
		if _, known := values[id]; !known || items[0] != "true" {
			return nil, site.ErrInvalidForm
		}
		values[id] = true
	}
	return values, nil
}
func (s *Server) adminModulesAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
			return
		}
		if _, ok := s.strictAPIQuery(w, r, nil); !ok {
			return
		}
	} else {
		if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage); !ok {
			return
		}
		if _, ok := s.strictAPIQuery(w, r, nil); !ok {
			return
		}
		var input struct {
			Revision string           `json:"revision"`
			Modules  map[string]*bool `json:"modules"`
		}
		if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 16<<10, "16 KiB"); !ok {
			return
		}
		if input.Revision == "" || len(input.Modules) == 0 {
			s.writeAPIError(w, r, 400, "module.invalid", "需要修订号和模块设置", nil)
			return
		}
		current := s.siteSettings.Snapshot()
		if current.Settings.Modules == nil {
			current.Settings.Modules = map[string]bool{}
		}
		for id, value := range input.Modules {
			if value == nil {
				s.writeAPIError(w, r, 400, "module.invalid", "开关必须是布尔值", nil)
				return
			}
			current.Settings.Modules[id] = *value
		}
		_, fields, err := s.siteSettings.Save(input.Revision, current.Settings)
		if errors.Is(err, site.ErrRevisionConflict) {
			s.writeAPIError(w, r, 409, "module.revision_conflict", "设置已更新，请重新载入", nil)
			return
		}
		if len(fields) > 0 {
			s.writeAPIError(w, r, 422, "module.invalid", "模块配置不受支持", fields)
			return
		}
		if err != nil {
			s.writeAPIError(w, r, 500, "module.save_failed", "模块配置保存失败", nil)
			return
		}
	}
	snapshot := s.siteSettings.Snapshot()
	writeJSON(w, 200, map[string]any{"schema": module.Version, "revision": snapshot.Revision, "items": s.moduleCatalog(snapshot.Settings.Modules)})
}

func (s *Server) enabledMobileDock(items []navigationItemView) []navigationItemView {
	return slices.DeleteFunc(items, func(item navigationItemView) bool { return !s.moduleURLAvailable(item.URL) })
}

func (s *Server) moduleURLAvailable(raw string) bool {
	target, err := url.Parse(raw)
	if err != nil {
		return false
	}
	base, err := url.Parse(s.config.PublicURL)
	if target.Host != "" && (err != nil || !strings.EqualFold(target.Host, base.Host)) {
		return true
	}
	return s.moduleEnabled(module.Owner(target.Path))
}
