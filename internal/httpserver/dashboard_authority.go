package httpserver

import (
	"net/http"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

// A dashboard combines independent permissions. Preserve the authority for
// each loaded section without requiring article access from a dashboard-only
// account. The original query scope also protects counts and empty queues.
func (s *Server) dashboardAccess(r *http.Request, initial auth.Principal, posts []content.Post, commentsLoaded bool) error {
	if err := r.Context().Err(); err != nil {
		return err
	}
	if !s.auth.Configured() {
		if s.isLoopbackRequest(r) {
			return nil
		}
		return auth.ErrSessionNotFound
	}
	if initial.Can(auth.CapabilityContentRead) {
		if err := s.contentListAccess(r, contentAuthorScope(initial), posts, false); err != nil {
			return err
		}
	}
	p, err := s.originalSessionPrincipal(r.Context(), r)
	if err != nil {
		return err
	}
	if p.Username == "" {
		return auth.ErrSessionNotFound
	}
	if !p.Can(auth.CapabilityAdminAccess) || r.Method == http.MethodGet && !p.Can(auth.CapabilityDashboardRead) || r.Method == http.MethodPost && !canCreateContent(p) {
		return auth.ErrSessionForbidden
	}
	if initial.Can(auth.CapabilityContentRead) && (!p.Can(auth.CapabilityContentRead) || contentAuthorScope(p) != 0 && contentAuthorScope(p) != contentAuthorScope(initial)) {
		return auth.ErrSessionForbidden
	}
	for _, capabilities := range [][]string{
		{auth.CapabilityPluginsManage, auth.CapabilitySystemRead},
		{auth.CapabilityAppearanceManage, auth.CapabilitySystemRead},
		{auth.CapabilitySessionsRead},
		{auth.CapabilitySessionsManage},
		{auth.CapabilityPluginsManage},
	} {
		if principalCanAny(initial, capabilities...) && !principalCanAny(p, capabilities...) {
			return auth.ErrSessionForbidden
		}
	}
	if commentsLoaded && (!p.Can(auth.CapabilityCommentsModerate) || !s.moduleEnabled("comments")) {
		return auth.ErrSessionForbidden
	}
	return r.Context().Err()
}
