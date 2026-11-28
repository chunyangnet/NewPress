package httpserver

import (
	"context"
	"errors"
	"net/http"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

// Diagnostics remain readable while the corresponding module is paused. Only
// the original session, capability alternatives and content scope gate them.
type featureStatusAuthority struct {
	server       *Server
	request      *http.Request
	ctx          context.Context
	capabilities []string
	writeObject  bool
	source       content.PostState
}

func (a *featureStatusAuthority) view() (adminView, error) {
	if err := a.ctx.Err(); err != nil {
		return adminView{}, err
	}
	var current content.PostState
	var sourceErr error
	if a.source.ID > 0 {
		current, sourceErr = content.ReadPostState(a.ctx, a.server.repository, a.source.ID)
		if sourceErr == nil && current.ID != a.source.ID {
			sourceErr = content.ErrNotFound
		}
	}
	// Source reads can outlive a session. Refresh identity afterwards even when
	// storage fails, so loss of identity or capability keeps precedence.
	s, r := a.server, a.request
	view := adminView{}
	if !s.auth.Configured() {
		if !s.isLoopbackRequest(r) {
			return view, auth.ErrSessionNotFound
		}
		view.ReadOnly, view.Preview = true, true
	} else {
		p, err := s.originalSessionPrincipal(a.ctx, r)
		if err != nil {
			return view, err
		}
		if p.Username == "" {
			return view, auth.ErrSessionNotFound
		}
		if !p.Can(auth.CapabilityAdminAccess) || !principalCanAny(p, a.capabilities...) {
			return view, auth.ErrSessionForbidden
		}
		view.Principal = p
		if sourceErr == nil && a.source.ID > 0 {
			allowed := canReadContentObject
			if a.writeObject {
				allowed = canWriteContentObject
			}
			// Both the prepared snapshot and current object must remain visible.
			for _, state := range []content.PostState{a.source, current} {
				if !allowed(p, content.Post{ID: state.ID, AuthorID: state.AuthorID, Type: state.Type}) {
					return adminView{}, content.ErrNotFound
				}
			}
		}
	}
	if err := a.ctx.Err(); err != nil {
		return adminView{}, err
	}
	return view, sourceErr
}

func (a *featureStatusAuthority) access() error {
	_, err := a.view()
	return err
}

func (a *featureStatusAuthority) fail(w http.ResponseWriter, err error) {
	if a.request.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	w.Header().Set("Cache-Control", "no-store")
	for _, name := range []string{"Content-Length", "ETag", "Last-Modified", "Location"} {
		w.Header().Del(name)
	}
	switch {
	case errors.Is(err, content.ErrNotFound):
		http.NotFound(w, a.request)
	case errors.Is(err, auth.ErrSessionNotFound), errors.Is(err, auth.ErrSessionForbidden), errors.Is(err, auth.ErrSessionUnavailable):
		a.server.contentAccessError(w, a.request, err, true)
	default:
		a.server.writeAPIError(w, a.request, http.StatusServiceUnavailable, "feature_status.unavailable", "业务状态暂时不可用，请稍后重试", nil)
	}
}
