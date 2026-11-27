package httpserver

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/content"
)

func (s *Server) commentPostForRequest(r *http.Request, slug string) (content.Post, error) {
	if !s.moduleEnabled("comments") {
		return content.Post{}, content.ErrNotFound
	}
	if strings.HasPrefix(r.URL.Path, "/flash/") || strings.HasPrefix(r.URL.Path, "/api/v1/flash/") {
		return s.findFlash(r, slug)
	}
	return s.repository.GetPublishedBySlug(r.Context(), slug)
}

func commentAPIURL(post content.Post) string {
	if post.Type == content.TypeFlash {
		return "/api/v1/flash/" + post.Slug + "/comments"
	}
	return "/api/v1/posts/" + post.Slug + "/comments"
}

func contentCommentPageURL(post content.Post, page int, paths ...contentPathBuilder) string {
	path := resolvedContentPath(post, paths...)
	if page > 1 {
		path += "?comments_page=" + strconv.Itoa(page)
	}
	return path + "#comments"
}

func (s *Server) publicEngagementTarget(post content.Post) bool {
	return (post.Type == content.TypePost || post.Type == content.TypeFlash && s.moduleEnabled("flash")) && content.PublicAt(post, time.Now())
}
