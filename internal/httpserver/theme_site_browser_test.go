package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/announcement"
	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/feedback"
	"newpress.local/newpress/internal/linkpage"
	"newpress.local/newpress/internal/questions"
	"newpress.local/newpress/internal/shop"
	"newpress.local/newpress/internal/theme"
)

// Use the real application routes and installed template/asset contracts, with
// disposable stores. This does not substitute presentation fixtures for pages.
func TestThemeSiteBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_THEME_SITE_BROWSER_DIR")
	if dir == "" {
		t.Skip("opt-in actual theme site browser fixture")
	}
	if !filepath.IsAbs(dir) {
		t.Fatal("absolute evidence directory required")
	}
	slug := os.Getenv("NEWPRESS_THEME_SITE_THEME")
	if slug != "chunnel" && slug != "docshub" && slug != "nexora" {
		t.Fatal("choose chunnel, docshub or nexora")
	}
	s, _, accounts, repo, ownerCookie, owner := newPersistentAdminTestServer(t)
	reader, readerCookie, _ := createAndLoginTestUser(t, accounts, owner, "theme-reader", "theme-reader@example.test", "整站验收读者", "subscriber")
	root, storage := t.TempDir(), t.TempDir()
	if err := extension.CopyTree(t.Context(), filepath.Join("..", "..", "themes", slug), filepath.Join(root, slug)); err != nil {
		t.Fatal(err)
	}
	s.themes = theme.NewManager(root, slug, theme.WithSettingsDir(t.TempDir()))
	if err := s.themes.Load(); err != nil {
		t.Fatal(err)
	}
	s.config.ActiveTheme, s.config.ThemesDir, s.config.CommunityEnabled = slug, root, true
	commerce, _, _ := commerceHTTPStore(t, s)

	tabs := articlewidgets.Config{Version: 1, ID: "site_fixture_tabs", Kind: "tabs", Title: "阅读分组", Items: []articlewidgets.Item{{Title: "概述", Text: "第一组实际正文"}, {Title: "步骤", Text: "第二组实际正文"}}}
	body := "<h2>开始阅读</h2><p>这是一篇整站验收文章，包含中文、English 和数字 2026。</p>" + strings.Repeat("<p>阅读内容与目录应保持一致，窄屏下文字应自然换行。</p>", 12) + "<h2>代码示例</h2><pre><code class=\"language-typescript\">const greeting: string = '你好';</code></pre>" + componentSource(tabs) + "<h2>继续探索</h2><p>最后一节正文。</p>"
	posts := []content.Post{}
	for i := 0; i < 28; i++ {
		post := createWidgetPost(t, repo, content.PostInput{Type: content.TypePost, Status: content.StatusPublish, AuthorID: owner.UserID, AuthorName: owner.DisplayName,
			Title: fmt.Sprintf("整站验收文章 %02d", i), Slug: fmt.Sprintf("theme-site-%d", i), ContentSource: body, Category: "reading", TagNames: []string{"integration"}, CommentStatus: content.CommentOpen, PublishedAt: time.Now().Add(-time.Duration(i+1) * time.Hour)})
		posts = append(posts, post)
	}
	page := createWidgetPost(t, repo, content.PostInput{Type: content.TypePage, Status: content.StatusPublish, AuthorID: owner.UserID, AuthorName: owner.DisplayName, Title: "关于整站验收", Slug: "theme-about", ContentSource: body})
	flash := createWidgetPost(t, repo, content.PostInput{Type: content.TypeFlash, Status: content.StatusPublish, AuthorID: owner.UserID, AuthorName: owner.DisplayName, Title: "整站快报", Slug: "theme-flash", ContentSource: "真实快报正文与来源说明。", Flash: &content.FlashFields{SourceName: "本地验收", SourceURL: "https://example.test/source"}, PublishedAt: time.Now().Add(-time.Hour)})
	createOwnedTestPost(t, repo, owner.UserID, owner.DisplayName, "theme-private-draft", "PRIVATE-THEME-DRAFT")
	series, err := repo.SaveSeries(t.Context(), 0, content.Mutation{Actor: owner.Username}, content.SeriesInput{Kind: content.SeriesKindSequence, Status: content.StatusPublish, Title: "整站阅读选集", Slug: "theme-series", Description: "按顺序阅读文章。", PostIDs: []int64{posts[0].ID, posts[1].ID, posts[2].ID}})
	if err != nil {
		t.Fatal(err)
	}
	parentComment, err := s.comments.Create(t.Context(), comment.CreateInput{PostID: posts[0].ID, AuthorName: "评论读者", AuthorEmail: "reply@example.test", Status: comment.StatusApproved, ContentSource: "已有评论，用于核对回复与取消。"})
	if err != nil {
		t.Fatal(err)
	}

	circles, err := community.NewFileStore(filepath.Join(storage, "circles.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetCommunityStore(circles)
	files, err := community.NewFiles(filepath.Join(storage, "files"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetCommunityFiles(files)
	circle, err := circles.Apply(t.Context(), "theme-circle", community.Actor{ID: owner.UserID, Admin: true}, community.Command{Action: "create", Key: "theme-circle-create", OwnerID: owner.UserID, Name: "整站交流圈", Description: "阅读和创作交流。", Rules: "友善交流。", Visibility: "public", JoinPolicy: "open"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	joined, err := circles.Apply(t.Context(), "theme-circle", community.Actor{ID: reader.ID}, community.Command{Action: "join", Key: "theme-circle-join", Version: circle.Version}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	topic, err := circles.Apply(t.Context(), "theme-circle", community.Actor{ID: owner.UserID, Admin: true}, community.Command{Action: "topic", Key: "theme-circle-topic", Version: joined.Version, Title: "整站交流话题", Body: "## 正文\n\n保留真实阅读与讨论。", Kind: "discussion", Format: "markdown"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	qa, err := questions.NewFileStore(filepath.Join(storage, "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetQuestionsStore(qa)
	question, err := qa.Apply(t.Context(), 0, questions.Actor{ID: reader.ID}, questions.Command{Action: "create", Key: "theme-question", Title: "如何验证整站阅读体验？", Body: "请分享目录、搜索和窄屏操作的经验。", Tags: []string{"阅读"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := qa.Apply(t.Context(), question.ID, questions.Actor{ID: owner.UserID}, questions.Command{Action: "answer", Key: "theme-answer", Version: question.Version, Body: "从真实页面进入，再核对内容与交互。"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	boards, err := feedback.NewFileStore(filepath.Join(storage, "feedback.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetFeedbackStore(boards)
	boardSettings := feedback.Defaults()
	boardSettings.Name, boardSettings.Description, boardSettings.Moderation = "整站反馈", "阅读体验意见。", false
	board, err := boards.Apply(t.Context(), "theme-feedback", feedback.Actor{ID: owner.UserID, Admin: true}, feedback.Command{Action: "create", Key: "theme-site-create-board", Settings: &boardSettings}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	feedbackItem, err := boards.Apply(t.Context(), "theme-feedback", feedback.Actor{ID: owner.UserID, Admin: true}, feedback.Command{Action: "submit", Key: "theme-feedback-item", Version: board.Version, Title: "让阅读更方便", Body: "保留清楚的目录和操作提示。", Kind: "suggestion"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	links, err := linkpage.NewFileStore(filepath.Join(storage, "links.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetLinkPageStore(links)
	linkSettings := linkpage.Defaults()
	linkSettings.Name, linkSettings.Description = "整站友链", "相关站点。"
	linkBoard, err := links.Apply(t.Context(), "theme-links", linkpage.Actor{ID: owner.UserID, Admin: true}, linkpage.Command{Action: "create", Key: "theme-site-create-links", ExpectedUserID: owner.UserID, Settings: &linkSettings}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := links.Apply(t.Context(), "theme-links", linkpage.Actor{ID: owner.UserID, Admin: true}, linkpage.Command{Action: "save_item", Key: "theme-site-save-link-item", ExpectedUserID: owner.UserID, Version: linkBoard.Version, Item: &linkpage.Item{Name: "验收资料", URL: "https://example.test/reference", Description: "公开资料入口。", Enabled: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	notices, err := s.announcements.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	notices, err = s.announcements.Save(t.Context(), notices.Revision, "save", announcement.Item{Title: "整站验收公告", Body: "本站正在核对阅读和交互体验。", Published: true, Date: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	shopBackend, err := shop.NewFileBackend(filepath.Join(storage, "shop.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetShopService(&shop.Service{Backend: shopBackend})
	catalog := shop.InitialState().Catalog
	catalog.Enabled = true
	catalog.Products = []shop.Product{{Key: "theme-product", Title: "整站验收资料", Summary: "本地测试商品。", Kind: "virtual", Mode: "direct", Enabled: true, Currency: "CNY", Price: 100, Stock: 10, DescriptionSource: "<p>测试商品说明。</p>" + componentSource(tabs), DescriptionAuthorID: owner.UserID}}
	if _, err := s.shop.SaveCatalog(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}

	readState := func() []byte {
		t.Helper()
		state := map[string]any{}
		for _, id := range []int64{owner.UserID, reader.ID} {
			account, err := accounts.GetUser(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			state[fmt.Sprintf("account-%d", id)] = account
		}
		for _, kind := range []string{content.TypePost, content.TypePage, content.TypeFlash} {
			items, err := repo.ListAdmin(t.Context(), content.AdminQuery{Type: kind, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			state[kind] = items
			for _, post := range items.Items {
				revisions, err := repo.ListRevisions(t.Context(), post.ID, 100)
				if err != nil {
					t.Fatal(err)
				}
				state[fmt.Sprintf("revisions-%d", post.ID)] = revisions
			}
		}
		allSeries, err := repo.ListSeries(t.Context(), content.SeriesQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		state["series"] = allSeries
		comments, err := s.comments.ListAdmin(t.Context(), comment.Query{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		state["comments"] = comments
		commerceState, err := commerce.Backend.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		state["commerce"] = commerceState
		state["site_settings"] = s.siteSettings.Snapshot()
		values := s.themes.SettingsSnapshot().Values
		delete(values, "visual_style")
		state["theme_settings_except_fixture_style"] = values
		if err := filepath.WalkDir(storage, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(storage, path)
			state[relative] = string(data)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := readState()
	var escaped atomic.Int64
	handler := s.routes()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__theme_site/login/{role}", func(w http.ResponseWriter, r *http.Request) {
		cookie := readerCookie
		if r.PathValue("role") == "owner" {
			cookie = ownerCookie
		}
		if _, err := accounts.ResolveSession(r.Context(), cookie.Value); err != nil {
			http.Error(w, "fixture session expired; start a fresh fixture", 401)
			return
		}
		http.SetCookie(w, cookie)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /__theme_site/style/{style}", func(w http.ResponseWriter, r *http.Request) {
		style := r.PathValue("style")
		if slug != "chunnel" || (style != "classic" && style != "liquid-glass") {
			http.NotFound(w, r)
			return
		}
		values := s.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) != 0 {
			http.Error(w, "invalid fixture style", 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			escaped.Add(1)
			http.Error(w, "browser business writes must be intercepted", 403)
			return
		}
		handler.ServeHTTP(w, r)
	})
	host := httptest.NewServer(mux)
	t.Cleanup(host.Close)
	s.config.PublicURL = host.URL
	commerce.PublicURL = host.URL
	type pageCase struct {
		Key    string `json:"key"`
		Path   string `json:"path"`
		Status int    `json:"status"`
		Role   string `json:"role,omitempty"`
		Marker string `json:"marker,omitempty"`
	}
	pages := []pageCase{
		{"home", "/", 200, "", ""}, {"explore", "/explore/", 200, "", ""}, {"search", "/explore/?q=整站验收", 200, "", posts[0].Title},
		{"article", "/posts/" + posts[0].Slug, 200, "", posts[0].Title}, {"page", "/pages/" + page.Slug, 200, "", page.Title},
		{"category", "/categories/reading/", 200, "", posts[0].Title}, {"tag", "/tags/integration/", 200, "", posts[0].Title}, {"author", fmt.Sprintf("/authors/%d/", owner.UserID), 200, "", posts[0].Title},
		{"series", "/series/", 200, "", series.Title}, {"series-detail", "/series/theme-series/", 200, "", series.Title},
		{"community", "/community/", 200, "", "整站交流圈"}, {"circle", "/community/theme-circle/", 200, "", "整站交流圈"}, {"topic", fmt.Sprintf("/community/theme-circle/topics/%d", topic.TopicID), 200, "", "整站交流话题"},
		{"questions", "/questions/", 200, "", "如何验证整站阅读体验"}, {"question", fmt.Sprintf("/questions/%d/", question.ID), 200, "", "如何验证整站阅读体验"}, {"question-new", "/questions/new/", 200, "reader", ""},
		{"feedback", "/feedback/", 200, "", "整站反馈"}, {"feedback-board", "/feedback/theme-feedback/", 200, "", "让阅读更方便"}, {"feedback-item", fmt.Sprintf("/feedback/theme-feedback/items/%d", feedbackItem.ItemID), 200, "", "让阅读更方便"},
		{"links", "/links/", 200, "", "整站友链"}, {"links-board", "/links/theme-links/", 200, "", "验收资料"},
		{"flash", "/flash/", 200, "", flash.Title}, {"flash-single", "/flash/" + flash.Slug + "/", 200, "", flash.Title}, {"flash-submit", "/flash/submit/", 200, "reader", ""},
		{"announcements", "/announcements/", 200, "", "整站验收公告"},
		{"shop", "/shop/", 200, "", "整站验收资料"}, {"product", "/shop/product/theme-product/", 200, "", "整站验收资料"}, {"orders", "/shop/orders/", 200, "reader", ""},
		{"account", "/account/", 200, "reader", ""}, {"access", "/account/access/", 200, "reader", ""}, {"billing", "/billing/", 200, "reader", ""},
		{"login", "/admin/login/", 200, "", ""}, {"admin", "/admin/", 200, "owner", ""}, {"editor", "/admin/content/new/", 200, "owner", ""},
		{"not-found", "/theme-site-not-found/", 404, "", ""},
	}
	if slug != "chunnel" {
		pages[1].Marker = "整站验收文章"
	}
	write := func(name string, value any) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		encodeErr, closeErr := json.NewEncoder(file).Encode(value), file.Close()
		if encodeErr != nil || closeErr != nil {
			t.Fatalf("evidence: %v / %v", encodeErr, closeErr)
		}
	}
	write("fixture.json", map[string]any{"base": host.URL, "theme": slug, "version": s.themes.Active().Version, "pages": pages, "post_id": posts[0].ID, "comment_id": parentComment.ID, "reader_id": reader.ID, "owner_id": owner.UserID, "source_root": root})
	t.Log("actual theme site fixture ready", slug, host.URL)
	ticker, deadline := time.NewTicker(350*time.Millisecond), time.NewTimer(45*time.Minute)
	defer ticker.Stop()
	defer deadline.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(dir, "stop")); err != nil {
				continue
			}
			after := readState()
			if !reflect.DeepEqual(before, after) || escaped.Load() != 0 {
				t.Fatalf("fixture state changed or %d writes escaped", escaped.Load())
			}
			hash := sha256.Sum256(after)
			write("read-only-confirmation.json", map[string]any{"accounts_content_revisions_comments_and_stores_unchanged": true, "theme_settings_unchanged_except_explicit_style": true, "unintercepted_writes": escaped.Load(), "state_sha256": hex.EncodeToString(hash[:])})
			return
		case <-deadline.C:
			t.Fatal("explicit stop required within 45 minutes")
		case <-t.Context().Done():
			t.Fatal("fixture interrupted before explicit stop")
		}
	}
}
