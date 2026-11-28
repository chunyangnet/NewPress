package httpserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
)

func (s *Server) circleUploadActor(ctx context.Context, r *http.Request) (community.Actor, error) {
	p, err := s.originalSessionPrincipal(ctx, r)
	if err != nil {
		return community.Actor{}, err
	}
	if p.UserID <= 0 {
		return community.Actor{}, auth.ErrSessionNotFound
	}
	if !s.auth.CheckCSRF(p, r.Header.Get("X-CSRF-Token")) {
		return community.Actor{}, auth.ErrSessionForbidden
	}
	return community.Actor{ID: p.UserID, Admin: p.Can(auth.CapabilitySettingsManage)}, nil
}

func (s *Server) circleAttachmentError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrSessionNotFound):
		s.writeAPIError(w, r, http.StatusUnauthorized, "auth.authentication_required", "请先登录后继续", nil)
	case errors.Is(err, auth.ErrSessionForbidden):
		s.writeAPIError(w, r, http.StatusForbidden, "community.forbidden", "当前身份不能执行此操作", nil)
	case errors.Is(err, auth.ErrSessionUnavailable):
		s.sessionStorageError(w, r)
	default:
		s.circleError(w, r, err)
	}
}

// Apply is reached after Files.Upload has acquired its upload lock and staged
// the bytes. Re-resolve the actor here as well as after reading the request body.
// A failed authorization is a definite rejection: the backing store was never
// called, so Files.Upload can perform its existing reference-aware cleanup.
type circleAttachmentUploadStore struct {
	community.Store
	actor   func(context.Context) (community.Actor, error)
	authErr error
}

func (s *circleAttachmentUploadStore) Apply(ctx context.Context, slug string, _ community.Actor, cmd community.Command, now time.Time) (community.Result, error) {
	actor, err := s.actor(ctx)
	if err != nil {
		s.authErr = err
		return community.Result{}, community.ErrForbidden
	}
	return s.Store.Apply(ctx, slug, actor, cmd, now)
}

func (s *Server) circleAttachmentAccess(r *http.Request, slug string, original community.Attachment) error {
	c, err := s.circles.Get(r.Context(), slug)
	if err != nil {
		return err
	}
	// Resolve the session after the circle read, which may itself block on I/O.
	p, err := s.originalSessionPrincipal(r.Context(), r)
	if errors.Is(err, auth.ErrSessionForbidden) {
		return community.ErrNotFound
	}
	if err != nil {
		return err
	}
	file, ok := community.AttachmentByID(c, original.ID)
	actor := community.Actor{ID: p.UserID, Admin: p.Can(auth.CapabilitySettingsManage)}
	if !ok || file != original || !community.CanReadAttachment(c, file, actor) {
		return community.ErrNotFound
	}
	return r.Context().Err()
}
