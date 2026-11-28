package httpserver

import (
	"context"
	"errors"
	"net/http"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

var (
	errContentScope         = errors.New("content author scope changed")
	errContentAPIDisabled   = errors.New("content API disabled")
	errContentWriteDisabled = errors.New("content writes disabled")
)

func (s *Server) contentPrincipal(r *http.Request, write, api bool) (auth.Principal, error) {
	if err := r.Context().Err(); err != nil {
		return auth.Principal{}, err
	}
	var p auth.Principal
	var err error
	if !s.auth.Configured() {
		if !s.isLoopbackRequest(r) {
			return p, auth.ErrSessionNotFound
		}
		if write {
			return p, errContentWriteDisabled
		}
	} else {
		p, err = s.originalSessionPrincipal(r.Context(), r)
		if err != nil {
			return auth.Principal{}, err
		}
		if p.Username == "" {
			return p, auth.ErrSessionNotFound
		}
		if !p.Can(auth.CapabilityAdminAccess) || (write && !canCreateContent(p)) || (!write && !p.Can(auth.CapabilityContentRead)) {
			return p, auth.ErrSessionForbidden
		}
	}
	if api && !s.siteSettings.Snapshot().Settings.API.AdminContentEnabled {
		return p, errContentAPIDisabled
	}
	return p, r.Context().Err()
}

func (s *Server) contentObjectAccess(r *http.Request, id int64, write, api bool) error {
	state, readErr := content.ReadPostState(r.Context(), s.repository, id)
	p, err := s.contentPrincipal(r, write, api)
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	post := content.Post{ID: state.ID, AuthorID: state.AuthorID, Type: state.Type}
	if (write && !canWriteContentObject(p, post)) || (!write && !canReadContentObject(p, post)) {
		return errContentScope
	}
	return nil
}

// Counts and pagination have the same author scope as the original query. A
// narrowed scope cannot authorize an already-prepared unfiltered result, even
// when the visible page is empty or happens to contain only the author's posts.
func (s *Server) contentListAccess(r *http.Request, scope int64, posts []content.Post, api bool) error {
	var readErr error
	if scope > 0 {
		ids := make([]int64, len(posts))
		for i, post := range posts {
			ids[i] = post.ID
		}
		var states map[int64]content.PostState
		states, readErr = content.ReadPostStates(r.Context(), s.repository, ids)
		for _, state := range states {
			if state.AuthorID != scope {
				readErr = errContentScope
				break
			}
		}
	}
	p, err := s.contentPrincipal(r, false, api)
	if err != nil {
		return err
	}
	if current := contentAuthorScope(p); current != 0 && current != scope {
		return errContentScope
	}
	return readErr
}

func (s *Server) contentAccessError(w http.ResponseWriter, r *http.Request, err error, api bool) {
	// Validators and success headers must never survive a failed final check.
	for _, name := range []string{"ETag", "Last-Modified", "Location", "Idempotency-Replayed", "Content-Length"} {
		w.Header().Del(name)
	}
	if !api {
		if errors.Is(err, errContentScope) {
			err = auth.ErrSessionForbidden
		}
		s.editorAccessError(w, r, err)
		return
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		panic(http.ErrAbortHandler)
	case errors.Is(err, auth.ErrSessionUnavailable):
		s.sessionStorageError(w, r)
	case errors.Is(err, auth.ErrSessionNotFound):
		w.Header().Set("WWW-Authenticate", `NewPressSession realm="admin"`)
		s.writeAPIError(w, r, 401, "auth.authentication_required", "需要有效的管理员会话", nil)
	case errors.Is(err, auth.ErrSessionForbidden):
		s.writeAPIError(w, r, 403, "auth.capability_required", "当前账号没有执行此操作的权限", nil)
	case errors.Is(err, errContentScope):
		s.writeAPIError(w, r, 403, "content.owner_required", "当前账号不能访问其他作者的内容", nil)
	case errors.Is(err, errContentWriteDisabled):
		s.writeAPIError(w, r, 403, "auth.write_disabled", "当前仅允许本机只读访问", nil)
	case errors.Is(err, errContentAPIDisabled):
		s.writeAPIError(w, r, 404, "request.not_found", "资源不存在", nil)
	case errors.Is(err, content.ErrNotFound):
		s.writeAPIError(w, r, 404, "content.not_found", "内容没有找到", nil)
	default:
		s.writeAPIError(w, r, 503, "content.unavailable", "内容暂时不可用，请稍后重试", nil)
	}
}

func (s *Server) recheckContentAPIWrite(w http.ResponseWriter, r *http.Request, id int64) (auth.Principal, bool) {
	var state content.PostState
	var readErr error
	if id > 0 {
		state, readErr = content.ReadPostState(r.Context(), s.repository, id)
	}
	p, err := s.contentPrincipal(r, true, true)
	if err != nil {
		s.contentAccessError(w, r, err, true)
		return auth.Principal{}, false
	}
	if !s.sameOriginRequest(r) {
		s.writeAPIError(w, r, 403, "auth.cross_site_request_rejected", "跨站写请求已拒绝", nil)
		return auth.Principal{}, false
	}
	tokens := r.Header.Values("X-CSRF-Token")
	if len(tokens) != 1 || !s.auth.CheckCSRF(p, tokens[0]) {
		s.writeAPIError(w, r, 403, "auth.csrf_validation_failed", "CSRF 校验失败", nil)
		return auth.Principal{}, false
	}
	if readErr != nil {
		s.contentAccessError(w, r, readErr, true)
		return auth.Principal{}, false
	}
	if id > 0 && !canWriteContentObject(p, content.Post{ID: state.ID, AuthorID: state.AuthorID, Type: state.Type}) {
		s.contentAccessError(w, r, errContentScope, true)
		return auth.Principal{}, false
	}
	return p, true
}

func (s *Server) contentBeforeOutput(w http.ResponseWriter, r *http.Request, api bool, access func() error) func() bool {
	return func() bool {
		if err := access(); err != nil {
			s.contentAccessError(w, r, err, api)
			return false
		}
		return true
	}
}

// Prepare JSON before authorizing output, including time spent in marshaling.
// Set successful metadata only after that check; bounded writes refresh again.
func (s *Server) writeContentJSON(w http.ResponseWriter, r *http.Request, status int, value any, headers func(), access func() error) {
	writePrivateJSON(w, r, status, value, headers, access, func(err error) { s.contentAccessError(w, r, err, true) })
}
