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
)

// Opt-in browser QA uses an isolated repository and authentication session.
func TestArticleBadgeBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_ARTICLE_BADGE_BROWSER_FIXTURE")
	if dir == "" {
		t.Skip("browser fixture only")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, repo, cookie, _ := newAuthenticatedContentServer(t)
	var presets []content.ArticleBadgePreset
	for _, group := range content.ArticleBadgePresets() {
		presets = append(presets, group.Presets...)
	}
	titles := []string{"把日常写成值得收藏的文章", "在文字与光影之间，重新发现生活", "给创作者的一份灵感手册", "周末计划：留一点时间给自己", "用设计，让阅读成为一种享受", "我们为什么仍然需要认真写作"}
	var editorID int64
	for i := len(presets) - 1; i >= 0; i-- {
		b := presets[i].ArticleBadge
		pinned := b.Kind == "pinned"
		p, err := repo.Create(t.Context(), content.PostInput{
			Pinned: &pinned,
			Type:   content.TypePost, Status: content.StatusPublish, Title: titles[i%len(titles)], Slug: "badge-" + b.Kind,
			Excerpt: "记录观察，分享想法。让每一篇认真创作的内容，都拥有自己的辨识度。", ContentSource: "<h2>从一个小标记开始</h2><p>这是文章角标的独立测试内容，用于检查保存、预览和不同屏幕下的展示效果。</p>",
			CoverURL: []string{"/theme-assets/images/editorial/cover-notes-small.webp", "/theme-assets/images/editorial/cover-library-small.webp", "/theme-assets/images/editorial/cover-print-small.webp"}[i%3],
			Category: "创作与生活", AuthorName: "NewPress 编辑部", CommentStatus: content.CommentOpen, ArticleBadge: &b,
		})
		if err != nil {
			t.Fatal(err)
		}
		editorID = p.ID
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, fmt.Sprintf("/admin/content/%d/edit/", editorID), http.StatusSeeOther)
	})
	mux.Handle("/", handler)
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	raw, _ := json.Marshal(map[string]any{"url": host.URL, "login": host.URL + "/fixture/login", "editor": fmt.Sprintf("%s/admin/content/%d/edit/", host.URL, editorID)})
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("article badge fixture ready", host.URL)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
				return
			}
		}
	}
}
