package httpserver

import (
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
)

func (s *Server) currentAdmin(request *http.Request) (auth.Principal, bool) {
	principal, _, ok := s.currentAdminSession(request)
	return principal, ok
}

func (s *Server) currentAdminSession(request *http.Request) (auth.Principal, string, bool) {
	if state, ok := request.Context().Value(sessionStateKey{}).(requestSessionState); ok {
		return state.principal, state.token, state.ok
	}
	if !s.auth.Configured() {
		return auth.Principal{}, "", false
	}
	tokens, ok := browserSessionTokens(request)
	if !ok {
		return auth.Principal{}, "", false
	}
	for _, token := range tokens {
		if principal, ok := s.auth.ValidateSessionContext(request.Context(), token); ok {
			return principal, token, true
		}
	}
	return auth.Principal{}, "", false
}

func (s *Server) requireAdminPage(writer http.ResponseWriter, request *http.Request) (auth.Principal, bool, bool) {
	writer.Header().Set("Cache-Control", "no-store")
	if !s.auth.Configured() {
		if s.isLoopbackRequest(request) {
			return auth.Principal{}, true, true
		}
		http.NotFound(writer, request)
		return auth.Principal{}, false, false
	}
	if principal, ok := s.currentAdmin(request); ok {
		if !principal.Can(auth.CapabilityAdminAccess) {
			s.renderError(writer, http.StatusForbidden, "当前账号没有进入管理后台的权限")
			return auth.Principal{}, false, false
		}
		return principal, false, true
	}
	next := request.URL.RequestURI()
	http.Redirect(writer, request, "/admin/login/?next="+url.QueryEscape(next), http.StatusSeeOther)
	return auth.Principal{}, false, false
}

func (s *Server) requireAdminPageCapability(writer http.ResponseWriter, request *http.Request, capabilities ...string) (auth.Principal, bool, bool) {
	principal, readOnly, ok := s.requireAdminPage(writer, request)
	if !ok || readOnly || principalCanAny(principal, capabilities...) {
		return principal, readOnly, ok
	}
	s.renderError(writer, http.StatusForbidden, "当前账号没有访问这项后台功能的权限")
	return auth.Principal{}, false, false
}

func principalCanAny(principal auth.Principal, capabilities ...string) bool {
	for _, capability := range capabilities {
		if principal.Can(capability) {
			return true
		}
	}
	return false
}

func (s *Server) requireAdminAPI(writer http.ResponseWriter, request *http.Request, capabilities ...string) bool {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Add("Vary", "Cookie")
	if !s.auth.Configured() {
		if s.isLoopbackRequest(request) {
			return true
		}
		s.writeAPIError(writer, request, http.StatusNotFound, "request.not_found", "资源不存在", nil)
		return false
	}
	if principal, ok := s.currentAdmin(request); ok {
		if !principal.Can(auth.CapabilityAdminAccess) {
			s.writeAPIError(writer, request, http.StatusForbidden, "auth.capability_required", "当前账号不能访问管理 API", nil)
			return false
		}
		if len(capabilities) > 0 && !principalCanAny(principal, capabilities...) {
			s.writeAPIError(writer, request, http.StatusForbidden, "auth.capability_required", "当前账号没有访问此资源的权限", map[string]any{"required": capabilities})
			return false
		}
		return true
	}
	writer.Header().Set("WWW-Authenticate", `NewPressSession realm="admin"`)
	s.writeAPIError(writer, request, http.StatusUnauthorized, "auth.authentication_required", "需要有效的管理员会话", nil)
	return false
}

func (s *Server) setAdminCookie(writer http.ResponseWriter, token string) {
	secure := s.secureSessionCookie()
	maxAge := int(s.config.AdminSessionTTL.Seconds())
	if maxAge <= 0 {
		maxAge = int((12 * time.Hour).Seconds())
	}
	http.SetCookie(writer, &http.Cookie{
		Name: auth.CookieName, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearAdminCookie(writer http.ResponseWriter) {
	s.expireAdminCookie(writer, "/")
}

func (s *Server) expireAdminCookie(writer http.ResponseWriter, cookiePath string) {
	http.SetCookie(writer, &http.Cookie{
		Name: auth.CookieName, Value: "", Path: cookiePath, MaxAge: -1,
		Expires: time.Unix(1, 0), HttpOnly: true, Secure: s.secureSessionCookie(),
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) secureSessionCookie() bool {
	u, err := url.Parse(s.config.PublicURL)
	if err != nil || u.Scheme != "http" {
		return true
	}
	if strings.EqualFold(u.Hostname(), "localhost") {
		return false
	}
	a, err := netip.ParseAddr(u.Hostname())
	return err != nil || a.Zone() != "" || !a.IsLoopback()
}

func safeAdminNext(candidate string) string {
	if candidate == "" {
		return "/admin/"
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/admin/") || strings.HasPrefix(parsed.Path, "//") || parsed.Path == "/admin/login/" {
		return "/admin/"
	}
	return parsed.RequestURI()
}

func (s *Server) sameOriginRequest(request *http.Request) bool {
	if request == nil {
		return false
	}
	metadata := request.Header.Values("Sec-Fetch-Site")
	origins := request.Header.Values("Origin")
	referers := request.Header.Values("Referer")
	if len(metadata) > 1 || len(origins) > 1 || len(referers) > 1 {
		return false
	}
	if len(metadata) == 1 {
		switch strings.ToLower(strings.TrimSpace(metadata[0])) {
		case "same-origin", "same-site", "none":
		default:
			return false
		}
	}
	var parsed *url.URL
	if len(origins) == 1 {
		parsed = requestOrigin(origins[0], true)
	} else if len(referers) == 1 {
		parsed = requestOrigin(referers[0], false)
	} else {
		// Non-browser API callers still need the session's independent CSRF
		// token. Public unauthenticated writes require an explicit source.
		return true
	}
	if parsed == nil {
		return false
	}
	expected, err := url.Parse(s.config.PublicURL)
	if err == nil && expected.Host != "" && expected.Scheme != "" {
		return strings.EqualFold(parsed.Scheme, expected.Scheme) && strings.EqualFold(parsed.Host, expected.Host)
	}
	requestScheme := "http"
	if request.TLS != nil {
		requestScheme = "https"
	}
	return strings.EqualFold(parsed.Scheme, requestScheme) && strings.EqualFold(parsed.Host, request.Host)
}

// Origin is an origin, not an arbitrary URL containing a matching host.
// Referer may include a path/query, but never credentials or a fragment.
func requestOrigin(value string, originHeader bool) *url.URL {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		(!strings.EqualFold(parsed.Scheme, "https") && !strings.EqualFold(parsed.Scheme, "http")) ||
		strings.Contains(value, "#") || (originHeader && (parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery)) {
		return nil
	}
	return parsed
}

func (s *Server) isLoopbackRequest(request *http.Request) bool {
	result := s.clientIPs.resolve(request)
	if !result.Reliable || !result.Address.IsLoopback() {
		return false
	}
	// A page on an attacker-controlled domain can rebind its DNS to loopback.
	// The TCP peer alone therefore cannot authorize the credential-free preview.
	host, err := url.Parse("http://" + request.Host)
	if err != nil || host.Host == "" || host.User != nil || host.Path != "" || host.RawQuery != "" || host.ForceQuery || host.Fragment != "" {
		return false
	}
	if strings.EqualFold(host.Hostname(), "localhost") {
		return true
	}
	if address, err := netip.ParseAddr(host.Hostname()); err == nil && address.Zone() == "" && address.IsLoopback() {
		return true
	}
	expected, err := url.Parse(s.config.PublicURL)
	return err == nil && expected.Host != "" && strings.EqualFold(host.Host, expected.Host)
}
