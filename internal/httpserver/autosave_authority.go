package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

var errPreviewTokenExpired = errors.New("preview token expired")

func (s *Server) autosaveReadState(r *http.Request, id int64, owner string) (content.AutosaveState, error) {
	state, readErr := content.ReadAutosaveState(r.Context(), s.repository, id, owner)
	// Storage may block. Resolve the original session after the metadata read.
	p, err := s.originalSessionPrincipal(r.Context(), r)
	if err != nil {
		return content.AutosaveState{}, err
	}
	if p.Username == "" {
		return content.AutosaveState{}, auth.ErrSessionNotFound
	}
	key, ok := autosaveOwnerKey(p)
	if !ok || key != owner || !p.Can(auth.CapabilityAdminAccess) || !principalCanAny(p, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn) {
		return content.AutosaveState{}, auth.ErrSessionForbidden
	}
	if readErr != nil {
		return content.AutosaveState{}, readErr
	}
	if !canWriteContentObject(p, content.Post{ID: state.PostID, AuthorID: state.AuthorID, Type: state.Type}) {
		return content.AutosaveState{}, auth.ErrSessionForbidden
	}
	if state.PostID != id || state.PostVersion != state.SourceVersion || !state.ExpiresAt.After(time.Now()) {
		return content.AutosaveState{}, content.ErrAutosaveNotFound
	}
	return state, nil
}

func (s *Server) contentPreviewAccess(r *http.Request, claims previewTokenClaims, owner string) error {
	state, err := s.autosaveReadState(r, claims.PostID, owner)
	if err != nil {
		return err
	}
	if claims.ExpiresUnix <= time.Now().Unix() {
		return errPreviewTokenExpired
	}
	if state.SourceVersion != claims.SourceVersion || state.UpdatedAt.UnixNano() != claims.AutosaveUpdated {
		return content.ErrAutosaveNotFound
	}
	return nil
}

func (s *Server) autosaveReadError(w http.ResponseWriter, r *http.Request, err error, page bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		panic(http.ErrAbortHandler)
	}
	if errors.Is(err, auth.ErrSessionUnavailable) {
		s.sessionStorageError(w, r)
		return
	}
	status, code, message := http.StatusServiceUnavailable, "autosave_unavailable", "恢复点暂时不可用，请稍后重试"
	switch {
	case errors.Is(err, auth.ErrSessionNotFound):
		if page {
			http.Redirect(w, r, "/admin/login/?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		w.Header().Set("WWW-Authenticate", `NewPressSession realm="admin"`)
		status, code = http.StatusUnauthorized, "authentication_required"
	case errors.Is(err, auth.ErrSessionForbidden):
		status, code, message = http.StatusForbidden, "content_object_forbidden", "当前账号没有访问此恢复点的权限"
	case errors.Is(err, errPreviewTokenExpired):
		status, code, message = http.StatusForbidden, "preview_expired", "预览链接无效或已经过期"
	case errors.Is(err, content.ErrNotFound), errors.Is(err, content.ErrAutosaveNotFound):
		status, code, message = http.StatusGone, "autosave_changed", "恢复点已更新、放弃或正式保存，请从编辑器重新打开预览"
	}
	if page {
		s.renderPreviewError(w, status, message)
	} else {
		writeJSON(w, status, map[string]string{"error": code})
	}
}

func (s *Server) refreshAutosaveWrite(w http.ResponseWriter, r *http.Request) (*http.Request, auth.Principal, bool) {
	r, ok := s.freshSessionRequest(w, r)
	if !ok {
		return nil, auth.Principal{}, false
	}
	p, ok := s.requireAutosavePagePrincipal(w, r)
	if !ok {
		return nil, auth.Principal{}, false
	}
	if len(r.PostForm["csrf_token"]) != 1 || !s.auth.CheckCSRF(p, r.PostForm.Get("csrf_token")) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return nil, auth.Principal{}, false
	}
	return r, p, true
}

func (s *Server) recheckAutosaveMutation(w http.ResponseWriter, r *http.Request, original content.Post) bool {
	post, readErr := s.repository.GetByID(r.Context(), original.ID)
	r, p, ok := s.refreshAutosaveWrite(w, r)
	if !ok {
		return false
	}
	if readErr != nil {
		s.autosaveReadError(w, r, readErr, false)
		return false
	}
	if !canWriteContentObject(p, post) {
		s.autosaveReadError(w, r, auth.ErrSessionForbidden, false)
		return false
	}
	if post.Version != original.Version || post.AuthorID != original.AuthorID {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "content_version_conflict"})
		return false
	}
	return true
}
