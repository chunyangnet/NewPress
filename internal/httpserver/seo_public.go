package httpserver

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/assetref"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
)

type pageSEOView struct {
	Kind, Published, Modified, ImageAlt string
	ImageWidth, ImageHeight             int
	JSONLD                              template.JS
}

type editorSEOView struct {
	Title, Description, URL, Image, Cover, SiteTitle, Separator, DefaultImage string
	Visible                                                                   bool
}

func (s *Server) shareImage(value, basePath string) (string, int, int) {
	if value == "" {
		return "", 0, 0
	}
	base, err := url.Parse(s.publicResourceURL(basePath))
	if err != nil {
		return "", 0, 0
	}
	reference, err := url.Parse(value)
	if err != nil {
		return "", 0, 0
	}
	absolute := base.ResolveReference(reference)
	if (absolute.Scheme != "http" && absolute.Scheme != "https") || absolute.Host == "" || absolute.User != nil {
		return "", 0, 0
	}
	if s.media != nil {
		if link, ok := assetref.New(s.config.PublicURL).URL(absolute.String(), basePath); ok {
			item, err := s.media.Get(link.ID)
			if err != nil || item.Status() != media.StatusActive || item.PublicURL != link.Path {
				return "", 0, 0
			}
			if absolute.RawQuery == "" && absolute.Fragment == "" && item.Image.Recipe > 0 {
				variant, _ := item.Variant("card-large")
				return s.absolutePublicURL(item.VariantURL("card-large", "native")), variant.Width, variant.Height
			}
			if absolute.RawQuery == "" {
				return absolute.String(), item.Width, item.Height
			}
		}
	}
	return absolute.String(), 0, 0
}

func (s *Server) editorSEOPreview(input content.PostInput) editorSEOView {
	settings := s.siteSettings.Snapshot().Settings
	normalized, fields := content.NormalizePostInput(input, time.Now())
	preview := editorSEOView{SiteTitle: settings.Identity.Title, Separator: settings.Identity.TitleSeparator, DefaultImage: settings.Identity.SocialDefaultImage, Visible: settings.SEO.SearchEngineVisible, Cover: input.CoverURL}
	title := input.Title
	if title == "" {
		title = "文章标题"
	}
	preview.Title = title + " " + preview.Separator + " " + preview.SiteTitle
	preview.Description = input.Excerpt
	if preview.Description == "" {
		preview.Description = normalized.Excerpt
	}
	if preview.Description == "" {
		preview.Description = settings.Identity.Description
	}
	slug := content.NormalizeSlug(input.Slug)
	if slug == "" {
		slug = content.NormalizeSlug(input.Title)
	}
	if slug == "" {
		slug = "your-article"
	}
	path := s.contentPath(content.Post{Type: input.Type, Slug: slug, PublishedAt: input.PublishedAt})
	preview.URL = s.absolutePublicURL(path)
	// Invalid submitted covers remain editable, but must never become image
	// requests in the error page before the client enhancement runs.
	if fields["cover_url"] != "" {
		return preview
	}
	cover := normalized.CoverURL
	if cover == "" {
		cover = preview.DefaultImage
	}
	preview.Image, _, _ = s.shareImage(cover, path)
	return preview
}

func (s *Server) preparePublicSEO(w http.ResponseWriter, status int, data *pageView) {
	data.SEO = pageSEOView{Kind: "website"}
	if status != http.StatusOK || data.IsAdmin || data.IsPreview || !trackingPage(data.Kind) {
		return
	}
	imagePath := data.Site.SocialImageURL
	if data.Post.ID > 0 && data.Post.CoverURL != "" {
		imagePath = data.Post.CoverURL
	}
	data.Site.SocialImageURL, data.SEO.ImageWidth, data.SEO.ImageHeight = s.shareImage(imagePath, data.Canonical)
	if data.Site.SocialImageURL != "" {
		data.SEO.ImageAlt = data.PageTitle
	}
	var main map[string]any
	if data.Post.ID > 0 && content.PublicAt(data.Post, time.Now()) {
		p := data.Post
		kind := "WebPage"
		if p.Type == content.TypePost {
			kind = "BlogPosting"
			data.SEO.Kind = "article"
		}
		data.SEO.Published = p.PublishedAt.UTC().Format(time.RFC3339)
		data.SEO.Modified = p.UpdatedAt.UTC().Format(time.RFC3339)
		author := map[string]any{"@type": "Person", "name": p.AuthorName}
		if p.AuthorID > 0 {
			author["url"] = s.absolutePublicURL("/authors/" + strconv.FormatInt(p.AuthorID, 10) + "/")
		}
		main = map[string]any{"@type": kind, "@id": data.Canonical + "#content", "url": data.Canonical, "mainEntityOfPage": data.Canonical, "headline": p.Title, "description": data.Description, "datePublished": data.SEO.Published, "dateModified": data.SEO.Modified, "author": author, "publisher": map[string]any{"@type": "Organization", "name": data.Site.Title, "url": s.absolutePublicURL("/")}, "inLanguage": data.Site.Locale}
		if data.Site.SocialImageURL != "" {
			main["image"] = []string{data.Site.SocialImageURL}
		}
	} else if data.Kind == "home" {
		main = map[string]any{"@type": "WebSite", "@id": s.absolutePublicURL("/") + "#website", "url": s.absolutePublicURL("/"), "name": data.Site.Title, "description": data.Site.Description, "inLanguage": data.Site.Locale}
	}
	if main == nil || !s.siteSettings.Snapshot().Settings.SEO.SearchEngineVisible {
		return
	}
	canonical, err := url.Parse(data.Canonical)
	if err != nil || (canonical.Scheme != "http" && canonical.Scheme != "https") || canonical.Host == "" {
		return
	}
	graph := []any{main}
	if data.Post.ID > 0 {
		graph = append(graph, map[string]any{"@type": "BreadcrumbList", "itemListElement": []any{
			map[string]any{"@type": "ListItem", "position": 1, "name": data.Site.Title, "item": s.absolutePublicURL("/")},
			map[string]any{"@type": "ListItem", "position": 2, "name": data.Post.Title, "item": data.Canonical},
		}})
	}
	payload, err := json.Marshal(map[string]any{"@context": "https://schema.org", "@graph": graph})
	if err != nil {
		return
	}
	// Only JSON produced here may become script text. Marshal's HTML escaping
	// prevents a title containing </script> from terminating the data element.
	data.SEO.JSONLD = template.JS(payload)
	digest := sha256.Sum256(payload)
	policy := w.Header().Get("Content-Security-Policy")
	if policy != "" {
		w.Header().Set("Content-Security-Policy", strings.Replace(policy, "script-src 'self'", "script-src 'self' 'sha256-"+base64.StdEncoding.EncodeToString(digest[:])+"'", 1))
	}
}
