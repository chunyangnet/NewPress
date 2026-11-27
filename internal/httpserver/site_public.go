package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/site"
)

type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	XMLNS   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Location     string `xml:"loc"`
	LastModified string `xml:"lastmod,omitempty"`
	Change       string `xml:"changefreq,omitempty"`
	Priority     string `xml:"priority,omitempty"`
}

func (s *Server) maintenanceMode(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		settings := s.siteSnapshot(request.Context()).Settings
		if !settings.Operations.MaintenanceEnabled || maintenanceBypass(request.URL.Path) {
			next.ServeHTTP(writer, request)
			return
		}
		writer.Header().Set("Retry-After", "300")
		writer.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		if strings.HasPrefix(request.URL.Path, "/api/") {
			s.writeAPIError(writer, request, http.StatusServiceUnavailable, "site.maintenance", settings.Operations.MaintenanceMessage, nil)
			return
		}
		data := s.baseViewFor(writer, "计划维护", settings.Operations.MaintenanceMessage, request.URL.Path)
		data.BodyClass = "is-maintenance"
		data.Status = statusPresentation(http.StatusServiceUnavailable, settings.Operations.MaintenanceMessage)
		data.Status.Kind, data.Status.Title, data.Status.Icon = "maintenance", "站点维护中", "refresh-cw"
		data.Description = settings.Operations.MaintenanceMessage
		s.render(writer, http.StatusServiceUnavailable, "maintenance", data)
	})
}

func maintenanceBypass(path string) bool {
	if path == "/robots.txt" || path == "/.well-known/security.txt" || path == "/theme-settings.css" || path == "/favicon.ico" {
		return true
	}
	for _, prefix := range []string{"/admin", "/health/", "/theme-assets/", "/core-assets/", "/media/", "/api/v1/admin/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	switch path {
	case "/api/v1/system", "/api/v1/plugins", "/api/v1/themes":
		return true
	default:
		return false
	}
}

func (s *Server) robotsTXT(writer http.ResponseWriter, request *http.Request) {
	snapshot := s.siteSettings.Snapshot()
	settings := snapshot.Settings
	body := ""
	if settings.Operations.MaintenanceEnabled || !settings.SEO.SearchEngineVisible {
		body = "User-agent: *\nDisallow: /\n"
	} else {
		body = strings.TrimSpace(settings.SEO.RobotsText)
		if body == "" {
			body = "User-agent: *\nAllow: /"
		}
		body += "\n"
		if settings.SEO.SitemapEnabled && !strings.Contains(strings.ToLower(body), "sitemap:") {
			body += "Sitemap: " + s.absolutePublicURL("/sitemap.xml") + "\n"
		}
	}
	serveGenerated(writer, request, "text/plain; charset=utf-8", []byte(body), publicCacheControl(settings, 300))
}

func (s *Server) sitemapXML(writer http.ResponseWriter, request *http.Request) {
	settings := s.siteSnapshot(request.Context()).Settings
	if !settings.SEO.SearchEngineVisible || !settings.SEO.SitemapEnabled || settings.Operations.MaintenanceEnabled {
		http.NotFound(writer, request)
		return
	}
	items, err := s.sitemapContent(request.Context())
	if err != nil {
		slog.Error("list content for sitemap", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "站点地图暂时不可用")
		return
	}
	terms, err := s.sitemapTerms(request.Context())
	if err != nil {
		slog.Error("list taxonomy for sitemap", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "站点地图暂时不可用")
		return
	}
	location := site.TimeLocation(settings)
	urls := []sitemapURL{
		{Location: s.absolutePublicURL("/"), Change: "daily", Priority: "1.0"},
		{Location: s.absolutePublicURL("/explore/"), Change: "daily", Priority: "0.8"},
	}
	for _, item := range items {
		path := s.contentPath(item)
		change := "weekly"
		priority := "0.7"
		if item.Type == content.TypePage {
			path = s.contentPath(item)
			change = "monthly"
			priority = "0.6"
		}
		urls = append(urls, sitemapURL{
			Location: s.absolutePublicURL(path), LastModified: item.UpdatedAt.In(location).Format("2006-01-02"),
			Change: change, Priority: priority,
		})
	}
	for _, term := range terms {
		priority := "0.5"
		if term.Taxonomy == content.TaxonomyTag {
			priority = "0.4"
		}
		urls = append(urls, sitemapURL{
			Location: s.absolutePublicURL(termArchiveURL(term)), LastModified: term.UpdatedAt.In(location).Format("2006-01-02"),
			Change: "weekly", Priority: priority,
		})
	}
	payload, err := xml.MarshalIndent(sitemapURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9", URLs: urls}, "", "  ")
	if err != nil {
		slog.Error("encode sitemap", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "站点地图暂时不可用")
		return
	}
	payload = append([]byte(xml.Header), append(payload, '\n')...)
	writer.Header().Set("X-Robots-Tag", "noindex")
	serveGenerated(writer, request, "application/xml; charset=utf-8", payload, publicCacheControl(settings, 300))
}

func (s *Server) sitemapContent(ctx context.Context) ([]content.Post, error) {
	const maximumItems = 50_000
	now := time.Now()
	items := make([]content.Post, 0, 256)
	types := []string{content.TypePost, content.TypePage}
	if s.moduleEnabled("flash") {
		types = append(types, content.TypeFlash)
	}
	for _, postType := range types {
		for _, status := range []string{content.StatusPublish, content.StatusFuture} {
			for offset := 0; offset < maximumItems; offset += 100 {
				result, err := s.repository.ListAdmin(ctx, content.AdminQuery{Type: postType, Status: status, Limit: 100, Offset: offset})
				if err != nil {
					return nil, err
				}
				for _, item := range result.Items {
					if content.PublicAt(item, now) {
						items = append(items, item)
						if len(items) >= maximumItems {
							return items, nil
						}
					}
				}
				if offset+len(result.Items) >= result.Total || len(result.Items) == 0 {
					break
				}
			}
		}
	}
	return items, nil
}

func (s *Server) sitemapTerms(ctx context.Context) ([]content.Term, error) {
	const maximumItems = 50_000
	items := make([]content.Term, 0, 128)
	for offset := 0; offset < maximumItems; offset += 500 {
		result, err := s.repository.ListTerms(ctx, content.TermQuery{Limit: 500, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, term := range result.Items {
			if term.Count <= 0 {
				continue
			}
			items = append(items, term)
			if len(items) >= maximumItems {
				return items, nil
			}
		}
		if offset+len(result.Items) >= result.Total || len(result.Items) == 0 {
			break
		}
	}
	return items, nil
}

func (s *Server) securityTXT(writer http.ResponseWriter, request *http.Request) {
	settings := s.siteSettings.Snapshot().Settings
	contact := settings.Operations.SecurityContact
	if contact == "" {
		http.NotFound(writer, request)
		return
	}
	expires := time.Now().UTC().Truncate(24*time.Hour).AddDate(1, 0, 0).Format(time.RFC3339)
	body := strings.Join([]string{
		"Contact: " + contact,
		"Expires: " + expires,
		"Canonical: " + s.absolutePublicURL("/.well-known/security.txt"),
		"Preferred-Languages: " + settings.Identity.Locale,
		"",
	}, "\n")
	serveGenerated(writer, request, "text/plain; charset=utf-8", []byte(body), "public, max-age=3600, must-revalidate")
}

func (s *Server) absolutePublicURL(path string) string {
	base := strings.TrimRight(s.config.PublicURL, "/")
	if base == "" {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

func (s *Server) publicResourceURL(value string) string {
	if strings.HasPrefix(value, "/") {
		return s.absolutePublicURL(value)
	}
	return value
}

func serveGenerated(writer http.ResponseWriter, request *http.Request, contentType string, payload []byte, cacheControl string) {
	digest := sha256.Sum256(payload)
	etag := `"generated-` + hex.EncodeToString(digest[:8]) + `"`
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", cacheControl)
	writer.Header().Set("ETag", etag)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if etagMatches(request.Header.Get("If-None-Match"), etag) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(payload)
}

func publicCacheControl(settings site.Settings, fallback int) string {
	seconds := settings.Operations.PublicCacheSeconds
	if seconds == 0 {
		seconds = fallback
	}
	if seconds <= 0 {
		return "no-cache"
	}
	return fmt.Sprintf("public, max-age=%d, stale-while-revalidate=%d", seconds, min(seconds, 300))
}
