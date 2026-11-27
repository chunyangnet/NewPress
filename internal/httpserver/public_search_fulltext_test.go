package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestPublicFullTextSearchHTMLAndAPI(t *testing.T) {
	_, handler, repository, _, _ := newAuthenticatedContentServer(t)
	for _, input := range []content.PostInput{
		{Title: "Body match", Slug: "full-body", Status: content.StatusPublish, ContentSource: "<p>独有正<em>文</em>标记</p>"},
		{Title: "独有正文标记 title", Slug: "full-title", Status: content.StatusPublish, ContentSource: "<p>other</p>", PublishedAt: time.Now().Add(-24 * time.Hour)},
		{Title: "Private body", Slug: "full-private", Status: content.StatusPrivate, ContentSource: "<p>独有正文标记 私密独有</p>"},
	} {
		input.Excerpt = "explicit unrelated summary"
		if _, err := repository.Create(t.Context(), input); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "https://example.test"+path, nil))
		return w
	}
	w := get("/api/v1/posts?q=独有正文标记&limit=1")
	var page struct {
		Items []publicPostResponse `json:"items"`
		Total int                  `json:"total"`
	}
	decodeAPIResponse(t, w, &page)
	if w.Code != 200 || page.Total != 2 || len(page.Items) != 1 || page.Items[0].Slug != "full-title" {
		t.Fatal(w.Code, page)
	}
	w = get("/api/v1/posts?q=独有正文标记&limit=1&page=2")
	decodeAPIResponse(t, w, &page)
	if w.Code != 200 || page.Total != 2 || len(page.Items) != 1 || page.Items[0].Slug != "full-body" {
		t.Fatal(w.Code, page)
	}
	w = get("/explore/?q=独有正文标记")
	markup := w.Body.String()
	if w.Code != 200 || !strings.Contains(markup, `value="relevance" selected`) || !strings.Contains(markup, `href="/posts/full-body"`) || strings.Contains(markup, `href="/posts/full-private"`) {
		t.Fatal("full search HTML", w.Code, markup)
	}
	w = get("/api/v1/posts?q=私密独有")
	decodeAPIResponse(t, w, &page)
	if w.Code != 200 || page.Total != 0 {
		t.Fatal("private match", w.Code, page)
	}
	// Explicit publication order remains available, including with no query.
	w = get("/api/v1/posts?q=独有正文标记&sort=published&limit=1")
	decodeAPIResponse(t, w, &page)
	if w.Code != 200 || len(page.Items) != 1 || page.Items[0].Slug != "full-body" {
		t.Fatal(w.Code, page)
	}
	if w = get("/explore/?sort=relevance"); w.Code != 200 {
		t.Fatal("empty query fallback", w.Code)
	}
}
