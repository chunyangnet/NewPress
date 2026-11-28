package httpserver

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/version"
)

func (s *Server) live(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	// Deep readiness traverses private business state. Only a reliably resolved
	// loopback probe may invoke it; forwarded headers alone never grant access.
	if !s.isLoopbackRequest(request) {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("X-NewPress-Instance", os.Getenv("NEWPRESS_UPDATE_INSTANCE"))
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := s.repository.Ready(ctx); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if err := s.media.Ready(); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if err := s.comments.Ready(ctx); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if err := s.navigation.Ready(ctx); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if err := s.auth.Ready(ctx); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if err := s.engagements.Ready(ctx); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if s.currencyCatalog != nil && s.currencyCatalog.Ready(ctx) != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if s.progression != nil && s.progression.Store.Ready(ctx) != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if s.commerce != nil && s.commerce.Ready(ctx) != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "component": "payments"})
		return
	}
	if err := s.notifications.Store.Ready(ctx); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if s.linkPages == nil || s.linkPages.Ready(ctx) != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if s.feedback == nil || s.feedback.Ready(ctx) != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if s.questions != nil && s.questions.Ready(ctx) != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if s.config.CommunityEnabled {
		if s.circles == nil || s.circles.Ready(ctx) != nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) systemAPI(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAdminAPI(writer, request, auth.CapabilitySystemRead) {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	contentCount := 0
	categoryCount := 0
	tagCount := 0
	contentRepositoryReady := true
	taxonomyReady := true
	if items, err := s.repository.ListAdmin(ctx, content.AdminQuery{Limit: 1}); err == nil {
		contentCount = items.Total
	} else {
		contentRepositoryReady = false
	}
	if terms, err := s.repository.ListTerms(ctx, content.TermQuery{Limit: 1}); err == nil {
		categoryCount = terms.Counts[content.TaxonomyCategory]
		tagCount = terms.Counts[content.TaxonomyTag]
	} else {
		contentRepositoryReady = false
		taxonomyReady = false
	}
	if err := s.repository.Ready(ctx); err != nil {
		contentRepositoryReady = false
		taxonomyReady = false
	}
	mediaSnapshot := s.media.Snapshot()
	commentSnapshot, commentErr := s.comments.Snapshot(ctx)
	navigationSnapshot, navigationErr := s.navigation.Snapshot(ctx)
	writeJSON(writer, http.StatusOK, map[string]any{
		"name":                      version.Name,
		"version":                   version.Version,
		"api":                       version.APIVersion,
		"commit":                    version.Commit,
		"build_date":                version.BuildDate,
		"go_version":                runtime.Version(),
		"active_theme":              s.themes.Active(),
		"site_settings_revision":    s.siteSettings.Snapshot().Revision,
		"media_library_revision":    mediaSnapshot.Revision,
		"media_count":               mediaSnapshot.Count,
		"comment_count":             commentSnapshot.Total,
		"pending_comment_count":     commentSnapshot.StatusCounts[comment.StatusPending],
		"comment_repository_ready":  commentErr == nil,
		"navigation_ready":          navigationErr == nil,
		"navigation_menu_count":     navigationSnapshot.MenuCount,
		"navigation_item_count":     navigationSnapshot.ItemCount,
		"navigation_location_count": len(navigationSnapshot.Locations),
		"content_count":             contentCount,
		"term_count":                categoryCount + tagCount,
		"category_count":            categoryCount,
		"tag_count":                 tagCount,
		"taxonomy_ready":            taxonomyReady,
		"content_repository_ready":  contentRepositoryReady,
		"uptime":                    time.Since(s.startedAt).Round(time.Second).String(),
	})
}

func (s *Server) pluginsAPI(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAdminAPI(writer, request, auth.CapabilityPluginsManage) {
		return
	}
	plugins, err := s.plugins.List()
	if err != nil {
		s.writeAPIError(writer, request, http.StatusInternalServerError, "plugins.unavailable", "插件清单暂时不可用", nil)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": plugins, "count": len(plugins)})
}

func (s *Server) themesAPI(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAdminAPI(writer, request, auth.CapabilityAppearanceManage) {
		return
	}
	themes, err := s.themes.List()
	if err != nil {
		s.writeAPIError(writer, request, http.StatusInternalServerError, "themes.unavailable", "主题清单暂时不可用", nil)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": themes, "count": len(themes)})
}
