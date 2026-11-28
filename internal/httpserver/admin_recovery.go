package httpserver

import (
	"context"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
)

// Recovery pages show status and navigation, not the administrative summary.
// Build identity without storage reads so access can be rechecked first.
func (s *Server) adminRecoveryIdentity(current string, principal auth.Principal, readOnly bool) adminView {
	username := principal.Label()
	if username == "" {
		username = "本机只读预览"
	}
	return adminView{
		Current: current, Configured: s.auth.Configured(), Authenticated: principal.Username != "",
		Preview: !s.auth.Configured(), ReadOnly: readOnly, Principal: principal,
		Username: username, Login: principal.Username, RoleName: principal.RoleName,
		CSRFToken: principal.CSRFToken, PersistentUsers: s.auth.PersistentUsers(),
	}
}

func (s *Server) prepareAdminRecovery(ctx context.Context, a *adminView) {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if ctx.Err() != nil {
		return
	}
	a.SiteSettings = s.siteSnapshot(ctx)
	if a.Can(auth.CapabilityPluginsManage) && s.plugins != nil {
		a.PluginSettings = s.plugins.SettingsPages()
	}
	if ctx.Err() != nil || s.comments == nil {
		return
	}
	// Use the same capability and module rules as the rendered sidebar. A
	// hidden comment entry needs no count, including when its module is paused.
	for _, group := range a.Navigation() {
		for _, item := range group.Items {
			if item.ID == "comments" {
				if snapshot, err := s.comments.Snapshot(ctx); err == nil && ctx.Err() == nil {
					a.PendingComments = snapshot.StatusCounts[comment.StatusPending]
				}
				return
			}
		}
	}
}
