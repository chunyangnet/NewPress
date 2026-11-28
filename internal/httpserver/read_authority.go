package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/feedback"
)

// Revalidate only the session selected at the request boundary. Cookie fallback
// is useful on entry, but cannot take over an already-authorized request.
func (s *Server) originalSessionPrincipal(ctx context.Context, r *http.Request) (auth.Principal, error) {
	if err := ctx.Err(); err != nil {
		return auth.Principal{}, err
	}
	previous, token, hadSession := s.currentAdminSession(r)
	if !hadSession {
		return auth.Principal{}, nil
	}
	current, err := s.auth.ResolveSession(ctx, token)
	if errors.Is(err, auth.ErrSessionNotFound) {
		return auth.Principal{}, nil
	}
	if err != nil {
		return auth.Principal{}, err
	}
	if current.UserID != previous.UserID || current.Username != previous.Username {
		return auth.Principal{}, auth.ErrSessionForbidden
	}
	return current, ctx.Err()
}

func (s *Server) recheckAdminRead(w http.ResponseWriter, r *http.Request, capability string) (*http.Request, bool) {
	r, ok := s.freshSessionRequest(w, r)
	if !ok {
		return nil, false
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		_, _, ok = s.requireAdminJSONReadCapability(w, r, capability)
	} else {
		_, _, ok = s.requireAdminPageCapability(w, r, capability)
	}
	return r, ok
}

func (s *Server) adminReadAccess(r *http.Request, capability string) error {
	if err := r.Context().Err(); err != nil {
		return err
	}
	if !s.auth.Configured() {
		if s.isLoopbackRequest(r) {
			return nil
		}
		return auth.ErrSessionNotFound
	}
	p, err := s.originalSessionPrincipal(r.Context(), r)
	if err != nil {
		return err
	}
	if p.Username == "" {
		return auth.ErrSessionNotFound
	}
	if !p.Can(auth.CapabilityAdminAccess) || !p.Can(capability) {
		return auth.ErrSessionForbidden
	}
	return nil
}

// Export authorization needs only board settings or circle ownership. Header
// lists avoid copying every post, reply and attachment at each stream check.
func (s *Server) circleExportAccess(r *http.Request, slug string) error {
	headers, err := s.circles.List(r.Context())
	if err != nil {
		return err
	}
	p, err := s.originalSessionPrincipal(r.Context(), r)
	if err != nil {
		return err
	}
	if p.UserID <= 0 {
		return auth.ErrSessionNotFound
	}
	for _, c := range headers {
		if c.Slug == slug {
			if community.Owns(c, community.Actor{ID: p.UserID, Admin: p.Can(auth.CapabilitySettingsManage)}) {
				return nil
			}
			return community.ErrForbidden
		}
	}
	return community.ErrNotFound
}

func (s *Server) feedbackExportAccess(r *http.Request, slug string) error {
	headers, err := s.feedback.List(r.Context())
	if err != nil {
		return err
	}
	p, err := s.originalSessionPrincipal(r.Context(), r)
	if err != nil {
		return err
	}
	if p.UserID <= 0 {
		return auth.ErrSessionNotFound
	}
	for _, b := range headers {
		if b.Slug == slug {
			if feedback.Can(b, feedbackActor(p), "moderate") {
				return nil
			}
			return feedback.ErrForbidden
		}
	}
	return feedback.ErrNotFound
}

func (s *Server) privateReadAuthError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, auth.ErrSessionNotFound):
		s.writeAPIError(w, r, http.StatusUnauthorized, "auth.authentication_required", "请先登录后继续", nil)
	case errors.Is(err, auth.ErrSessionForbidden):
		s.writeAPIError(w, r, http.StatusForbidden, "auth.identity_changed", "当前登录身份已变化，请重新发起请求", nil)
	case errors.Is(err, auth.ErrSessionUnavailable):
		s.sessionStorageError(w, r)
	default:
		return false
	}
	return true
}
