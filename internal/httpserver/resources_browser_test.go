package httpserver

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/virtualcurrency"
)

func TestResourcesBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_RESOURCES_BROWSER") != "1" {
		t.Skip("opt-in browser fixture")
	}
	s, _, accounts, repo, adminCookie, owner := newPersistentAdminTestServer(t)
	root := t.TempDir()
	if e := extension.CopyTree(t.Context(), filepath.Join("..", "..", "themes", "chunnel"), filepath.Join(root, "chunnel")); e != nil {
		t.Fatal(e)
	}
	s.themes = theme.NewManager(root, "chunnel", theme.WithSettingsDir(t.TempDir()))
	if e := s.themes.Load(); e != nil {
		t.Fatal(e)
	}
	s.config.PublicURL = "http://127.0.0.1:19487"
	service, _, coin := commerceHTTPStore(t, s)
	service.PublicURL = s.config.PublicURL
	_, readerCookie, reader := createAndLoginTestUser(t, accounts, owner, "resource-browser-reader", "resource-browser@example.test", "林间来信", "subscriber")
	settings, _ := coin.Settings(t.Context())
	if _, e := coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner.UserID, UserID: reader.UserID, Delta: 10000, ExpectedVersion: settings.Version, RequestID: "resource-browser-fund", Reason: "资源演示余额"}); e != nil {
		t.Fatal(e)
	}
	cfg := resourceFixtureConfig()
	r := &cfg.Items[0]
	r.Badge = "限时特惠"
	r.Cover = "/theme-assets/images/cover-core.svg"
	r.Features = []string{"高清无水印", "专属资源", "多线路下载", "持续更新"}
	r.Variants[0].Body = "<p>感谢支持。包含内容日历、运营方案、活动策划模板与增长复盘表。</p>"
	r.Variants[0].Downloads[0].Code = "np26"
	r.Variants[0].Downloads[0].Password = "newpress"
	r.Variants[0].Downloads[0].URL = "https://pan.baidu.com/"
	r.Variants[0].Downloads = append(r.Variants[0].Downloads, resources.Download{ID: "quark", Name: "夸克备用线路", Provider: "quark", URL: "https://pan.quark.cn/", Code: "2026"})
	r.Variants = append(r.Variants, resources.Variant{ID: "sample", Name: "体验版", Kind: "content", Body: "<h3>内容社区的三个起点</h3><p>先确定面向谁、持续提供什么，以及如何让读者参与。</p>", Groups: []resources.Group{{Name: "回答一个问题", Conditions: []resources.Condition{{ID: "quiz", Kind: "quiz"}, {ID: "form", Kind: "form"}}}}, Questions: []resources.Question{{ID: "q", Title: "持续的优质内容，应该从什么出发？", Options: []string{"读者的真实需求", "只追求发布数量"}, Answer: "读者的真实需求"}}, Fields: []resources.Field{{ID: "name", Label: "你的称呼", Type: "text", Required: true}}, PassPercent: 100, AttemptsPerDay: 3})
	p := createWidgetPost(t, repo, content.PostInput{AuthorID: owner.UserID, AuthorName: "编辑部", Title: "让好内容，连接更多人", Slug: "resources-preview", Status: content.StatusPublish, ContentSource: "<p>在数字浪潮的推动下，内容创作与分享已成为互联网时代的重要趋势。如何打造更具价值、更具吸引力的内容社区，成为众多平台不断探索的方向。</p><p>本文从产品设计、用户体验和社区生态三个维度，深入探讨内容社区的发展方向。</p>", Resources: &cfg})
	h := s.routes()
	fixture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__resource_admin":
			http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: adminCookie.Value, Path: "/", HttpOnly: true})
			http.Redirect(w, r, fmt.Sprintf("/admin/content/%d/edit/", p.ID), 303)
		case "/__resource_reader":
			http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: readerCookie.Value, Path: "/", HttpOnly: true})
			http.Redirect(w, r, "/posts/"+p.Slug, 303)
		default:
			h.ServeHTTP(w, r)
		}
	})
	listener, e := net.Listen("tcp", "127.0.0.1:19487")
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Handler: fixture, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { server.Close() })
	go server.Serve(listener)
	t.Log("resources fixture ready: http://127.0.0.1:19487/posts/resources-preview")
	select {
	case <-time.After(45 * time.Minute):
	case <-t.Context().Done():
	}
}
