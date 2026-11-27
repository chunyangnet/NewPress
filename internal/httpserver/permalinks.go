package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/site"
)

type contentPathBuilder func(content.Post) string

func (v pageView) ContentURL(p content.Post) string { return resolvedContentPath(p, v.contentPaths) }

func resolvedContentPath(p content.Post, paths ...contentPathBuilder) string {
	if len(paths) > 0 && paths[0] != nil {
		return paths[0](p)
	}
	return content.PublicPath(p)
}

func (s *Server) permalinkBuilder() contentPathBuilder {
	if s.siteSettings == nil {
		return content.PublicPath
	}
	settings := s.siteSettings.Snapshot().Settings
	location := site.TimeLocation(settings)
	return func(p content.Post) string {
		if p.Type == content.TypeFlash {
			return content.PublicPath(p)
		}
		prefix, style := "/posts/", settings.Writing.PostPermalink
		if p.Type == content.TypePage {
			prefix, style = "/pages/", settings.Writing.PagePermalink
		}
		if style == "id" && p.ID > 0 {
			return prefix + "id/" + strconv.FormatInt(p.ID, 10)
		}
		if (style == "month" || style == "date") && !p.PublishedAt.IsZero() {
			layout := "2006/01/"
			if style == "date" {
				layout = "2006/01/02/"
			}
			return prefix + p.PublishedAt.In(location).Format(layout) + p.Slug
		}
		return content.PublicPath(p)
	}
}

func (s *Server) contentPath(p content.Post) string { return s.permalinkBuilder()(p) }

func (s *Server) canonicalContentRedirect(w http.ResponseWriter, r *http.Request, p content.Post) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	target := s.contentPath(p)
	if r.URL.Path == target {
		return false
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusMovedPermanently)
	return true
}

// All supported structures keep resolving after a setting changes. The content
// visibility check is shared by the ID, dated, and legacy forms.
func (s *Server) contentAtPublicPath(ctx context.Context, path string) (content.Post, error) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 2 || (parts[0] != "posts" && parts[0] != "pages") {
		return content.Post{}, content.ErrNotFound
	}
	kind := content.TypePost
	if parts[0] == "pages" {
		kind = content.TypePage
	}
	var p content.Post
	var err error
	switch {
	case len(parts) == 3 && parts[1] == "id":
		id, e := strconv.ParseInt(parts[2], 10, 64)
		if e != nil || id < 1 || strconv.FormatInt(id, 10) != parts[2] {
			return p, content.ErrNotFound
		}
		p, err = s.repository.GetByID(ctx, id)
	case len(parts) == 2 || kind == content.TypePost && (len(parts) == 4 || len(parts) == 5):
		slug := parts[len(parts)-1]
		if !content.ValidSlug(slug) {
			return p, content.ErrNotFound
		}
		if kind == content.TypePage {
			p, err = s.repository.GetPublishedPageBySlug(ctx, slug)
		} else {
			p, err = s.repository.GetPublishedBySlug(ctx, slug)
		}
		if errors.Is(err, content.ErrNotFound) {
			target, e := s.repository.ResolveRedirect(ctx, "/"+parts[0]+"/"+slug)
			if e == nil && strings.HasPrefix(target, "/"+parts[0]+"/") && target != "/"+parts[0]+"/"+slug {
				replacement := strings.TrimPrefix(target, "/"+parts[0]+"/")
				if content.ValidSlug(replacement) {
					if kind == content.TypePage {
						p, err = s.repository.GetPublishedPageBySlug(ctx, replacement)
					} else {
						p, err = s.repository.GetPublishedBySlug(ctx, replacement)
					}
				}
			}
		}
		if err == nil && len(parts) > 2 {
			layout := "2006/01"
			if len(parts) == 5 {
				layout = "2006/01/02"
			}
			if p.PublishedAt.In(site.TimeLocation(s.siteSettings.Snapshot().Settings)).Format(layout) != strings.Join(parts[1:len(parts)-1], "/") {
				return content.Post{}, content.ErrNotFound
			}
		}
	default:
		return p, content.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if p.Type != kind || !content.PublicAt(p, time.Now()) {
		return content.Post{}, content.ErrNotFound
	}
	return p, nil
}

func (s *Server) permalinkContent(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if r.Method == http.MethodPost {
		path = strings.TrimSuffix(path, "/comments")
	}
	p, err := s.contentAtPublicPath(r.Context(), path)
	if err != nil {
		if errors.Is(err, content.ErrNotFound) {
			s.renderError(w, 404, "内容没有找到")
		} else {
			s.renderError(w, 503, "内容暂时不可用")
		}
		return
	}
	if s.canonicalContentRedirect(w, r, p) {
		return
	}
	r.SetPathValue("slug", p.Slug)
	if r.Method == http.MethodPost {
		s.publicCommentCreate(w, r)
		return
	}
	if p.Type == content.TypePage {
		s.page(w, r)
	} else {
		s.single(w, r)
	}
}

func (s *Server) registerPermalinkRoutes(mux *moduleMux) {
	for _, path := range []string{"/posts/id/{id}", "/posts/{year}/{month}/{slug}", "/posts/{year}/{month}/{day}/{slug}", "/pages/id/{id}"} {
		mux.HandleFunc("GET "+path, s.permalinkContent)
		if strings.HasPrefix(path, "/posts/") {
			mux.HandleFunc("POST "+path+"/comments", s.permalinkContent)
		}
	}
}
