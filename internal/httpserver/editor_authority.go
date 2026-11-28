package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

// Refresh only the session chosen when the request entered. Body reads and
// storage calls can outlive that session or a role change.
func (s *Server) refreshContentWrite(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	if r.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	p, err := s.originalSessionPrincipal(r.Context(), r)
	if err != nil {
		s.editorAccessError(w, r, err)
		return auth.Principal{}, false
	}
	if !s.auth.Configured() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "content_write_forbidden"})
		return auth.Principal{}, false
	}
	if p.Username == "" {
		s.editorAccessError(w, r, auth.ErrSessionNotFound)
		return auth.Principal{}, false
	}
	if !p.Can(auth.CapabilityAdminAccess) || !canCreateContent(p) {
		s.editorAccessError(w, r, auth.ErrSessionForbidden)
		return auth.Principal{}, false
	}
	if len(r.PostForm["csrf_token"]) != 1 || !s.auth.CheckCSRF(p, r.PostForm.Get("csrf_token")) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return auth.Principal{}, false
	}
	return p, true
}

func (s *Server) recheckContentWrite(w http.ResponseWriter, r *http.Request, id int64) bool {
	var state content.PostState
	var readErr error
	if id > 0 {
		state, readErr = content.ReadPostState(r.Context(), s.repository, id)
	}
	p, ok := s.refreshContentWrite(w, r)
	if !ok {
		return false
	}
	if readErr != nil {
		s.editorAccessError(w, r, readErr)
		return false
	}
	if id > 0 && !canWriteContentObject(p, content.Post{ID: state.ID, AuthorID: state.AuthorID, Type: state.Type}) {
		s.editorAccessError(w, r, auth.ErrSessionForbidden)
		return false
	}
	return true
}

func (s *Server) editorReadAccess(r *http.Request, id int64, write bool, recovery *content.Autosave) error {
	if recovery != nil {
		state, err := s.autosaveReadState(r, id, recovery.OwnerKey)
		if err != nil {
			return err
		}
		if state.SourceVersion != recovery.SourceVersion || !state.UpdatedAt.Equal(recovery.UpdatedAt) {
			return content.ErrAutosaveNotFound
		}
		return nil
	}
	var state content.PostState
	var readErr error
	if id > 0 {
		state, readErr = content.ReadPostState(r.Context(), s.repository, id)
	}
	if err := r.Context().Err(); err != nil {
		return err
	}
	if !s.auth.Configured() && s.isLoopbackRequest(r) {
		return readErr
	}
	p, err := s.originalSessionPrincipal(r.Context(), r)
	if err != nil {
		return err
	}
	if p.Username == "" {
		return auth.ErrSessionNotFound
	}
	if !p.Can(auth.CapabilityAdminAccess) || (write && !canCreateContent(p)) || (!write && !p.Can(auth.CapabilityContentRead)) {
		return auth.ErrSessionForbidden
	}
	if readErr != nil {
		return readErr
	}
	post := content.Post{ID: state.ID, AuthorID: state.AuthorID, Type: state.Type}
	if id > 0 && (!canReadContentObject(p, post) || (write && !canWriteContentObject(p, post))) {
		return auth.ErrSessionForbidden
	}
	return nil
}

func (s *Server) editorAccessError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		panic(http.ErrAbortHandler)
	case errors.Is(err, auth.ErrSessionNotFound):
		http.Redirect(w, r, "/admin/login/?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	case errors.Is(err, auth.ErrSessionForbidden):
		s.renderError(w, http.StatusForbidden, "当前账号没有访问这篇内容的权限")
	case errors.Is(err, auth.ErrSessionUnavailable):
		s.sessionStorageError(w, r)
	case errors.Is(err, content.ErrAutosaveNotFound):
		s.renderError(w, http.StatusGone, "恢复点已更新、放弃或正式保存，请重新打开编辑器")
	case errors.Is(err, content.ErrNotFound):
		s.renderError(w, http.StatusNotFound, "内容没有找到")
	default:
		s.renderError(w, http.StatusServiceUnavailable, "内容暂时不可用，请稍后重试")
	}
}
