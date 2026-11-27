package httpserver

import (
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
)

const adminSessionsFormMaxBytes = 16 << 10

var (
	errSessionScopeInvalid   = errors.New("invalid session scope")
	errSessionScopeForbidden = errors.New("session scope forbidden")
)

type adminSessionsView struct {
	Rows             []adminSessionRow
	Scope            string
	MineURL          string
	AllURL           string
	CanViewAll       bool
	CanRevoke        bool
	CanRevokeOthers  bool
	Total            int
	MineCount        int
	OtherCount       int
	UserCount        int
	ExpiringSoon     int
	AbsoluteTTL      string
	IdleTTL          string
	CookieSecure     bool
	GeneratedAtLabel string
}

type adminSessionRow struct {
	Session          auth.SessionInfo
	ClientName       string
	PlatformName     string
	DeviceKind       string
	IPAddress        string
	Fingerprint      string
	UserInitial      string
	CreatedLabel     string
	LastSeenLabel    string
	LastSeenRelative string
	IdleExpiresLabel string
	IdleRemaining    string
	AbsoluteLabel    string
	AbsoluteRemain   string
	IdleTone         string
	IsMine           bool
	CanRevoke        bool
}

func (s *Server) adminSessions(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilitySessionsRead)
	if !ok {
		return
	}
	query, ok := strictQueryShape(request.URL.Query(), map[string]bool{"scope": true, "notice": true})
	if !ok {
		s.renderError(writer, http.StatusBadRequest, "会话筛选参数不正确")
		return
	}
	scope, err := resolveAdminSessionScope(query.Get("scope"), principal, readOnly)
	if errors.Is(err, errSessionScopeForbidden) {
		s.renderAdminSessions(writer, request, http.StatusForbidden, principal, readOnly, currentSessionToken(s, request, readOnly), "mine", adminNotice{
			Message: "当前角色只能查看并管理自己的登录设备", Tone: "error",
		})
		return
	}
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "会话范围必须是 mine 或 all")
		return
	}
	s.renderAdminSessions(writer, request, http.StatusOK, principal, readOnly, currentSessionToken(s, request, readOnly), scope, sessionNoticeFromQuery(query.Get("notice")))
}

func (s *Server) adminSessionsSubmit(writer http.ResponseWriter, request *http.Request) {
	principal, token, form, ok := s.requireSessionManagementForm(writer, request)
	if !ok {
		return
	}
	scope, err := resolveAdminSessionScope(form.Get("scope"), principal, false)
	if errors.Is(err, errSessionScopeForbidden) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "session_scope_forbidden"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_session_scope"})
		return
	}

	switch form.Get("action") {
	case "revoke":
		if len(form["session_id"]) != 1 || form.Get("session_id") == "" {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "session_id_required"})
			return
		}
		_, err = s.auth.RevokeSessionIDContext(request.Context(), principal, token, form.Get("session_id"))
		if err != nil {
			s.renderSessionMutationError(writer, request, principal, token, scope, err)
			return
		}
		http.Redirect(writer, request, adminSessionsURL(scope, "revoked"), http.StatusSeeOther)
	case "revoke_others":
		if _, present := form["session_id"]; present {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_session_form_shape"})
			return
		}
		_, err = s.auth.RevokeOtherSessionsContext(request.Context(), principal, token)
		if err != nil {
			s.renderSessionMutationError(writer, request, principal, token, scope, err)
			return
		}
		http.Redirect(writer, request, adminSessionsURL(scope, "others"), http.StatusSeeOther)
	default:
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unknown_session_action"})
	}
}

func (s *Server) requireSessionManagementForm(writer http.ResponseWriter, request *http.Request) (auth.Principal, string, url.Values, bool) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilitySessionsManage, auth.CapabilitySessionsOwn)
	if !ok {
		return auth.Principal{}, "", nil, false
	}
	if readOnly || !s.auth.Configured() {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "admin_credentials_not_configured"})
		return auth.Principal{}, "", nil, false
	}
	if _, ok := strictQueryShape(request.URL.Query(), nil); !ok {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_session_query"})
		return auth.Principal{}, "", nil, false
	}
	if !s.sameOriginRequest(request) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "cross_site_request_rejected"})
		return auth.Principal{}, "", nil, false
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/x-www-form-urlencoded") || len(parameters) != 0 {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "form_content_type_required"})
		return auth.Principal{}, "", nil, false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, adminSessionsFormMaxBytes)
	if err := request.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]string{"error": "session_form_too_large"})
			return auth.Principal{}, "", nil, false
		}
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_session_form"})
		return auth.Principal{}, "", nil, false
	}
	form := request.PostForm
	if invalidSessionForm(form) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_session_form_shape"})
		return auth.Principal{}, "", nil, false
	}
	if !s.auth.CheckCSRF(principal, form.Get("csrf_token")) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return auth.Principal{}, "", nil, false
	}
	_, token, ok := s.currentAdminSession(request)
	if !ok || token == "" {
		writer.Header().Set("WWW-Authenticate", `NewPressSession realm="admin"`)
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "authentication_required"})
		return auth.Principal{}, "", nil, false
	}
	return principal, token, form, true
}

func invalidSessionForm(form url.Values) bool {
	allowed := map[string]bool{"csrf_token": true, "action": true, "scope": true, "session_id": true}
	for key, values := range form {
		if !allowed[key] || len(values) != 1 {
			return true
		}
	}
	return len(form["csrf_token"]) != 1 || len(form["action"]) != 1 || len(form["scope"]) != 1
}

func (s *Server) renderAdminSessions(writer http.ResponseWriter, request *http.Request, statusCode int, principal auth.Principal, readOnly bool, currentToken, scope string, notice adminNotice) {
	now := time.Now()
	location := site.TimeLocation(s.siteSnapshot(request.Context()).Settings)
	mine, err := s.auth.ListSessionsContext(request.Context(), principal, currentToken, false)
	if err != nil {
		s.sessionStorageError(writer, request)
		return
	}
	selected := mine
	canViewAll := readOnly || principal.Can(auth.CapabilitySessionsManage)
	if scope == "all" && principal.Can(auth.CapabilitySessionsManage) {
		selected, err = s.auth.ListSessionsContext(request.Context(), principal, currentToken, true)
		if err != nil {
			s.sessionStorageError(writer, request)
			return
		}
	}

	otherCount := 0
	for _, item := range mine {
		if !item.Current {
			otherCount++
		}
	}
	identities := make(map[string]struct{})
	expiringSoon := 0
	rows := make([]adminSessionRow, 0, len(selected))
	for _, item := range selected {
		identities[sessionIdentityKey(item)] = struct{}{}
		remaining := item.IdleExpiresAt.Sub(now)
		if remaining > 0 && remaining <= 15*time.Minute {
			expiringSoon++
		}
		clientName, platformName, deviceKind := describeSessionClient(item.UserAgent)
		isMine := principalOwnsSession(principal, item)
		ipAddress := item.IPAddress
		if ipAddress == "" {
			ipAddress = "未记录"
		}
		rows = append(rows, adminSessionRow{
			Session: item, ClientName: clientName, PlatformName: platformName, DeviceKind: deviceKind,
			IPAddress: ipAddress, Fingerprint: sessionFingerprint(item.ID), UserInitial: sessionUserInitial(item.DisplayName, item.Username),
			CreatedLabel:  item.CreatedAt.In(location).Format("2006-01-02 15:04 MST"),
			LastSeenLabel: item.LastSeenAt.In(location).Format("2006-01-02 15:04 MST"), LastSeenRelative: sessionAgo(now, item.LastSeenAt),
			IdleExpiresLabel: item.IdleExpiresAt.In(location).Format("2006-01-02 15:04 MST"), IdleRemaining: sessionUntil(now, item.IdleExpiresAt),
			AbsoluteLabel: item.ExpiresAt.In(location).Format("2006-01-02 15:04 MST"), AbsoluteRemain: sessionUntil(now, item.ExpiresAt),
			IdleTone: sessionIdleTone(remaining), IsMine: isMine,
			CanRevoke: !readOnly && !item.Current && (principal.Can(auth.CapabilitySessionsManage) || (principal.Can(auth.CapabilitySessionsOwn) && isMine)),
		})
	}

	absoluteTTL := configuredSessionDuration(s.config.AdminSessionTTL, 12*time.Hour)
	idleTTL := configuredSessionDuration(s.config.AdminIdleTTL, 30*time.Minute)
	publicURL, _ := url.Parse(s.config.PublicURL)
	data := s.baseViewFor(writer, "登录与设备", "查看当前登录会话、设备指纹、空闲窗口并安全撤销会话", "/admin/sessions/")
	data.BodyClass = "is-admin is-admin-sessions"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("sessions", principal, readOnly)
	data.Admin.Notice = notice.Message
	data.Admin.NoticeTone = notice.Tone
	data.Sessions = adminSessionsView{
		Rows: rows, Scope: scope, MineURL: adminSessionsURL("mine", ""), AllURL: adminSessionsURL("all", ""),
		CanViewAll: canViewAll, CanRevoke: !readOnly && (principal.Can(auth.CapabilitySessionsManage) || principal.Can(auth.CapabilitySessionsOwn)),
		CanRevokeOthers: !readOnly && otherCount > 0 && (principal.Can(auth.CapabilitySessionsManage) || principal.Can(auth.CapabilitySessionsOwn)),
		Total:           len(selected), MineCount: len(mine), OtherCount: otherCount, UserCount: len(identities), ExpiringSoon: expiringSoon,
		AbsoluteTTL: sessionDurationLabel(absoluteTTL), IdleTTL: sessionDurationLabel(idleTTL), CookieSecure: publicURL != nil && strings.EqualFold(publicURL.Scheme, "https"),
		GeneratedAtLabel: now.In(location).Format("2006-01-02 15:04:05 MST"),
	}
	s.render(writer, statusCode, "admin-sessions", data)
}

func (s *Server) renderSessionMutationError(writer http.ResponseWriter, request *http.Request, principal auth.Principal, token, scope string, err error) {
	if errors.Is(err, auth.ErrSessionUnavailable) {
		s.sessionStorageError(writer, request)
		return
	}
	status := http.StatusInternalServerError
	notice := adminNotice{Message: "会话操作暂时无法完成，现有会话保持不变", Tone: "error"}
	switch {
	case errors.Is(err, auth.ErrSessionNotFound):
		status = http.StatusNotFound
		notice.Message = "目标会话已过期、已被撤销或不存在"
	case errors.Is(err, auth.ErrSessionForbidden):
		status = http.StatusForbidden
		notice.Message = "当前角色不能撤销其他账号的登录会话"
	case errors.Is(err, auth.ErrCurrentSession):
		status = http.StatusConflict
		notice.Message = "当前设备必须使用“退出登录”结束，避免浏览器残留有效 Cookie"
	default:
		slog.Error("mutate admin session", "error", err, "request_id", request.Header.Get("X-Request-ID"))
	}
	s.renderAdminSessions(writer, request, status, principal, false, token, scope, notice)
}

func resolveAdminSessionScope(value string, principal auth.Principal, readOnly bool) (string, error) {
	canViewAll := readOnly || principal.Can(auth.CapabilitySessionsManage)
	if value == "" {
		if canViewAll {
			return "all", nil
		}
		return "mine", nil
	}
	if value != "mine" && value != "all" {
		return "", errSessionScopeInvalid
	}
	if value == "all" && !canViewAll {
		return "", errSessionScopeForbidden
	}
	return value, nil
}

func currentSessionToken(s *Server, request *http.Request, readOnly bool) string {
	if readOnly {
		return ""
	}
	_, token, _ := s.currentAdminSession(request)
	return token
}

func adminSessionsURL(scope, notice string) string {
	values := make(url.Values)
	if scope != "" {
		values.Set("scope", scope)
	}
	if notice != "" {
		values.Set("notice", notice)
	}
	if encoded := values.Encode(); encoded != "" {
		return "/admin/sessions/?" + encoded
	}
	return "/admin/sessions/"
}

func sessionNoticeFromQuery(value string) adminNotice {
	switch value {
	case "revoked":
		return adminNotice{Message: "目标设备的会话已立即撤销，后续请求将要求重新登录", Tone: "success"}
	case "others":
		return adminNotice{Message: "当前账号在其他设备上的会话已全部注销，当前设备保持在线", Tone: "success"}
	default:
		return adminNotice{}
	}
}

func principalOwnsSession(principal auth.Principal, item auth.SessionInfo) bool {
	if principal.UserID > 0 || item.UserID > 0 {
		return principal.UserID > 0 && principal.UserID == item.UserID
	}
	return principal.Username != "" && strings.EqualFold(principal.Username, item.Username)
}

func sessionIdentityKey(item auth.SessionInfo) string {
	if item.UserID > 0 {
		return fmt.Sprintf("id:%d", item.UserID)
	}
	return "username:" + strings.ToLower(item.Username)
}

func describeSessionClient(userAgent string) (string, string, string) {
	if userAgent == "" {
		return "未记录客户端", "未知平台", "unknown"
	}
	lower := strings.ToLower(userAgent)
	client := "其他客户端"
	switch {
	case strings.Contains(lower, "edg/"):
		client = "Microsoft Edge" + sessionClientVersion(userAgent, "Edg/")
	case strings.Contains(lower, "opr/"):
		client = "Opera" + sessionClientVersion(userAgent, "OPR/")
	case strings.Contains(lower, "firefox/"):
		client = "Firefox" + sessionClientVersion(userAgent, "Firefox/")
	case strings.Contains(lower, "chrome/") || strings.Contains(lower, "crios/"):
		marker := "Chrome/"
		if strings.Contains(lower, "crios/") {
			marker = "CriOS/"
		}
		client = "Chrome" + sessionClientVersion(userAgent, marker)
	case strings.Contains(lower, "safari/") && strings.Contains(lower, "version/"):
		client = "Safari" + sessionClientVersion(userAgent, "Version/")
	case strings.Contains(lower, "postmanruntime/"):
		client = "Postman" + sessionClientVersion(userAgent, "PostmanRuntime/")
	case strings.Contains(lower, "curl/"):
		client = "curl" + sessionClientVersion(userAgent, "curl/")
	case strings.Contains(lower, "wget/"):
		client = "Wget" + sessionClientVersion(userAgent, "Wget/")
	case strings.Contains(lower, "go-http-client/"):
		client = "Go HTTP Client"
	}

	platform, kind := "未知平台", "desktop"
	switch {
	case strings.Contains(lower, "ipad") || strings.Contains(lower, "tablet"):
		platform, kind = "平板设备", "tablet"
	case strings.Contains(lower, "iphone") || strings.Contains(lower, "ipod"):
		platform, kind = "iPhone / iOS", "mobile"
	case strings.Contains(lower, "android"):
		platform, kind = "Android", "mobile"
	case strings.Contains(lower, "windows"):
		platform = "Windows"
	case strings.Contains(lower, "macintosh") || strings.Contains(lower, "mac os x"):
		platform = "macOS"
	case strings.Contains(lower, "cros"):
		platform = "ChromeOS"
	case strings.Contains(lower, "linux"):
		platform = "Linux"
	}
	if strings.Contains(lower, "bot") || strings.Contains(lower, "crawler") || strings.Contains(lower, "spider") {
		kind = "bot"
		platform = "自动化客户端"
	} else if strings.Contains(lower, "curl/") || strings.Contains(lower, "wget/") || strings.Contains(lower, "postmanruntime/") || strings.Contains(lower, "go-http-client/") {
		kind = "terminal"
	}
	return client, platform, kind
}

func sessionClientVersion(userAgent, marker string) string {
	index := strings.Index(strings.ToLower(userAgent), strings.ToLower(marker))
	if index < 0 {
		return ""
	}
	start := index + len(marker)
	end := start
	for end < len(userAgent) {
		character := userAgent[end]
		if (character < '0' || character > '9') && character != '.' {
			break
		}
		end++
	}
	if end == start {
		return ""
	}
	version := userAgent[start:end]
	if dot := strings.IndexByte(version, '.'); dot > 0 {
		version = version[:dot]
	}
	return " " + version
}

func sessionFingerprint(id string) string {
	if len(id) <= 18 {
		return id
	}
	return id[:10] + "…" + id[len(id)-6:]
}

func sessionUserInitial(displayName, username string) string {
	value := strings.TrimSpace(displayName)
	if value == "" {
		value = strings.TrimSpace(username)
	}
	if value == "" {
		return "?"
	}
	runes := []rune(value)
	return strings.ToUpper(string(runes[0]))
}

func sessionAgo(now, value time.Time) string {
	duration := now.Sub(value)
	if duration <= 90*time.Second {
		return "刚刚活动"
	}
	return sessionDurationLabel(duration) + "前"
}

func sessionUntil(now, value time.Time) string {
	duration := value.Sub(now)
	if duration <= 0 {
		return "即将失效"
	}
	if duration < time.Minute {
		return "不足 1 分钟"
	}
	return "还剩 " + sessionDurationLabel(duration)
}

func sessionDurationLabel(duration time.Duration) string {
	if duration < 0 {
		duration = -duration
	}
	duration = duration.Round(time.Minute)
	if duration < time.Minute {
		return "不足 1 分钟"
	}
	if duration < time.Hour {
		return fmt.Sprintf("%d 分钟", int(duration/time.Minute))
	}
	if duration < 24*time.Hour {
		hours := int(duration / time.Hour)
		minutes := int((duration % time.Hour) / time.Minute)
		if minutes == 0 {
			return fmt.Sprintf("%d 小时", hours)
		}
		return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
	}
	days := int(duration / (24 * time.Hour))
	hours := int((duration % (24 * time.Hour)) / time.Hour)
	if hours == 0 {
		return fmt.Sprintf("%d 天", days)
	}
	return fmt.Sprintf("%d 天 %d 小时", days, hours)
}

func sessionIdleTone(remaining time.Duration) string {
	switch {
	case remaining <= 10*time.Minute:
		return "urgent"
	case remaining <= 30*time.Minute:
		return "watch"
	default:
		return "stable"
	}
}

func configuredSessionDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}
