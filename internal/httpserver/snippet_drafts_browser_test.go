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

func TestSnippetDraftBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_SNIPPET_DRAFT_BROWSER")
	if dir == "" {
		t.Skip("set NEWPRESS_SNIPPET_DRAFT_BROWSER for isolated real UI acceptance")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, r, cookie, principal := newAuthenticatedContentServer(t)
	a := snippetAccess(principal)
	snippet, err := r.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "编辑部公共说明", ContentSource: "<p>这段固定版本的说明将复制到新草稿。</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(t.Context(), content.PostInput{Title: "独立草稿验收文章", Slug: "snippet-draft-browser", Status: content.StatusPublish, ContentSource: "<h2>正文标题</h2><p>文章自己的段落。</p>" + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__snippet_draft_login", func(w http.ResponseWriter, request *http.Request) {
		copy := *cookie
		copy.Secure = false
		http.SetCookie(w, &copy)
		http.Redirect(w, request, "/admin/content/1/edit/", http.StatusSeeOther)
	})
	mux.Handle("/", handler)
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	meta, _ := json.Marshal(map[string]any{"url": host.URL, "post_id": p.ID, "snippet_id": snippet.ID, "snippet_version": snippet.Version})
	if err = os.WriteFile(filepath.Join(dir, "fixture.json"), meta, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			t.Fatal("browser fixture not completed")
		case <-ticker.C:
			if _, err = os.Stat(filepath.Join(dir, "stop")); err != nil {
				continue
			}
			original, err := r.GetByID(t.Context(), p.ID)
			if err != nil || original.Version != p.Version || original.ContentSource != p.ContentSource || original.Title != p.Title {
				t.Fatal("source mutated", original, err)
			}
			all, err := r.ListAdmin(t.Context(), content.AdminQuery{Limit: 20})
			if err != nil || all.Total != 2 {
				t.Fatal("wrong draft count", all.Total, err)
			}
			var draft content.Post
			for _, item := range all.Items {
				if item.ID != p.ID {
					draft = item
				}
			}
			if draft.Status != content.StatusDraft || draft.Version != 1 || strings.Contains(draft.ContentSource, "data-np-snippet") || !strings.Contains(draft.ContentSource, "固定版本") {
				t.Fatal("bad independent draft", draft)
			}
			record, err := r.GetSnippet(t.Context(), snippet.ID, a)
			if err != nil || record.Visibility != content.SnippetWithdrawn {
				t.Fatal("withdrawal not tested", record, err)
			}
			state, _ := json.Marshal(map[string]any{"source_unchanged": true, "draft_id": draft.ID, "draft_version": draft.Version, "exactly_one_draft": true, "source_withdrawn": true})
			if err = os.WriteFile(filepath.Join(dir, "state-confirmation.json"), state, 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}
