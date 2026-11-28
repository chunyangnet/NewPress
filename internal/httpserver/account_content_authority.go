package httpserver

import (
	"context"
	"errors"
	"net/http"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

var errOwnContentChanged = errors.New("own content changed during read")
var errOwnCommentsDisabled = errors.New("own comments disabled during read")

// Own content remains available to subscribers. Only the same live account,
// rather than an administrator capability, can authorize a prepared response.
func (s *Server) ownContentPrincipal(r *http.Request, userID int64) (auth.Principal, error) {
	if err := r.Context().Err(); err != nil {
		return auth.Principal{}, err
	}
	p, err := s.originalSessionPrincipal(r.Context(), r)
	if err != nil {
		return p, err
	}
	if p.UserID <= 0 || p.Username == "" {
		return p, auth.ErrSessionNotFound
	}
	if p.UserID != userID {
		return p, auth.ErrSessionForbidden
	}
	return p, r.Context().Err()
}

func (s *Server) ownPostsAccess(r *http.Request, initial auth.Principal, posts []content.Post, editLinks bool) error {
	ids := make([]int64, len(posts))
	for i, p := range posts {
		ids[i] = p.ID
	}
	states, readErr := content.ReadPostStates(r.Context(), s.repository, ids)
	p, err := s.ownContentPrincipal(r, initial.UserID)
	if err != nil {
		return err
	}
	if errors.Is(readErr, content.ErrNotFound) {
		return errOwnContentChanged
	}
	if readErr != nil {
		return readErr
	}
	for _, v := range states {
		if v.AuthorID != p.UserID {
			return auth.ErrSessionForbidden
		}
	}
	if editLinks && (!p.Can(auth.CapabilityAdminAccess) || !canCreateContent(p)) {
		return errOwnContentChanged
	}
	return nil
}

func (s *Server) ownCommentsAccess(r *http.Request, userID int64, comments []comment.Comment, visible map[int64]content.PostHeader) error {
	ids := make([]int64, len(comments))
	for i, c := range comments {
		ids[i] = c.ID
	}
	states, readErr := comment.ReadStates(r.Context(), s.comments, ids)
	parentIDs := make([]int64, 0, len(visible))
	for id := range visible {
		parentIDs = append(parentIDs, id)
	}
	var parents map[int64]content.PostHeader
	if readErr == nil {
		parents, readErr = content.ReadPostHeaders(r.Context(), s.repository, parentIDs)
	}
	if _, err := s.ownContentPrincipal(r, userID); err != nil {
		return err
	}
	if !s.moduleEnabled("comments") {
		return errOwnCommentsDisabled
	}
	if errors.Is(readErr, comment.ErrNotFound) {
		return errOwnContentChanged
	}
	if readErr != nil {
		return readErr
	}
	for _, c := range comments {
		v := states[c.ID]
		if v.UserID != userID {
			return auth.ErrSessionForbidden
		}
		if v.Version != c.Version || v.PostID != c.PostID || v.Status != comment.StatusApproved && v.Status != comment.StatusPending {
			return errOwnContentChanged
		}
	}
	for id, before := range visible {
		current, ok := parents[id]
		if !ok || !current.PublishedAt.Equal(before.PublishedAt) || !current.UpdatedAt.Equal(before.UpdatedAt) || !s.publicEngagementTarget(current.Post()) {
			return errOwnContentChanged
		}
		current.PublishedAt, current.UpdatedAt = before.PublishedAt, before.UpdatedAt
		if current != before {
			return errOwnContentChanged
		}
	}
	return r.Context().Err()
}

func (s *Server) ownContentError(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Del("Content-Length")
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		panic(http.ErrAbortHandler)
	case errors.Is(err, auth.ErrSessionUnavailable):
		s.sessionStorageError(w, r)
	case errors.Is(err, auth.ErrSessionNotFound):
		s.writeAPIError(w, r, 401, "auth.authentication_required", "请先登录后继续", nil)
	case errors.Is(err, auth.ErrSessionForbidden):
		s.writeAPIError(w, r, 403, "auth.identity_changed", "当前账号不能读取这些内容", nil)
	case errors.Is(err, errOwnContentChanged):
		s.writeAPIError(w, r, 409, "account.content_changed", "内容已更新，请刷新后重试", nil)
	case errors.Is(err, errOwnCommentsDisabled):
		s.writeAPIError(w, r, 404, "module.disabled", "评论功能已暂停", map[string]string{"module": "comments"})
	default:
		s.writeAPIError(w, r, 503, "account.content_unavailable", "内容暂时不可用，请稍后重试", nil)
	}
}
