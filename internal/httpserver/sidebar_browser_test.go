package httpserver

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/announcement"
	"newpress.local/newpress/internal/content"
)

// Opt-in browser fixture uses temporary users, theme settings and notice data.
func TestSidebarBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_SIDEBAR_BROWSER_FIXTURE")
	if dir == "" {
		t.Skip("isolated sidebar browser QA")
	}
	dir, _ = filepath.Abs(dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, repository, cookie, p := newAuthenticatedContentServer(t)
	now := time.Now().Add(-time.Hour)
	for i, title := range []string{"把日常，写成值得重读的故事", "从一张照片开始，发现城市之美", "慢下来，寻找生活里的好设计", "周末阅读：给自己留一段安静时光", "让灵感在这里相遇"} {
		covers := []string{"cover-library", "cover-stream", "cover-community", "cover-print", "cover-notes"}
		_, err := repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Title: title, Slug: fmt.Sprintf("sidebar-preview-%d", i), Status: content.StatusPublish, PublishedAt: now.Add(-time.Duration(i) * time.Hour), AuthorID: p.UserID, AuthorName: "NewPress 编辑部", Category: "精选阅读", Excerpt: "记录灵感，分享知识，在这里遇见值得停留的内容。", CoverURL: "/theme-assets/images/editorial/" + covers[i] + "-small.webp", ContentSource: "<p>这是用于预览主题模块的测试内容。</p>"})
		if err != nil {
			t.Fatal(err)
		}
	}
	store, err := announcement.NewFileStore(filepath.Join(dir, "announcements.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetAnnouncementStore(store)
	state, _ := store.Snapshot(t.Context())
	for i, title := range []string{"关于站点阅读体验升级的通知", "本周精选内容与专题更新", "社区交流与投稿说明"} {
		state, err = store.Save(t.Context(), state.Revision, "save", announcement.Item{Title: title, Body: "感谢你的关注。\n站点会持续更新精选内容，欢迎继续阅读与交流。", Date: now.Add(-time.Duration(i) * 24 * time.Hour), Published: true, Pinned: i == 0})
		if err != nil {
			t.Fatal(err)
		}
	}
	values := s.themes.SettingsSnapshot().Values
	keys := "notice_list,focus_list,topic_banner,entry_cards,image_grid"
	for _, key := range strings.Split(keys, ",") {
		values["home_"+key+"_visibility"] = "both"
	}
	for _, key := range []string{"welcome", "checkin", "recent", "authors", "tags", "stats", "subscribe", "hero", "notice", "channels", "categories", "series", "spotlight", "community"} {
		values["home_"+key+"_visibility"] = "hidden"
	}
	values["home_aside_order"] = keys
	values["home_topic_title"] = "设计，让生活更有趣"
	values["home_topic_summary"] = "收集灵感 · 发现好设计"
	values["home_sidebar_width"] = 340
	if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/announcements/", 303)
	})
	mux.Handle("/", handler)
	host := httptest.NewUnstartedServer(mux)
	if port := os.Getenv("NEWPRESS_SIDEBAR_BROWSER_PORT"); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			host.Listener.Close()
			t.Fatal("browser preview port must be between 1 and 65535")
		}
		listener, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			host.Listener.Close()
			t.Fatal(err)
		}
		host.Listener.Close()
		host.Listener = listener
	}
	host.Start()
	defer host.Close()
	s.config.PublicURL = host.URL
	raw, _ := json.Marshal(map[string]string{"url": host.URL, "login": host.URL + "/fixture/login"})
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("sidebar browser fixture ready", host.URL)
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
