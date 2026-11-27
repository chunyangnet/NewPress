package httpserver

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/layout"
	"newpress.local/newpress/internal/theme"
)

func (s *Server) adminAppearance(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/admin/appearance/" {
		s.renderError(writer, http.StatusNotFound, "外观设置页面没有找到")
		return
	}
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityAppearanceManage)
	if !ok {
		return
	}
	s.renderAppearance(writer, request, http.StatusOK, principal, readOnly, nil, noticeFromQuery(request.URL.Query().Get("notice")))
}

func (s *Server) adminAppearanceSubmit(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityAppearanceManage)
	if !ok {
		return
	}
	if readOnly || !s.auth.Configured() {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "admin_credentials_not_configured"})
		return
	}
	if !s.sameOriginRequest(request) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "cross_site_request_rejected"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 512<<10)
	contentType := request.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := request.ParseMultipartForm(256 << 10); err != nil {
			s.renderAppearance(writer, request, http.StatusBadRequest, principal, false, nil, adminNotice{"导入文件格式不正确", "error"})
			return
		}
	} else if err := request.ParseForm(); err != nil {
		s.renderAppearance(writer, request, http.StatusBadRequest, principal, false, nil, adminNotice{"设置请求格式不正确", "error"})
		return
	}
	if !s.auth.CheckCSRF(principal, request.FormValue("csrf_token")) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return
	}

	action := request.FormValue("action")
	switch action {
	case "reset":
		if _, err := s.themes.ResetSettingsRevision(request.FormValue("revision")); errors.Is(err, theme.ErrSettingsRevisionConflict) {
			s.renderAppearance(writer, request, http.StatusConflict, principal, false, nil, adminNotice{"主题已变化，请刷新后核对再恢复默认。", "error"})
			return
		} else if err != nil {
			slog.Error("reset theme settings", "error", err)
			s.renderAppearance(writer, request, http.StatusInternalServerError, principal, false, nil, adminNotice{"恢复默认设置失败", "error"})
			return
		}
		http.Redirect(writer, request, "/admin/appearance/?notice=reset", http.StatusSeeOther)
	case "import":
		file, _, err := request.FormFile("settings_file")
		if err != nil {
			s.renderAppearance(writer, request, http.StatusUnprocessableEntity, principal, false, nil, adminNotice{"请选择有效的 JSON 设置文件", "error"})
			return
		}
		defer file.Close()
		if _, fieldErrors, err := s.themes.ImportSettingsRevision(file, request.FormValue("revision")); err != nil {
			s.renderAppearance(writer, request, http.StatusUnprocessableEntity, principal, false, fieldErrors, adminNotice{"导入失败：" + err.Error(), "error"})
			return
		}
		http.Redirect(writer, request, "/admin/appearance/?notice=imported", http.StatusSeeOther)
	case "save":
		values, fieldErrors := s.themes.ParseSettingsForm(request.Form)
		if len(fieldErrors) > 0 {
			s.renderAppearance(writer, request, http.StatusUnprocessableEntity, principal, false, fieldErrors, adminNotice{"有些设置未通过校验，请检查标记字段", "error"})
			return
		}
		if _, fieldErrors, err := s.saveAppearanceSettings(request.FormValue("revision"), values); errors.Is(err, theme.ErrSettingsRevisionConflict) {
			s.renderAppearance(writer, request, http.StatusConflict, principal, false, nil, adminNotice{"主题已在其他窗口更新，请刷新后核对再保存。", "error"})
			return
		} else if err != nil {
			slog.Error("save theme settings", "error", err)
			s.renderAppearance(writer, request, http.StatusInternalServerError, principal, false, fieldErrors, adminNotice{"保存主题设置失败", "error"})
			return
		} else if len(fieldErrors) > 0 {
			s.renderAppearance(writer, request, http.StatusUnprocessableEntity, principal, false, fieldErrors, adminNotice{"有些设置未通过校验", "error"})
			return
		}
		http.Redirect(writer, request, "/admin/appearance/?notice=saved", http.StatusSeeOther)
	default:
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unknown_settings_action"})
	}
}

func (s *Server) adminAppearanceExport(writer http.ResponseWriter, request *http.Request) {
	if _, _, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityAppearanceManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="`+s.themes.Active().Slug+`-settings.json"`)
	writer.Header().Set("Cache-Control", "no-store")
	if err := s.themes.ExportSettings(writer); err != nil {
		slog.Error("export theme settings", "error", err)
	}
}

type adminNotice struct {
	Message string
	Tone    string
}

func (s *Server) renderAppearance(writer http.ResponseWriter, request *http.Request, status int, principal auth.Principal, readOnly bool, fieldErrors map[string]string, notice adminNotice) {
	data := s.baseViewFor(writer, "外观与主题设置", "使用结构化设置定制当前主题", "/admin/appearance/")
	data.BodyClass = "is-admin is-admin-appearance"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("appearance", principal, readOnly)
	data.Admin.Settings = s.themes.SettingsSnapshot()
	data.HomeEditor = s.homeLayoutEditor(data.Admin.Settings.Values)
	data.HomeCustomBlocks = layout.EditorCustomJSON(data.Admin.Settings.Values)
	visible := map[string]bool{}
	for _, group := range data.Admin.Settings.Groups {
		for _, field := range group.Fields {
			visible[field.Key] = true
		}
	}
	for _, region := range data.HomeEditor {
		visible["home_"+region.Key+"_order"] = true
		for _, block := range region.Modules {
			if block.Custom == nil {
				visible["home_"+block.Key+"_visibility"] = true
			}
		}
	}
	data.AppearanceFields = hiddenThemeFields(data.Admin.Settings.Values, visible)
	if len(data.HomeEditor) > 0 {
		data.HomeModuleSettings = homeSettingsFor(data.Admin.Settings.Groups)
		data.Admin.Settings.Groups = withoutHomeSettings(data.Admin.Settings.Groups)
	}
	data.Admin.FieldErrors = fieldErrors
	data.Admin.Notice = notice.Message
	data.Admin.NoticeTone = notice.Tone
	s.render(writer, status, "admin-appearance", data)
}

func (s *Server) themeSettingsCSS(writer http.ResponseWriter, request *http.Request) {
	snapshot := s.themes.SettingsCSSSnapshot()
	etag := `"theme-settings-` + snapshot.Revision + `"`
	writer.Header().Set("Content-Type", "text/css; charset=utf-8")
	if request.URL.Query().Get("v") == snapshot.Revision {
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if request.URL.Query().Get("v") != "" {
		// Never cache current bytes under a stale content identity.
		writer.Header().Set("Cache-Control", "no-store")
	} else {
		writer.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	}
	writer.Header().Set("ETag", etag)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if etagMatches(request.Header.Get("If-None-Match"), etag) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = io.WriteString(writer, snapshot.CSS)
}

func noticeFromQuery(value string) adminNotice {
	switch value {
	case "saved":
		return adminNotice{Message: "主题设置已保存并立即生效", Tone: "success"}
	case "reset":
		return adminNotice{Message: "已恢复当前主题默认设置", Tone: "success"}
	case "imported":
		return adminNotice{Message: "设置文件已校验并导入", Tone: "success"}
	default:
		return adminNotice{}
	}
}
