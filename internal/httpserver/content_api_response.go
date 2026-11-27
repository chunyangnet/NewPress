package httpserver

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
)

func (s *Server) writeContentAPISuccess(writer http.ResponseWriter, status int, post content.Post, location bool) {
	s.setContentAPIHeaders(writer, post)
	if location {
		writer.Header().Set("Location", "/api/v1/admin/contents/"+strconv.FormatInt(post.ID, 10))
	}
	writeJSON(writer, status, adminContentResponse(post, time.Now(), s.contentPath))
}

func (s *Server) setContentAPIHeaders(writer http.ResponseWriter, post content.Post) {
	writer.Header().Set("ETag", contentAPIETag(post))
	writer.Header().Set("Last-Modified", post.UpdatedAt.UTC().Format(http.TimeFormat))
}

func adminContentSummaryResponse(post content.Post, now time.Time, paths ...contentPathBuilder) adminContentSummaryAPIResponse {
	return adminContentSummaryAPIResponse{
		Pinned:       post.IsPinned(),
		ArticleBadge: content.ArticleBadgeConfig(post),
		ID:           post.ID, AuthorID: post.AuthorID, Type: post.Type, Status: post.Status,
		Slug: post.Slug, Title: post.Title, Excerpt: post.Excerpt, CoverURL: post.CoverURL,
		AuthorName: post.AuthorName, Category: post.Category, Categories: contentTermResponses(post.Categories), Tags: contentTermResponses(post.Tags), CommentStatus: post.CommentStatus,
		PublishedAt: optionalAPITime(post.PublishedAt), CreatedAt: post.CreatedAt, UpdatedAt: post.UpdatedAt,
		Version: post.Version, ETag: contentAPIETag(post), Permalink: resolvedContentPath(post, paths...),
		IsPublic: content.PublicAt(post, now),
	}
}

func adminContentResponse(post content.Post, now time.Time, paths ...contentPathBuilder) adminContentAPIResponse {
	return adminContentAPIResponse{
		Resources:    resources.FromMeta(post.Meta),
		Pinned:       post.IsPinned(),
		ArticleBadge: content.ArticleBadgeConfig(post),
		ID:           post.ID, AuthorID: post.AuthorID, Type: post.Type, Status: post.Status,
		Slug: post.Slug, Title: post.Title, Excerpt: post.Excerpt,
		ContentSource: post.ContentSource, ContentHTML: post.ContentHTML, CoverURL: post.CoverURL,
		AuthorName: post.AuthorName, Category: post.Category, Categories: contentTermResponses(post.Categories), Tags: contentTermResponses(post.Tags), CommentStatus: post.CommentStatus,
		PublishedAt: optionalAPITime(post.PublishedAt), CreatedAt: post.CreatedAt, UpdatedAt: post.UpdatedAt,
		Version: post.Version, ETag: contentAPIETag(post), Permalink: resolvedContentPath(post, paths...),
		IsPublic: content.PublicAt(post, now),
	}
}

func optionalAPITime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	result := value.UTC()
	return &result
}

func contentAPIETag(post content.Post) string {
	digest := sha256.New()
	for _, terms := range [][]content.Term{post.Categories, post.Tags} {
		for _, term := range terms {
			_, _ = fmt.Fprintf(digest, "%d:%d;", term.ID, term.Version)
		}
	}
	return fmt.Sprintf(`"content-%d-v%d-t%x"`, post.ID, post.Version, digest.Sum(nil)[:4])
}

func contentTermResponses(terms []content.Term) []contentTermAPIResponse {
	result := make([]contentTermAPIResponse, 0, len(terms))
	for _, term := range terms {
		result = append(result, contentTermAPIResponse{
			ID: term.ID, Taxonomy: term.Taxonomy, ParentID: term.ParentID,
			Slug: term.Slug, Name: term.Name, Version: term.Version,
		})
	}
	return result
}

func contentRevisionsAPIETag(post content.Post) string {
	return fmt.Sprintf(`"content-%d-revisions-v%d"`, post.ID, post.Version)
}

func contentAPIPermalink(post content.Post) string {
	if post.Type == content.TypePage {
		return "/pages/" + post.Slug
	}
	return "/posts/" + post.Slug
}

func adminContentsAPIURL(query content.AdminQuery, page, perPage, defaultPerPage int) string {
	values := make(url.Values)
	if query.Type != "" {
		values.Set("type", query.Type)
	}
	if query.Status != "" {
		values.Set("status", query.Status)
	}
	if query.Search != "" {
		values.Set("q", query.Search)
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	if perPage != defaultPerPage {
		values.Set("per_page", strconv.Itoa(perPage))
	}
	if encoded := values.Encode(); encoded != "" {
		return "/api/v1/admin/contents?" + encoded
	}
	return "/api/v1/admin/contents"
}
