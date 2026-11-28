package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
)

var errCommentWriteDisabled = errors.New("comment moderation writes disabled")
var errCommentModuleDisabled = errors.New("comment module disabled")

// Refresh the original session, moderation permission and module switch at
// operation and response boundaries, including requests already in flight.
func (s *Server) commentPrincipal(r *http.Request, write bool) (auth.Principal, error) {
	if err := r.Context().Err(); err != nil {
		return auth.Principal{}, err
	}
	if !s.auth.Configured() {
		if write {
			return auth.Principal{}, errCommentWriteDisabled
		}
		if s.isLoopbackRequest(r) {
			if !s.moduleEnabled("comments") {
				return auth.Principal{}, errCommentModuleDisabled
			}
			return auth.Principal{}, nil
		}
		return auth.Principal{}, auth.ErrSessionNotFound
	}
	p, err := s.originalSessionPrincipal(r.Context(), r)
	if err != nil {
		return auth.Principal{}, err
	}
	if p.Username == "" {
		return p, auth.ErrSessionNotFound
	}
	if !p.Can(auth.CapabilityAdminAccess) || !p.Can(auth.CapabilityCommentsModerate) {
		return p, auth.ErrSessionForbidden
	}
	if !s.moduleEnabled("comments") {
		return p, errCommentModuleDisabled
	}
	return p, r.Context().Err()
}

func (s *Server) commentAccess(r *http.Request) error {
	_, err := s.commentPrincipal(r, false)
	return err
}

func (s *Server) recheckCommentAccess(w http.ResponseWriter, r *http.Request) bool {
	if err := s.commentAccess(r); err != nil {
		s.commentAccessError(w, r, err)
		return false
	}
	return true
}

func (s *Server) commentOperationAccess(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		s.commentAccessError(w, r, err)
		return false
	}
	return s.recheckCommentAccess(w, r)
}

func (s *Server) refreshCommentWrite(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, err := s.commentPrincipal(r, true)
	if err != nil {
		s.commentAccessError(w, r, err)
		return auth.Principal{}, false
	}
	api := strings.HasPrefix(r.URL.Path, "/api/")
	if !s.sameOriginPublicWrite(r) {
		if api {
			s.writeAPIError(w, r, 403, "auth.cross_site_request_rejected", "写请求必须携带可验证的同源 Origin 或 Referer", nil)
		} else {
			writeJSON(w, 403, map[string]string{"error": "cross_site_request_rejected"})
		}
		return auth.Principal{}, false
	}
	tokens := r.Header.Values("X-CSRF-Token")
	if !api {
		tokens = r.PostForm["csrf_token"]
	}
	if len(tokens) != 1 || !s.auth.CheckCSRF(p, tokens[0]) {
		if api {
			s.writeAPIError(w, r, 403, "auth.csrf_validation_failed", "CSRF 校验失败", nil)
		} else {
			writeJSON(w, 403, map[string]string{"error": "csrf_validation_failed"})
		}
		return auth.Principal{}, false
	}
	return p, true
}

func (s *Server) commentAccessError(w http.ResponseWriter, r *http.Request, err error) {
	for _, name := range []string{"ETag", "Last-Modified", "Location", "Content-Length"} {
		w.Header().Del(name)
	}
	w.Header().Set("Cache-Control", "no-store")
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		panic(http.ErrAbortHandler)
	}
	if errors.Is(err, auth.ErrSessionUnavailable) {
		s.sessionStorageError(w, r)
		return
	}
	api := strings.HasPrefix(r.URL.Path, "/api/")
	status, code, message := 503, "comments.unavailable", "评论服务暂时不可用，请稍后重试"
	var details any
	switch {
	case errors.Is(err, auth.ErrSessionNotFound):
		if !api {
			http.Redirect(w, r, "/admin/login/?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		w.Header().Set("WWW-Authenticate", `NewPressSession realm="admin"`)
		status, code, message = 401, "auth.authentication_required", "需要有效的管理员会话"
	case errors.Is(err, auth.ErrSessionForbidden):
		status, code, message = 403, "auth.capability_required", "当前账号没有评论审核权限"
	case errors.Is(err, errCommentWriteDisabled):
		status, code, message = 403, "auth.write_disabled", "管理员凭据未配置，当前仅允许本机只读访问"
	case errors.Is(err, errCommentModuleDisabled):
		status, code, message = 404, "module.disabled", "评论功能已暂停"
		details = map[string]string{"module": "comments"}
	case errors.Is(err, comment.ErrNotFound):
		status, code, message = 404, "comments.not_found", "评论没有找到"
	}
	if api {
		s.writeAPIError(w, r, status, code, message, details)
	} else {
		s.renderError(w, status, message)
	}
}

func (s *Server) writeCommentJSON(w http.ResponseWriter, r *http.Request, status int, value any, headers func()) {
	writePrivateJSON(w, r, status, value, headers, func() error { return s.commentAccess(r) }, func(err error) { s.commentAccessError(w, r, err) })
}

func (s *Server) writeCommentVersionError(w http.ResponseWriter, r *http.Request, status int, code, message string, details any, headers func()) {
	s.writeCommentJSON(w, r, status, apiErrorEnvelope{Error: apiErrorBody{Code: code, Message: message, RequestID: r.Header.Get("X-Request-ID"), Details: details}}, headers)
}
