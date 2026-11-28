package httpserver

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"newpress.local/newpress/internal/auth"
)

// An administrative page can contain several independently authorized sections.
// Retaining an alternative entry capability cannot authorize a section prepared
// under a capability that was lost while reading storage or rendering the page.
func (s *Server) diagnosticPageAccess(r *http.Request, initial auth.Principal, capabilities ...string) error {
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
	if !p.Can(auth.CapabilityAdminAccess) || !principalCanAny(p, capabilities...) {
		return auth.ErrSessionForbidden
	}
	// Compare effective capabilities consumed by these pages and their shell,
	// not role spellings or unrelated account permissions. Wildcard roles may
	// be replaced with equivalent explicit capabilities while a read is active.
	for _, capability := range []string{
		auth.CapabilityDashboardRead, auth.CapabilityContentRead, auth.CapabilityContentWrite,
		auth.CapabilityContentWriteOwn, auth.CapabilityTaxonomyManage, auth.CapabilityMediaRead,
		auth.CapabilityCommentsModerate, auth.CapabilityUsersRead, auth.CapabilitySessionsRead,
		auth.CapabilitySessionsManage, auth.CapabilityAppearanceManage, auth.CapabilityNavigationManage,
		auth.CapabilitySettingsManage, auth.CapabilityPluginsManage, auth.CapabilitySystemRead,
		auth.CapabilitySystemUpdate,
	} {
		if initial.Can(capability) && !p.Can(capability) {
			return auth.ErrSessionForbidden
		}
	}
	if scope := contentAuthorScope(p); scope != 0 && scope != contentAuthorScope(initial) {
		return auth.ErrSessionForbidden
	}
	return r.Context().Err()
}

func (s *Server) diagnosticReadError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	w.Header().Set("Cache-Control", "no-store")
	for _, name := range []string{"Content-Length", "Content-Disposition", "ETag", "Last-Modified", "Location"} {
		w.Header().Del(name)
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		s.contentAccessError(w, r, err, true)
		return
	}
	switch {
	case errors.Is(err, auth.ErrSessionNotFound):
		http.Redirect(w, r, "/admin/login/?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	case errors.Is(err, auth.ErrSessionForbidden):
		s.renderError(w, http.StatusForbidden, "当前账号没有访问此页面的权限")
	case errors.Is(err, auth.ErrSessionUnavailable):
		s.sessionStorageError(w, r)
	default:
		s.renderError(w, http.StatusServiceUnavailable, "页面暂时不可用，请稍后重试")
	}
}

func (s *Server) recheckDiagnosticPage(w http.ResponseWriter, r *http.Request, access func() error) bool {
	if err := access(); err != nil {
		s.diagnosticReadError(w, r, err)
		return false
	}
	return true
}
