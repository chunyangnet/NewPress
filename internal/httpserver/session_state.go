package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"newpress.local/newpress/internal/auth"
)

type sessionStateKey struct{}

// Leave room for cookies from older paths without allowing a request to fan
// out into an unbounded number of storage reads or durable revocations.
const maxBrowserSessionCookies = 4

func browserSessionTokens(r *http.Request) ([]string, bool) {
	cookies := r.CookiesNamed(auth.CookieName)
	if len(cookies) > maxBrowserSessionCookies {
		return nil, false
	}
	var tokens []string
	for _, cookie := range cookies {
		if !slices.Contains(tokens, cookie.Value) {
			tokens = append(tokens, cookie.Value)
		}
	}
	return tokens, true
}

type requestSessionState struct {
	principal auth.Principal
	token     string
	ok        bool
}

// Resolve at the request boundary for ordinary view helpers. Explicit security
// rechecks refresh this snapshot after sensitive storage operations. An outage
// must never downgrade an authenticated visitor or erase a valid browser cookie.
func (s *Server) sessionState(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" ||
			strings.HasPrefix(r.URL.Path, "/core-assets/") || strings.HasPrefix(r.URL.Path, "/theme-assets/") {
			next.ServeHTTP(w, r)
			return
		}
		current, ok := s.freshSessionRequest(w, r)
		if !ok {
			return
		}
		next.ServeHTTP(w, current)
	})
}

// freshSessionRequest deliberately bypasses the cached request principal. Use it
// at a security barrier that promises to notice revocation during a storage read.
func (s *Server) freshSessionRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	state := requestSessionState{}
	if previous, cached := r.Context().Value(sessionStateKey{}).(requestSessionState); cached {
		principal, err := s.originalSessionPrincipal(r.Context(), r)
		if errors.Is(err, auth.ErrSessionForbidden) {
			s.writeAPIError(w, r, http.StatusForbidden, "auth.identity_changed", "当前登录身份已变化，请重新发起请求", nil)
			return nil, false
		}
		if err != nil {
			s.sessionStorageError(w, r)
			return nil, false
		}
		if principal.Username != "" {
			state = requestSessionState{principal: principal, token: previous.token, ok: true}
		}
		return r.WithContext(context.WithValue(r.Context(), sessionStateKey{}, state)), true
	}
	if s.auth.Configured() {
		tokens, ok := browserSessionTokens(r)
		if !ok {
			s.sessionCookieError(w, r)
			return nil, false
		}
		for _, token := range tokens {
			principal, err := s.auth.ResolveSession(r.Context(), token)
			if errors.Is(err, auth.ErrSessionNotFound) {
				continue
			}
			if err != nil {
				s.sessionStorageError(w, r)
				return nil, false
			}
			state = requestSessionState{principal: principal, token: token, ok: true}
			break
		}
	}
	return r.WithContext(context.WithValue(r.Context(), sessionStateKey{}, state)), true
}

func (s *Server) sessionCookieError(w http.ResponseWriter, r *http.Request) {
	s.writeAPIError(w, r, http.StatusBadRequest, "auth.too_many_session_cookies", "登录 Cookie 过多，请清除本站 Cookie 后重试", nil)
}

func (s *Server) sessionStorageError(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "5")
	// Never log a record, cookie, CSRF value, or driver error that could contain
	// private authentication state. Request ID links this to operational logs.
	slog.Warn("session storage unavailable", "request_id", r.Header.Get("X-Request-ID"))
	if strings.HasPrefix(r.URL.Path, "/api/") {
		s.writeAPIError(w, r, http.StatusServiceUnavailable, "auth.session_unavailable", "登录服务暂时不可用，请稍后重试", nil)
		return
	}
	http.Error(w, "登录服务暂时不可用，请稍后重试", http.StatusServiceUnavailable)
}

func (s *Server) revokeBrowserSessions(w http.ResponseWriter, r *http.Request) bool {
	tokens, ok := browserSessionTokens(r)
	if !ok {
		s.sessionCookieError(w, r)
		return false
	}
	for _, token := range tokens {
		if err := s.auth.RevokeSessionContext(r.Context(), token); err != nil {
			s.sessionStorageError(w, r)
			return false
		}
	}
	return true
}

// Replace the old browser session before issuing its successor cookie. If
// revocation fails, do not leave a successful but undisclosed new session.
func (s *Server) replaceBrowserSession(w http.ResponseWriter, r *http.Request, token string) bool {
	if !s.revokeBrowserSessions(w, r) {
		_ = s.auth.RevokeSessionContext(context.WithoutCancel(r.Context()), token)
		return false
	}
	s.setAdminCookie(w, token)
	return true
}
