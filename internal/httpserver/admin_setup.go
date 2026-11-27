package httpserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/coreui"
)

const (
	setupRequestLimit = 16 << 10
	setupRateWindow   = 15 * time.Minute
	setupRateMaximum  = 5
	setupRateKeys     = 4096
)

type setupRateEntry struct {
	started time.Time
	count   int
}

type setupRateLimiter struct {
	mu      sync.Mutex
	entries map[string]setupRateEntry
	now     func() time.Time
}

func (limiter *setupRateLimiter) allow(key string) bool {
	if key == "" {
		key = "unknown"
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := time.Now()
	if limiter.now != nil {
		now = limiter.now()
	}
	if limiter.entries == nil {
		limiter.entries = make(map[string]setupRateEntry)
	}
	entry, ok := limiter.entries[key]
	if !ok || now.Sub(entry.started) >= setupRateWindow || now.Before(entry.started) {
		if len(limiter.entries) >= setupRateKeys {
			for existingKey, existing := range limiter.entries {
				if now.Sub(existing.started) >= setupRateWindow || now.Before(existing.started) {
					delete(limiter.entries, existingKey)
				}
			}
			if len(limiter.entries) >= setupRateKeys {
				return false
			}
		}
		limiter.entries[key] = setupRateEntry{started: now, count: 1}
		return true
	}
	if entry.count >= setupRateMaximum {
		return false
	}
	entry.count++
	limiter.entries[key] = entry
	return true
}

func (s *Server) registerSetupRoutes(mux *moduleMux) {
	mux.HandleFunc("GET /install/", s.adminSetupPage)
	mux.HandleFunc("POST /install/", s.adminSetupSubmit)
}

func (s *Server) adminSetupPage(writer http.ResponseWriter, request *http.Request) {
	s.setupSecurityHeaders(writer)
	if request.URL.Path != "/install/" || request.URL.RawQuery != "" || s.config.SetupToken == "" || s.config.DatabaseURL == "" {
		http.NotFound(writer, request)
		return
	}
	available, err := s.auth.FirstAdminSetupAvailable(request.Context())
	if err != nil {
		s.setupUnavailable(writer, request)
		return
	}
	if !available || s.auth.Configured() {
		http.NotFound(writer, request)
		return
	}
	s.renderSetupPage(writer, request, http.StatusOK, "")
}

func (s *Server) adminSetupSubmit(writer http.ResponseWriter, request *http.Request) {
	s.setupSecurityHeaders(writer)
	if request.URL.Path != "/install/" || request.URL.RawQuery != "" || s.config.SetupToken == "" || s.config.DatabaseURL == "" {
		http.NotFound(writer, request)
		return
	}
	if !strictSetupOrigin(request, s.config.PublicURL) {
		s.setupError(writer, http.StatusForbidden, "安装请求无效，请从当前站点重新打开安装页面。")
		return
	}
	if !s.setupRate.allow(s.clientIPs.string(request)) {
		writer.Header().Set("Retry-After", "900")
		s.setupError(writer, http.StatusTooManyRequests, "尝试次数过多，请稍后再试。")
		return
	}
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		s.setupError(writer, http.StatusBadRequest, "安装请求格式不正确，请重试。")
		return
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		s.setupError(writer, http.StatusBadRequest, "安装请求格式不正确，请重试。")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, setupRequestLimit)
	if err := request.ParseForm(); err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		s.setupError(writer, status, "安装请求格式不正确，请重试。")
		return
	}
	if !validSetupForm(request.PostForm) {
		s.setupError(writer, http.StatusBadRequest, "安装请求格式不正确，请重试。")
		return
	}
	available, err := s.auth.FirstAdminSetupAvailable(request.Context())
	if err != nil {
		s.setupUnavailable(writer, request)
		return
	}
	if !available || s.auth.Configured() {
		http.NotFound(writer, request)
		return
	}
	if !setupTokenMatches(s.config.SetupToken, request.PostForm.Get("setup_token")) {
		s.setupError(writer, http.StatusForbidden, "安装凭据无效，请检查后重试。")
		return
	}
	password := request.PostForm.Get("password")
	if password == "" || password != request.PostForm.Get("confirm_password") {
		s.setupError(writer, http.StatusBadRequest, "请填写有效的账号信息，并确认两次输入的密码一致。")
		return
	}
	input := auth.UserInput{
		Username:    request.PostForm.Get("username"),
		Email:       request.PostForm.Get("email"),
		DisplayName: request.PostForm.Get("display_name"),
		Status:      auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"administrator"},
	}
	if _, err := s.auth.ClaimFirstAdministrator(request.Context(), input, password); err != nil {
		if errors.Is(err, auth.ErrFirstAdminSetupClosed) {
			http.NotFound(writer, request)
			return
		}
		if errors.Is(err, auth.ErrPasswordBusy) {
			writer.Header().Set("Retry-After", "1")
			s.setupError(writer, http.StatusTooManyRequests, "当前请求较多，请稍后重试。")
			return
		}
		var validation *auth.UserValidationError
		if errors.As(err, &validation) {
			s.setupError(writer, http.StatusBadRequest, "账号信息或密码不符合要求，请检查后重试。")
			return
		}
		s.setupUnavailable(writer, request)
		return
	}
	http.Redirect(writer, request, "/admin/login/", http.StatusSeeOther)
}

func (s *Server) renderSetupPage(writer http.ResponseWriter, request *http.Request, status int, notice string) {
	data := s.baseViewFor(writer, "首次管理员设置", "创建此站点的第一个管理员账号", "/install/")
	data.BodyClass = "is-admin-login"
	data.IsAdmin = true
	data.Admin = adminView{Current: "login", Configured: false, Notice: notice, NoticeTone: "error"}
	s.render(writer, status, "admin-install", data)
}

func (s *Server) setupUnavailable(writer http.ResponseWriter, request *http.Request) {
	s.setupError(writer, http.StatusServiceUnavailable, "安装服务暂时不可用，请稍后重试。")
}

func (s *Server) setupError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	writer.WriteHeader(status)
	_ = coreui.Error(writer, http.StatusText(status), message)
}

func (s *Server) setupSecurityHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
}

func strictSetupOrigin(request *http.Request, publicURL string) bool {
	if strings.EqualFold(strings.TrimSpace(request.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	originValues := request.Header.Values("Origin")
	if len(originValues) != 1 {
		return false
	}
	origin := strings.TrimSpace(originValues[0])
	if origin == "" {
		return false
	}
	provided, err := url.Parse(origin)
	if err != nil || provided.Host == "" || provided.User != nil || provided.RawQuery != "" || provided.Fragment != "" || (provided.Path != "" && provided.Path != "/") {
		return false
	}
	expected, err := url.Parse(publicURL)
	if err != nil || expected.Host == "" {
		return false
	}
	return strings.EqualFold(provided.Scheme, expected.Scheme) && strings.EqualFold(provided.Host, expected.Host)
}

func setupTokenMatches(expected, provided string) bool {
	expectedDigest := sha256.Sum256([]byte(expected))
	providedDigest := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(expectedDigest[:], providedDigest[:]) == 1
}

func validSetupForm(form url.Values) bool {
	allowed := map[string]struct{}{
		"setup_token": {}, "username": {}, "email": {},
		"display_name": {}, "password": {}, "confirm_password": {},
	}
	if len(form) != len(allowed) {
		return false
	}
	for key, values := range form {
		if _, ok := allowed[key]; !ok || len(values) != 1 {
			return false
		}
	}
	return true
}
