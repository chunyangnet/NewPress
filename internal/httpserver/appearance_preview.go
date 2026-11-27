package httpserver

import (
	"log/slog"
	"net/http"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/theme"
)

func (s *Server) appearancePreview(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilityAppearanceManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	query, ok := s.strictAPIQuery(w, r, map[string]bool{"page": true})
	if !ok {
		return
	}
	page := query.Get("page")
	if page != "home" && page != "article" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "preview_page_invalid"})
		return
	}
	values := s.themes.SettingsSnapshot().Values
	if r.Method == http.MethodPost {
		if readOnly || !s.sameOriginRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "preview_forbidden"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
		if err := r.ParseForm(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "preview_form_invalid"})
			return
		}
		if !s.auth.CheckCSRF(p, r.PostForm.Get("csrf_token")) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
			return
		}
		var fields map[string]string
		values, fields = s.themes.ParseSettingsForm(r.PostForm)
		if len(fields) > 0 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "preview_settings_invalid", "fields": fields})
			return
		}
	}
	snapshot, css, classes, fields := s.themes.PreviewSettings(values)
	if len(fields) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "preview_settings_invalid", "fields": fields})
		return
	}
	var data pageView
	var err error
	templateName := "home"
	if page == "home" {
		previewRequest := r.Clone(r.Context())
		copyURL := *r.URL
		copyURL.Path, copyURL.RawQuery = "/", ""
		previewRequest.URL = &copyURL
		data, err = s.homePage(previewRequest, snapshot.Values)
	} else {
		posts, loadErr := s.repository.ListPublished(r.Context(), 1)
		err = loadErr
		if err == nil && len(posts) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "preview_article_missing"})
			return
		}
		if err == nil {
			post := posts[0]
			data = s.baseViewFor(w, post.Title, post.Excerpt, s.contentPath(post))
			data.Post, data.BodyClass, templateName = post, "is-single", "single"
		}
	}
	if err != nil {
		slog.Warn("appearance preview content", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "preview_content_unavailable"})
		return
	}
	data.IsPreview, data.ThemeValues, data.ThemeClass = true, snapshot.Values, classes
	s.prepareResources(r.Context(), &data)
	s.prepareArticleWidgets(r.Context(), &data)
	s.prepareArticleComponents(r.Context(), &data)
	s.prepareResponsiveImages(&data)
	s.prepareSiteIntegrations(w, http.StatusOK, &data, templateName)
	data.Kind = templateName
	var html theme.RenderBuffer
	if err := s.renderTheme(&html, templateName, data); err != nil {
		slog.Error("appearance preview render", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "preview_render_failed"})
		return
	}
	payload := withSelectAssets(html.Bytes(), templateName)
	if strings.Contains(data.Post.ContentHTML, `data-np-config=`) {
		payload = withArticleComponentAssets(payload)
	} else if strings.Contains(data.Post.ContentHTML, `data-np-widget="post"`) {
		payload = withArticleWidgetStyles(payload)
	}
	if strings.Contains(data.Post.ContentHTML, `data-npr-resource=`) {
		payload = withResourceAssets(payload)
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": string(payload), "css": css})
}
