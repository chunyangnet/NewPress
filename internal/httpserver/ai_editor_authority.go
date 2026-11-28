package httpserver

import (
	"context"
	"errors"
	"net/http"

	"newpress.local/newpress/internal/auth"
)

var (
	errAIEditorToolInactive = errors.New("AI editor tool lease inactive")
	errAIEditorToolOrigin   = errors.New("AI editor tool origin changed")
	errAIEditorToolCSRF     = errors.New("AI editor tool CSRF validation failed")
)

// Interactive tools retain the session chosen on entry. The account checks
// used for background jobs alone cannot detect logout of this browser session.
func (s *Server) aiEditorToolAccess(r *http.Request, lease aiProMaxAccess) error {
	p, err := s.contentPrincipal(r, true, false)
	if err != nil {
		return err
	}
	if !s.sameOriginPublicWrite(r) {
		return errAIEditorToolOrigin
	}
	tokens := r.Header.Values("X-CSRF-Token")
	if len(tokens) != 1 || !s.auth.CheckCSRF(p, tokens[0]) {
		return errAIEditorToolCSRF
	}
	if !lease.active() {
		return errAIEditorToolInactive
	}
	return r.Context().Err()
}

func (s *Server) recheckAIEditorTool(w http.ResponseWriter, r *http.Request, lease aiProMaxAccess) bool {
	if err := s.aiEditorToolAccess(r, lease); err != nil {
		s.aiEditorToolError(w, r, err)
		return false
	}
	return true
}

func (s *Server) aiEditorToolError(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Cache-Control", "no-store")
	for _, name := range []string{"Content-Length", "ETag", "Last-Modified", "Location"} {
		w.Header().Del(name)
	}
	if r.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	switch {
	case errors.Is(err, errAIEditorToolInactive):
		s.writeAPIError(w, r, 409, "ai.inactive", "插件或 AI 模块已停用，操作已中止", nil)
	case errors.Is(err, errAIEditorToolOrigin):
		s.writeAPIError(w, r, 403, "auth.cross_site_request_rejected", "请从本站编辑器操作", nil)
	case errors.Is(err, errAIEditorToolCSRF):
		s.writeAPIError(w, r, 403, "auth.csrf_validation_failed", "CSRF 校验失败", nil)
	case errors.Is(err, auth.ErrSessionNotFound), errors.Is(err, auth.ErrSessionForbidden), errors.Is(err, auth.ErrSessionUnavailable), errors.Is(err, errContentWriteDisabled):
		s.contentAccessError(w, r, err, true)
	case errors.Is(err, context.Canceled):
		panic(http.ErrAbortHandler)
	default:
		s.aiEditorError(w, r, err)
	}
}
