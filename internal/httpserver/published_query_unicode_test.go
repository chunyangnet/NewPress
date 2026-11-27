package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

type unicodeQueryRepository struct {
	content.Repository
	queries []content.PublishedQuery
}

func (r *unicodeQueryRepository) SearchPublished(ctx context.Context, query content.PublishedQuery) (content.PublishedPage, error) {
	r.queries = append(r.queries, query)
	return r.Repository.SearchPublished(ctx, query)
}

func TestPublishedSearchHTTPRejectsInvalidUTF8Fields(t *testing.T) {
	server, handler, repository, _, _ := newAuthenticatedContentServer(t)
	counted := &unicodeQueryRepository{Repository: repository}
	server.repository = counted
	for _, endpoint := range []string{"/explore/", "/api/v1/posts"} {
		for _, query := range []string{
			"q=%C2&category=%A9",
			"q=%E4&category=%B8%AD",
			"q=%E4%B8&category=%AD",
			"q=%F0&category=%9F%8C%90",
			"q=%F0%9F&category=%8C%90",
			"q=%F0%9F%8C&category=%90",
			"q=%20%C2%20&category=%20%A9%20",
			"q=%FF", "category=%FF",
			"q=before%00after", "category=before%00after",
		} {
			t.Run(endpoint+"/"+query, func(t *testing.T) {
				counted.queries = nil
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://example.test"+endpoint+"?"+query, nil))
				if response.Code != http.StatusBadRequest {
					t.Errorf("invalid query returned %d, want 400", response.Code)
				}
				if len(counted.queries) != 0 {
					t.Errorf("invalid query reached content search: %#v", counted.queries)
				}
				if endpoint == "/api/v1/posts" {
					var envelope apiErrorEnvelope
					if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != "request.invalid_query" {
						t.Errorf("API validation error = %s, %v", response.Body, err)
					}
				}
			})
		}
	}
}

func TestPublishedSearchHTTPPreservesUnicodeFilters(t *testing.T) {
	server, handler, repository, _, _ := newAuthenticatedContentServer(t)
	const category = "编码指南 🌐 �"
	post, err := repository.Create(t.Context(), content.PostInput{
		Title: "中文 🌐 �", Slug: "unicode-search-result", Category: category,
		Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	counted := &unicodeQueryRepository{Repository: repository}
	server.repository = counted
	for _, endpoint := range []string{"/explore/", "/api/v1/posts"} {
		for _, needle := range []string{"中文", "🌐", "�"} {
			t.Run(endpoint+"/"+needle, func(t *testing.T) {
				counted.queries = nil
				query := url.Values{"q": {" \u3000" + needle + "\t"}, "category": {" " + category + " "}}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://example.test"+endpoint+"?"+query.Encode(), nil))
				if response.Code != http.StatusOK {
					t.Fatalf("valid Unicode query returned %d", response.Code)
				}
				if len(counted.queries) != 1 || counted.queries[0].Search != needle || counted.queries[0].Category != category {
					t.Fatalf("Unicode query changed before content search: %#v", counted.queries)
				}
				if endpoint == "/api/v1/posts" {
					var payload struct {
						Items []publicPostResponse `json:"items"`
						Total int                  `json:"total"`
					}
					decodeAPIResponse(t, response, &payload)
					if payload.Total != 1 || len(payload.Items) != 1 || payload.Items[0].ID != post.ID {
						t.Errorf("Unicode API results = %#v", payload)
					}
				} else if !strings.Contains(response.Body.String(), `href="/posts/unicode-search-result"`) {
					t.Error("Unicode search result missing from HTML")
				}
			})
		}
	}
}
