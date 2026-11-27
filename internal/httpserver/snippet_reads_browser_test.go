package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestSnippetReadBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_SNIPPET_READ_BROWSER")
	if dir == "" {
		t.Skip("set NEWPRESS_SNIPPET_READ_BROWSER to an isolated output directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, r, cookie, principal := newAuthenticatedContentServer(t)
	a := snippetAccess(principal)
	var snippet content.Snippet
	for i := 1; i <= 30; i++ {
		var err error
		snippet, err = r.SaveSnippet(t.Context(), snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, content.SnippetInput{Title: "分页验收片段", ContentSource: fmt.Sprintf("<p>片段第 %d 版正文</p>", i), Visibility: content.SnippetShared}, a)
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := r.Create(t.Context(), content.PostInput{Title: "分页验收文章", Slug: "snippet-reads-browser", ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__snippet_fixture_login", func(w http.ResponseWriter, r *http.Request) {
		copy := *cookie
		copy.Secure = false
		http.SetCookie(w, &copy)
		http.Redirect(w, r, "/admin/snippets/?edit=1", http.StatusSeeOther)
	})
	mux.Handle("/", handler)
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	raw, _ := json.Marshal(map[string]any{"url": host.URL, "snippet_id": snippet.ID, "post_id": p.ID, "versions": 30})
	if err = os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
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
			if current.Version != p.Version || current.ContentSource != p.ContentSource || record.Version != 30 || len(record.Revisions) != 30 {
				t.Fatal("read flow changed canonical state")
			}
			raw, _ = json.Marshal(map[string]any{"post_version": current.Version, "snippet_version": record.Version, "snippet_revisions": len(record.Revisions), "read_flow_preserved_canonical_state": true})
			if err = os.WriteFile(filepath.Join(dir, "state-confirmation.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}
