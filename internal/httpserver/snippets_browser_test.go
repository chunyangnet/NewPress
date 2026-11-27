package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

// Isolated loopback fixture for the real admin UI. No credentials are written
// to artifacts; a test-only route installs the existing temporary session.
func TestSnippetBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_SNIPPET_BROWSER")
	if dir == "" {
		t.Skip("set NEWPRESS_SNIPPET_BROWSER to an isolated output directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, r, cookie, principal := newAuthenticatedContentServer(t)
	a := snippetAccess(principal)
	snippet, err := r.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "引用验收片段", ContentSource: "<p>片段第一版独有文字</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(t.Context(), content.PostInput{Title: "引用验收文章", Slug: "snippet-browser", Status: content.StatusPublish, Excerpt: "固定摘要", ContentSource: "<p>文章自己的段落</p>" + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: snippet.CurrentVersion})})
	if err != nil {
		t.Fatal(err)
	}
	snippet, err = r.SaveSnippet(t.Context(), snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, content.SnippetInput{Title: "引用验收片段", ContentSource: "<p>片段第二版独有文字</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	settings := s.siteSettings.Snapshot()
	settings.Settings.API.PublicIncludeBody = true
	if _, fields, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__snippet_fixture_login", func(w http.ResponseWriter, r *http.Request) {
		copy := *cookie
		copy.Secure = false
		http.SetCookie(w, &copy)
		http.Redirect(w, r, "/admin/snippets/", http.StatusSeeOther)
	})
	mux.Handle("/", handler)
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	metadata, _ := json.Marshal(map[string]any{"url": host.URL, "snippet_id": snippet.ID, "post_id": p.ID, "initial_post_version": p.Version})
	if err = os.WriteFile(filepath.Join(dir, "fixture.json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			t.Fatal("browser fixture stopped before final verification")
		case <-ticker.C:
			if _, err = os.Stat(filepath.Join(dir, "stop")); err != nil {
				continue
			}
			current, err := r.GetByID(t.Context(), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			record, err := r.GetSnippet(t.Context(), snippet.ID, a)
			if err != nil {
				t.Fatal(err)
			}
			if current.Version != p.Version+1 || current.Title != p.Title || current.Status != p.Status || strings.Contains(current.ContentSource, "片段第二版") || !strings.Contains(current.ContentSource, `data-np-snippet-version="2"`) || record.Visibility != content.SnippetWithdrawn {
				t.Fatal("browser final state did not match intended update/withdrawal", current, record.Snippet)
			}
			revisions, err := r.ListRevisions(t.Context(), p.ID, 10)
			if err != nil || len(revisions) != 2 {
				t.Fatal(revisions, err)
			}
			raw, _ := json.Marshal(map[string]any{"post_version": current.Version, "post_revisions": len(revisions), "snippet": record.Snippet, "one_article_update": true, "reference_only": true, "title_status_preserved": true})
			if err = os.WriteFile(filepath.Join(dir, "state-confirmation.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}
