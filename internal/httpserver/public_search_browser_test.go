package httpserver

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

// Opt-in browser fixture serves the actual theme and public search endpoint.
// It uses isolated memory content and temporary settings without a user session.
func TestPublicSearchBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_PUBLIC_SEARCH_BROWSER")
	if directory == "" {
		t.Skip("set NEWPRESS_PUBLIC_SEARCH_BROWSER to an isolated output directory")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, repository, _, _ := newAuthenticatedContentServer(t)
	var posts []content.Post
	for _, input := range []content.PostInput{
		{Title: "正文命中示例", Slug: "search-body", ContentSource: "<p>独有正<em>文</em>标记</p>", PublishedAt: time.Now().Add(-time.Hour)},
		{Title: "独有正文标记：标题优先", Slug: "search-title", ContentSource: "<p>其他正文</p>", PublishedAt: time.Now().Add(-24 * time.Hour)},
		{Title: "私密内容", Slug: "search-private", Status: content.StatusPrivate, ContentSource: "<p>独有正文标记 私密独有</p>"},
	} {
		if input.Status == "" {
			input.Status = content.StatusPublish
		}
		input.Excerpt = "固定摘要，不含检索词。"
		post, err := repository.Create(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		posts = append(posts, post)
	}
	state := func() []byte {
		t.Helper()
		var current []any
		for _, post := range posts {
			item, err := repository.GetByID(t.Context(), post.ID)
			if err != nil {
				t.Fatal(err)
			}
			revisions, err := repository.ListRevisions(t.Context(), post.ID, 10)
			if err != nil {
				t.Fatal(err)
			}
			current = append(current, item, revisions)
		}
		raw, err := json.Marshal(current)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := string(state())
	host := httptest.NewServer(handler)
	defer host.Close()
	s.config.PublicURL = host.URL
	raw, _ := json.Marshal(map[string]string{"url": host.URL})
	if err := os.WriteFile(filepath.Join(directory, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			t.Fatal("browser fixture cancelled before read-only verification")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(directory, "stop")); err == nil {
				if string(state()) != before {
					t.Fatal("browser flow changed content or revisions")
				}
				if err := os.WriteFile(filepath.Join(directory, "read-only-confirmation.json"), []byte(`{"content_unchanged":true,"revisions_unchanged":true}`), 0600); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
	}
}
