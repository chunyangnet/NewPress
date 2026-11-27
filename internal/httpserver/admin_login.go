package httpserver

import (
	"errors"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
)

func (s *Server) admin(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/admin/" {
		s.renderError(writer, http.StatusNotFound, "后台页面没有找到")
		return
	}
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityDashboardRead)
	if !ok {
		return
	}
	notice, err := dashboardNoticeFromQuery(request.URL.RawQuery)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "控制台参数不正确")
		return
	}
	s.renderAdminDashboard(writer, request, http.StatusOK, principal, readOnly, adminQuickDraftView{}, notice)
}

func (s *Server) adminLogin(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.URL.Path != "/admin/login/" || !s.auth.Configured() {
		http.NotFound(writer, request)
		return
	}
	if principal, ok := s.currentAdmin(request); ok {
		if principal.Can(auth.CapabilityAdminAccess) {
			http.Redirect(writer, request, safeAdminNext(request.URL.Query().Get("next")), http.StatusSeeOther)
			return
		}
		if !s.revokeBrowserSessions(writer, request) {
			return
		}
		s.clearAdminCookie(writer)
	}
	data := s.baseViewFor(writer, "管理员登录", "登录 NewPress 管理后台", "/admin/login/")
	data.BodyClass = "is-admin-login"
	data.IsAdmin = true
	data.Admin = adminView{
		Current: "login", Configured: true, Next: safeAdminNext(request.URL.Query().Get("next")),
	}
	s.render(writer, http.StatusOK, "admin-login", data)
}

func (s *Server) adminLoginSubmit(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !s.auth.Configured() {
		http.NotFound(writer, request)
		return
	}
	if !s.sameOriginRequest(request) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "cross_site_request_rejected"})
		return
	}
	if !s.parseAdminAuthForm(writer, request, map[string]bool{"username": true, "password": true, "next": true, "verification_token": true}) {
		return
	}
	if len(request.PostForm["username"]) != 1 || len(request.PostForm["password"]) != 1 {
		s.renderAdminLoginError(writer, request, http.StatusBadRequest, "请输入账号与密码")
		return
	}
	attemptKey := s.clientIPs.string(request)
	if err := s.checkVerification(request, "admin_login", request.PostForm.Get("verification_token")); err != nil {
		s.renderAdminLoginError(writer, request, http.StatusUnprocessableEntity, err.Error())
		return
	}
	token, _, err := s.auth.AuthenticateWithMetadataContext(
		request.Context(), attemptKey, request.PostForm.Get("username"), request.PostForm.Get("password"),
		auth.SessionMetadata{IPAddress: attemptKey, UserAgent: request.UserAgent()},
	)
	if err != nil {
		if errors.Is(err, auth.ErrSessionUnavailable) {
			s.sessionStorageError(writer, request)
			return
		}
		status := http.StatusUnauthorized
		message := "用户名或密码不正确"
		if errors.Is(err, auth.ErrPasswordBusy) {
			status = http.StatusTooManyRequests
			message = "登录请求较多，请稍后重试"
			writer.Header().Set("Retry-After", "1")
		} else if errors.Is(err, auth.ErrRateLimited) {
			status = http.StatusTooManyRequests
			message = "尝试次数过多，请在 15 分钟后重试"
			writer.Header().Set("Retry-After", "900")
		}
		s.renderAdminLoginError(writer, request, status, message)
		return
	}
	if !s.replaceBrowserSession(writer, request, token) {
		return
	}
	http.Redirect(writer, request, safeAdminNext(request.PostForm.Get("next")), http.StatusSeeOther)
}

func (s *Server) renderAdminLoginError(writer http.ResponseWriter, request *http.Request, status int, message string) {
	data := s.baseViewFor(writer, "管理员登录", "登录 NewPress 管理后台", "/admin/login/")
	data.BodyClass = "is-admin-login"
	data.IsAdmin = true
	data.Admin = adminView{
		Current: "login", Configured: true, Next: safeAdminNext(request.PostForm.Get("next")),
		Notice: message, NoticeTone: "error",
	}
	s.render(writer, status, "admin-login", data)
}

func (s *Server) adminLogout(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !s.auth.Configured() {
		http.NotFound(writer, request)
		return
	}
	principal, ok := s.currentAdmin(request)
	if !ok {
		http.Redirect(writer, request, "/admin/login/", http.StatusSeeOther)
		return
	}
	if !s.sameOriginRequest(request) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "cross_site_request_rejected"})
		return
	}
	if !s.parseAdminAuthForm(writer, request, map[string]bool{"csrf_token": true}) {
		return
	}
	if len(request.PostForm["csrf_token"]) != 1 || !s.auth.CheckCSRF(principal, request.PostForm.Get("csrf_token")) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return
	}
	if !s.revokeBrowserSessions(writer, request) {
		return
	}
	s.clearAdminCookie(writer)
	http.Redirect(writer, request, "/admin/login/", http.StatusSeeOther)
}

func (s *Server) parseAdminAuthForm(writer http.ResponseWriter, request *http.Request, allowed map[string]bool) bool {
	// Credentials and CSRF tokens must never come from a URL, where proxies,
	// browser history and referrers can retain them. Reject parameter ambiguity.
	if request.URL.RawQuery != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "auth_query_rejected"})
		return false
	}
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "form_utf8_required"})
		return false
	}
	kind, params, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || kind != "application/x-www-form-urlencoded" || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "form_utf8_required"})
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 32<<10)
	if err := request.ParseForm(); err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(writer, status, map[string]string{"error": "invalid_auth_form"})
		return false
	}
	for key, values := range request.PostForm {
		if !allowed[key] || len(values) != 1 || !utf8.ValidString(values[0]) {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_auth_form"})
			return false
		}
	}
	return true
}
