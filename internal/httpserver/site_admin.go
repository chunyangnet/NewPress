package httpserver

import (
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
)

const siteSettingsRequestMaxBytes = 512 << 10

func (s *Server) adminSiteSettings(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/admin/settings/" {
		s.renderError(writer, http.StatusNotFound, "站点设置页面没有找到")
		return
	}
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	s.renderSiteSettings(writer, request, http.StatusOK, principal, readOnly, nil, site.SettingsSnapshot{}, siteSettingsNoticeFromQuery(request.URL.Query().Get("notice")))
}

func (s *Server) adminSiteSettingsSubmit(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilitySettingsManage)
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
	request.Body = http.MaxBytesReader(writer, request.Body, siteSettingsRequestMaxBytes)
	mediaType, _, mediaErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
	switch {
	case mediaErr == nil && strings.EqualFold(mediaType, "multipart/form-data"):
		if err := request.ParseMultipartForm(256 << 10); err != nil {
			s.renderSiteSettings(writer, request, http.StatusBadRequest, principal, false, nil, site.SettingsSnapshot{}, adminNotice{"导入文件格式不正确", "error"})
			return
		}
	case mediaErr == nil && strings.EqualFold(mediaType, "application/x-www-form-urlencoded"):
		if err := request.ParseForm(); err != nil {
			s.renderSiteSettings(writer, request, http.StatusBadRequest, principal, false, nil, site.SettingsSnapshot{}, adminNotice{"设置请求格式不正确", "error"})
			return
		}
	default:
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "site_settings_form_content_type_required"})
		return
	}
	form := request.PostForm
	action, revision, ok := strictSiteSettingsControls(form)
	if !ok {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_site_settings_form_shape"})
		return
	}
	if !s.auth.CheckCSRF(principal, form.Get("csrf_token")) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return
	}

	switch action {
	case "save":
		settingsForm := cloneSiteSettingsValues(form)
		candidate, fieldErrors, err := site.ParseForm(settingsForm)
		if err != nil {
			s.renderSiteSettings(writer, request, http.StatusBadRequest, principal, false, nil, site.SettingsSnapshot{}, adminNotice{"设置字段未知、重复或缺失", "error"})
			return
		}
		candidate.Modules = s.siteSettings.Snapshot().Settings.Modules
		preview := s.siteSettings.Preview(candidate)
		if len(fieldErrors) > 0 {
			s.renderSiteSettings(writer, request, http.StatusUnprocessableEntity, principal, false, fieldErrors, preview, adminNotice{"有些站点设置未通过校验，请检查标记字段", "error"})
			return
		}
		snapshot, fieldErrors, err := s.siteSettings.Save(revision, candidate)
		if s.handleSiteSettingsSaveError(writer, request, principal, err, fieldErrors, snapshot) {
			return
		}
		http.Redirect(writer, request, "/admin/settings/?notice=saved", http.StatusSeeOther)
	case "reset":
		if len(form) != 3 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_site_settings_reset_shape"})
			return
		}
		snapshot, err := s.siteSettings.Reset(revision)
		if s.handleSiteSettingsSaveError(writer, request, principal, err, nil, snapshot) {
			return
		}
		http.Redirect(writer, request, "/admin/settings/?notice=reset", http.StatusSeeOther)
	case "import":
		if len(form) != 3 || request.MultipartForm == nil || len(request.MultipartForm.File) != 1 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_site_settings_import_shape"})
			return
		}
		files := request.MultipartForm.File["settings_file"]
		if len(files) != 1 || files[0].Size > 256<<10 {
			s.renderSiteSettings(writer, request, http.StatusUnprocessableEntity, principal, false, nil, site.SettingsSnapshot{}, adminNotice{"请选择不超过 256 KiB 的 NewPress 站点设置 JSON 文件", "error"})
			return
		}
		file, err := files[0].Open()
		if err != nil {
			s.renderSiteSettings(writer, request, http.StatusUnprocessableEntity, principal, false, nil, site.SettingsSnapshot{}, adminNotice{"无法读取导入文件", "error"})
			return
		}
		defer file.Close()
		snapshot, fieldErrors, err := s.siteSettings.Import(file, revision)
		if err != nil && !errors.Is(err, site.ErrRevisionConflict) && !errors.Is(err, site.ErrRevisionRequired) {
			s.renderSiteSettings(writer, request, http.StatusUnprocessableEntity, principal, false, fieldErrors, snapshot, adminNotice{"导入失败：" + err.Error(), "error"})
			return
		}
		if s.handleSiteSettingsSaveError(writer, request, principal, err, fieldErrors, snapshot) {
			return
		}
		http.Redirect(writer, request, "/admin/settings/?notice=imported", http.StatusSeeOther)
	default:
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unknown_site_settings_action"})
	}
}

func (s *Server) adminSiteSettingsExport(writer http.ResponseWriter, request *http.Request) {
	if _, _, ok := s.requireAdminPageCapability(writer, request, auth.CapabilitySettingsManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="newpress-site-settings.json"`)
	writer.Header().Set("Cache-Control", "no-store")
	if err := s.siteSettings.Export(writer); err != nil {
		slog.Error("export site settings", "error", err)
	}
}

func (s *Server) renderSiteSettings(writer http.ResponseWriter, request *http.Request, status int, principal auth.Principal, readOnly bool, fieldErrors map[string]string, override site.SettingsSnapshot, notice adminNotice) {
	data := s.baseViewFor(writer, "站点设置", "集中管理站点身份、写作、评论、阅读、API、SEO 与运维策略", "/admin/settings/")
	data.BodyClass = "is-admin is-admin-site-settings"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("settings", principal, readOnly)
	if override.Revision != "" {
		data.Admin.SiteSettings = override
	}
	data.Admin.FieldErrors = fieldErrors
	data.Admin.Notice = notice.Message
	data.Admin.NoticeTone = notice.Tone
	data.Admin.Runtime = s.siteRuntimeSettings()
	s.render(writer, status, "admin-settings", data)
}

func (s *Server) handleSiteSettingsSaveError(writer http.ResponseWriter, request *http.Request, principal auth.Principal, err error, fieldErrors map[string]string, snapshot site.SettingsSnapshot) bool {
	switch {
	case errors.Is(err, site.ErrRevisionRequired):
		s.renderSiteSettings(writer, request, http.StatusPreconditionRequired, principal, false, fieldErrors, snapshot, adminNotice{"缺少设置修订号，请刷新页面后重试", "error"})
		return true
	case errors.Is(err, site.ErrRevisionConflict):
		s.renderSiteSettings(writer, request, http.StatusConflict, principal, false, fieldErrors, snapshot, adminNotice{"设置已被其他会话更新，当前值已重新载入，请合并后保存", "error"})
		return true
	case err != nil:
		slog.Error("save site settings", "error", err)
		s.renderSiteSettings(writer, request, http.StatusInternalServerError, principal, false, fieldErrors, snapshot, adminNotice{"站点设置写入失败，现有配置保持不变", "error"})
		return true
	case len(fieldErrors) > 0:
		s.renderSiteSettings(writer, request, http.StatusUnprocessableEntity, principal, false, fieldErrors, snapshot, adminNotice{"有些站点设置未通过校验", "error"})
		return true
	default:
		return false
	}
}

func strictSiteSettingsControls(form url.Values) (string, string, bool) {
	for _, key := range []string{"csrf_token", "action", "revision"} {
		if len(form[key]) != 1 {
			return "", "", false
		}
	}
	return form.Get("action"), form.Get("revision"), true
}

func cloneSiteSettingsValues(form url.Values) url.Values {
	values := make(url.Values, len(form)-3)
	for key, entries := range form {
		if key == "csrf_token" || key == "action" || key == "revision" {
			continue
		}
		values[key] = append([]string(nil), entries...)
	}
	return values
}

func siteSettingsNoticeFromQuery(value string) adminNotice {
	switch value {
	case "saved":
		return adminNotice{Message: "站点设置已保存并立即应用。备案与页脚内容已更新，刷新首页即可查看。", Tone: "success"}
	case "reset":
		return adminNotice{Message: "已恢复系统内置的站点默认值", Tone: "success"}
	case "imported":
		return adminNotice{Message: "站点设置文件已完成完整性与字段校验并导入", Tone: "success"}
	default:
		return adminNotice{}
	}
}

func (s *Server) siteRuntimeSettings() []adminRuntimeSetting {
	storage := "内存存储"
	storageDetail := "适合开发与测试；进程重启后内容会重置"
	if s.config.DatabaseURL != "" {
		storage = "PostgreSQL"
		storageDetail = "数据库连接由 NEWPRESS_DATABASE_URL 或 secret file 管理"
	}
	authentication := "本机只读预览"
	authStatus := "warning"
	if s.auth.Configured() {
		authentication = "Argon2id + 安全会话"
		authStatus = "pass"
	}
	clientAddressing := "TCP 对端"
	clientAddressingDetail := "默认不信任 X-Forwarded-For 或 X-Real-IP"
	if count := len(s.config.TrustedProxyCIDRs); count > 0 {
		clientAddressing = "显式可信代理"
		clientAddressingDetail = strconv.Itoa(count) + " 个 CIDR；严格解析最多 32 跳 X-Forwarded-For"
	}
	if len(s.config.CDNProxies) > 0 {
		clientAddressing = "可信代理与 CDN"
		clientAddressingDetail += "；已启用 " + strconv.Itoa(len(s.config.CDNProxies)) + " 家 CDN 独立回源验证，异常来源不用于属地查询"
	}
	return []adminRuntimeSetting{
		{Label: "公开基准 URL", Value: strings.TrimRight(s.config.PublicURL, "/"), Detail: "环境只读，用于 canonical、Sitemap 与 security.txt", Status: "locked"},
		{Label: "活动主题", Value: s.themes.Active().Name + " v" + s.themes.Active().Version, Detail: "主题切换保持部署期控制，避免运行时模板漂移", Status: "locked"},
		{Label: "内容存储", Value: storage, Detail: storageDetail, Status: "locked"},
		{Label: "管理认证", Value: authentication, Detail: "绝对会话 " + s.config.AdminSessionTTL.String() + " · 空闲 " + s.config.AdminIdleTTL.String(), Status: authStatus},
		{Label: "客户端来源", Value: clientAddressing, Detail: clientAddressingDetail, Status: "locked"},
		{Label: "数据目录", Value: filepath.Clean(s.config.DataDir), Detail: "站点设置文件权限为 0600，并通过临时文件原子替换", Status: "locked"},
		{Label: "上传上限", Value: humanBytes(s.effectiveMaxUploadBytes()), Detail: "可在媒体上传设置中调整；0 沿用部署默认值", Status: "editable"},
	}
}

func humanBytes(value int64) string {
	if value >= 1<<30 {
		return strconv.FormatFloat(float64(value)/(1<<30), 'f', 1, 64) + " GiB"
	}
	if value >= 1<<20 {
		return strconv.FormatFloat(float64(value)/(1<<20), 'f', 1, 64) + " MiB"
	}
	if value >= 1<<10 {
		return strconv.FormatFloat(float64(value)/(1<<10), 'f', 1, 64) + " KiB"
	}
	return fmt.Sprintf("%d B", value)
}
