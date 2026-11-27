package httpserver

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
)

var textSearchEndpoints = []string{
	"/admin/content/", "/api/v1/admin/contents", "/admin/taxonomy/",
	"/series/", "/api/v1/series", "/admin/series/", "/api/v1/admin/series",
	"/api/v1/admin/series/sources", "/api/v1/admin/seo/sources",
	"/admin/seo/", "/api/v1/admin/redirects",
}

func TestContentListHTTPRejectsInvalidSearchText(t *testing.T) {
	_, handler, _, cookie, _ := newAuthenticatedContentServer(t)
	for _, endpoint := range textSearchEndpoints {
		for name, search := range map[string]string{
			"bad_byte": "\xff", "truncated": "\xe4\xb8", "NUL": "before\x00after", "C1_control": "before\u0085after",
		} {
			t.Run(endpoint+"/"+name, func(t *testing.T) {
				response := serveContentAPI(handler, cookie, http.MethodGet, "https://example.test"+endpoint+"?q="+url.QueryEscape(search), "", nil)
				status := http.StatusBadRequest
				// Redirect queries retain their existing domain-validation mapping.
				if endpoint == "/admin/seo/" || endpoint == "/api/v1/admin/redirects" {
					status = http.StatusUnprocessableEntity
				}
				if response.Code != status {
					t.Fatalf("invalid search returned %d, want %d", response.Code, status)
				}
			})
		}
	}
}

func TestContentListHTTPPreservesUnicodeSearch(t *testing.T) {
	_, handler, repository, cookie, _ := newAuthenticatedContentServer(t)
	const marker = "unicode-query-marker"
	const title = marker + " 查询结果 中文 🌐 �"
	post, err := repository.Create(t.Context(), content.PostInput{Title: title, Slug: marker, Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateTerm(t.Context(), content.TermInput{Taxonomy: content.TaxonomyCategory, Name: title, Slug: marker}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SaveSeries(t.Context(), 0, content.Mutation{}, content.SeriesInput{Kind: content.SeriesKindTopic, Status: content.StatusPublish, Title: title, Slug: marker, PostIDs: []int64{post.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SaveRedirect(t.Context(), 0, content.Mutation{}, content.RedirectInput{Path: "/legacy/中文/🌐/�/" + marker, PostID: post.ID}); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range textSearchEndpoints {
		for _, needle := range []string{"中文", "🌐", "�"} {
			t.Run(endpoint+"/"+needle, func(t *testing.T) {
				response := serveContentAPI(handler, cookie, http.MethodGet, "https://example.test"+endpoint+"?q="+url.QueryEscape(" \u3000"+needle+"\t"), "", nil)
				if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), marker) {
					t.Fatalf("Unicode search returned %d, marker present=%v", response.Code, strings.Contains(response.Body.String(), marker))
				}
			})
		}
	}
}
