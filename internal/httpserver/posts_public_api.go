package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/theme"
)

func (s *Server) postsAPI(writer http.ResponseWriter, request *http.Request) {
	settings := s.siteSettings.Snapshot().Settings
	if !settings.API.PublicPostsEnabled {
		s.writeAPIError(writer, request, http.StatusNotFound, "request.not_found", "资源不存在", nil)
		return
	}
	query, ok := s.strictAPIQuery(writer, request, map[string]bool{"limit": true, "page": true, "q": true, "category": true, "sort": true, "author": true})
	if !ok {
		return
	}
	limit := settings.API.PublicDefaultLimit
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "limit 必须介于 1 和 100 之间", map[string]any{"field": "limit"})
			return
		}
		limit = parsed
	}
	page, pageErr := positiveQueryInt(query.Get("page"), 1, 100000)
	authorID := int64(0)
	var authorErr error
	if query.Get("author") != "" {
		authorID, authorErr = strconv.ParseInt(query.Get("author"), 10, 64)
		if authorID <= 0 {
			authorErr = errors.New("invalid author")
		}
	}
	publishedQuery, queryErr := content.NormalizePublishedQuery(content.PublishedQuery{
		Search: query.Get("q"), Category: query.Get("category"), AuthorID: authorID,
		Sort: query.Get("sort"), Limit: limit, Offset: (page - 1) * limit,
	})
	if pageErr != nil || authorErr != nil || queryErr != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "检索、作者、排序或分页参数不正确", nil)
		return
	}
	result, err := s.repository.SearchPublished(request.Context(), publishedQuery)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusInternalServerError, "content.unavailable", "内容暂时不可用", nil)
		return
	}
	items := make([]publicPostResponse, 0, len(result.Items))
	hasSnippets := false
	var bodies []string
	if settings.API.PublicIncludeBody {
		sources := make([]string, len(result.Items))
		for i, post := range result.Items {
			sources[i] = post.ContentHTML
		}
		bodies, hasSnippets = s.expandPublicSnippetBodies(request.Context(), sources)
	}
	for i, post := range result.Items {
		contentHTML := ""
		if settings.API.PublicIncludeBody {
			contentHTML = bodies[i]
		}
		items = append(items, publicPostResponse{
			Permalink:    s.contentPath(post),
			Pinned:       post.IsPinned(),
			ArticleBadge: post.ArticleBadge(),
			ID:           post.ID, AuthorID: post.AuthorID, Type: post.Type, Status: post.Status,
			Slug: post.Slug, Title: post.Title, Excerpt: post.Excerpt, ContentHTML: contentHTML,
			CoverURL: post.CoverURL, AuthorName: post.AuthorName, Category: post.Category,
			Categories: contentTermResponses(post.Categories), Tags: contentTermResponses(post.Tags),
			PublishedAt: post.PublishedAt, UpdatedAt: post.UpdatedAt, Version: post.Version,
		})
	}
	pages := max(1, (result.Total+limit-1)/limit)
	payload, err := encodePublicPosts(items, result.Total, page, limit, pages)
	if errors.Is(err, theme.ErrRenderLimit) {
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "content.response_too_large", "正文响应超过 8 MiB，请减少每页条数后重试", map[string]any{"field": "limit"})
		return
	}
	if err != nil {
		s.writeAPIError(writer, request, http.StatusInternalServerError, "response.encoding_failed", "响应暂时不可用", nil)
		return
	}
	cacheControl := publicCacheControl(settings, 0)
	if hasSnippets {
		cacheControl = "no-store"
	}
	serveGenerated(writer, request, "application/json; charset=utf-8", payload, cacheControl)
}

// Encode one bounded record at a time. The response buffer includes JSON
// escaping, metadata and the final newline; no partial page is sent on overflow.
// A template may repeat bodies too, so HTML uses the same final 8 MiB ceiling.
func encodePublicPosts(items []publicPostResponse, total, page, limit, pages int) ([]byte, error) {
	var output theme.RenderBuffer
	if _, err := output.WriteString(`{"items":[`); err != nil {
		return nil, err
	}
	for i, item := range items {
		if i > 0 {
			if _, err := output.WriteString(","); err != nil {
				return nil, err
			}
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		if _, err = output.Write(encoded); err != nil {
			return nil, err
		}
	}
	metadata, err := json.Marshal(struct {
		Count   int  `json:"count"`
		Total   int  `json:"total"`
		Page    int  `json:"page"`
		PerPage int  `json:"per_page"`
		Pages   int  `json:"pages"`
		HasMore bool `json:"has_more"`
	}{len(items), total, page, limit, pages, page < pages})
	if err != nil {
		return nil, err
	}
	for _, part := range []string{"],", string(metadata[1:]), "\n"} {
		if _, err = output.WriteString(part); err != nil {
			return nil, err
		}
	}
	return output.Bytes(), nil
}
