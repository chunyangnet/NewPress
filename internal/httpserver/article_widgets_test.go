package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func widgetSource(id int64) string {
	return fmt.Sprintf(`<aside data-np-widget="post" data-np-post="%d" data-np-layout="card"><p><a href="/posts/forged">伪造旧信息</a></p></aside>`, id)
}

func createWidgetPost(t *testing.T, repository *content.MemoryRepository, input content.PostInput) content.Post {
	t.Helper()
	if input.ContentSource == "" {
		input.ContentSource = "<p>真实正文，不会嵌入卡片</p>"
	}
	if input.Excerpt == "" {
		input.Excerpt = "公开摘要"
	}
	post, err := repository.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	return post
}

func TestArticleWidgetsResolveCurrentPublicMetadataAndHideWithdrawnPosts(t *testing.T) {
	s, handler, repository, _, _ := newAuthenticatedContentServer(t)
	child := createWidgetPost(t, repository, content.PostInput{Title: "原文章标题", Slug: "widget-child", Status: content.StatusPublish, AuthorName: "公开作者", CoverURL: "/theme-assets/images/cover-core.svg"})
	parent := createWidgetPost(t, repository, content.PostInput{Title: "引用文章", Slug: "widget-parent", Status: content.StatusPublish, ContentSource: "<p>正文前</p>\n" + widgetSource(child.ID) + "\n<p>正文后</p>"})
	for _, preview := range []bool{false, true} {
		data := pageView{Post: parent, IsPreview: preview}
		s.prepareArticleWidgets(t.Context(), &data)
		if !strings.Contains(data.Post.ContentHTML, "原文章标题") || !strings.Contains(data.Post.ContentHTML, `href="/posts/widget-child"`) || strings.Contains(data.Post.ContentHTML, "伪造旧信息") || strings.Contains(data.Post.ContentHTML, "真实正文") {
			t.Fatalf("bad public/preview widget: %s", data.Post.ContentHTML)
		}
		if !strings.HasPrefix(data.Post.ContentHTML, "<p>正文前</p>\n") || !strings.HasSuffix(data.Post.ContentHTML, "\n<p>正文后</p>") {
			t.Fatal("surrounding source changed")
		}
		if got := contenthtml.Sanitize(data.Post.ContentHTML); !strings.Contains(got, `data-np-part="title"`) || strings.Contains(got, "<script") {
			t.Fatal("render-time sanitizer lost contract")
		}
	}
	input := inputFromPost(child)
	input.Title, input.Slug = "更新后的标题", "widget-renamed"
	child, err := repository.Update(t.Context(), child.ID, content.Mutation{ExpectedVersion: child.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	response := serveCookieRequest(handler, http.MethodGet, "https://example.test/posts/"+parent.Slug, nil, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "更新后的标题") || !strings.Contains(response.Body.String(), `/posts/widget-renamed`) || !strings.Contains(response.Body.String(), "newpress-article-widgets.css") {
		t.Fatalf("public page did not resolve current widget: %d", response.Code)
	}
	if _, err := repository.ChangeStatus(t.Context(), child.ID, content.Mutation{ExpectedVersion: child.Version}, content.StatusPrivate); err != nil {
		t.Fatal(err)
	}
	response = serveCookieRequest(handler, http.MethodGet, "https://example.test/posts/"+parent.Slug, nil, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "文章暂不可用") || strings.Contains(response.Body.String(), "更新后的标题") || strings.Contains(response.Body.String(), "伪造旧信息") {
		t.Fatalf("withdrawn metadata survived: %d", response.Code)
	}
	unchanged, _ := repository.GetByID(t.Context(), parent.ID)
	if unchanged.ContentSource != parent.ContentSource || unchanged.Version != parent.Version {
		t.Fatal("display projection changed canonical content")
	}
}

type widgetCountingRepository struct {
	content.Repository
	calls    int
	ids      []int64
	override *content.PublishedPage
}

func (r *widgetCountingRepository) SearchPublished(ctx context.Context, query content.PublishedQuery) (content.PublishedPage, error) {
	r.calls++
	r.ids = append([]int64(nil), query.PostIDs...)
	if r.override != nil {
		return *r.override, nil
	}
	return r.Repository.SearchPublished(ctx, query)
}

func TestArticleWidgetsBoundRepeatedExpansionAndOutputBytes(t *testing.T) {
	repository := content.NewMemoryRepository(false)
	post := createWidgetPost(t, repository, content.PostInput{Title: "公开文章", Slug: "widget-repeated", Status: content.StatusPublish})
	counting := &widgetCountingRepository{Repository: repository}
	s := &Server{repository: counting}
	source := strings.Repeat(widgetSource(post.ID), 10000)
	data := pageView{Post: content.Post{ContentHTML: source}}
	s.prepareArticleWidgets(t.Context(), &data)
	if counting.calls != 1 || len(counting.ids) != 1 || strings.Count(data.Post.ContentHTML, `data-np-widget="post"`) != maxRenderedArticleWidgets || len(data.Post.ContentHTML) > maxArticleWidgetBodyBytes || strings.Contains(data.Post.ContentHTML, "伪造旧信息") {
		t.Fatalf("repeated widgets inflated output: calls=%d ids=%d bytes=%d", counting.calls, len(counting.ids), len(data.Post.ContentHTML))
	}
	// Even a repository adapter returning unexpectedly large metadata cannot
	// make the expanded body overrun the page renderer's output limit.
	post.Title = strings.Repeat("大标题", 10000)
	post.Excerpt = strings.Repeat("长摘要", 10000)
	counting.override = &content.PublishedPage{Items: []content.Post{post}, Total: 1}
	data = pageView{Post: content.Post{ContentHTML: strings.Repeat(widgetSource(post.ID), maxRenderedArticleWidgets)}}
	s.prepareArticleWidgets(t.Context(), &data)
	if len(data.Post.ContentHTML) > maxArticleWidgetBodyBytes || !strings.Contains(data.Post.ContentHTML, "文章暂不可用") || strings.Contains(data.Post.ContentHTML, "伪造旧信息") {
		t.Fatalf("oversize metadata escaped byte budget: %d", len(data.Post.ContentHTML))
	}
}

func TestArticleWidgetsBoundAndDeduplicateLookupsPreserveUnrelatedSource(t *testing.T) {
	repository := content.NewMemoryRepository(false)
	var source strings.Builder
	for i := 0; i < 26; i++ {
		post := createWidgetPost(t, repository, content.PostInput{Title: fmt.Sprintf("文章 %d", i), Slug: fmt.Sprintf("widget-%d", i), Status: content.StatusPublish})
		source.WriteString(widgetSource(post.ID))
		source.WriteString(widgetSource(post.ID))
	}
	counting := &widgetCountingRepository{Repository: repository}
	s := &Server{repository: counting}
	data := pageView{Post: content.Post{ContentHTML: source.String()}}
	s.prepareArticleWidgets(t.Context(), &data)
	if counting.calls != 1 || len(counting.ids) != 24 || strings.Count(data.Post.ContentHTML, "文章暂不可用") != 4 || strings.Contains(data.Post.ContentHTML, "伪造旧信息") {
		t.Fatalf("lookups unbounded: calls=%d ids=%d unavailable=%d", counting.calls, len(counting.ids), strings.Count(data.Post.ContentHTML, "文章暂不可用"))
	}
	original := `<!--keep-->  <p class="legacy">Unchanged &amp; exact</p>`
	data = pageView{Post: content.Post{ContentHTML: original}}
	s.prepareArticleWidgets(t.Context(), &data)
	if data.Post.ContentHTML != original || counting.calls != 1 {
		t.Fatal("ordinary body was parsed or queried")
	}
	data = pageView{IsAdmin: true, Post: content.Post{ContentHTML: source.String()}}
	s.prepareArticleWidgets(t.Context(), &data)
	if data.Post.ContentHTML != source.String() || counting.calls != 1 {
		t.Fatal("admin source projection was changed")
	}
}

func TestArticleWidgetsRejectInvalidMissingPrivateFutureAndPageReferences(t *testing.T) {
	repository := content.NewMemoryRepository(false)
	s := &Server{repository: repository}
	for i, input := range []content.PostInput{
		{Status: content.StatusDraft}, {Status: content.StatusPrivate}, {Status: content.StatusTrash},
		{Status: content.StatusFuture, PublishedAt: time.Now().Add(time.Hour)},
		{Status: content.StatusPublish, Type: content.TypePage},
	} {
		input.Title, input.Slug = "非公开信息", fmt.Sprintf("hidden-widget-%d", i)
		post := createWidgetPost(t, repository, input)
		data := pageView{Post: content.Post{ContentHTML: widgetSource(post.ID)}}
		s.prepareArticleWidgets(t.Context(), &data)
		if !strings.Contains(data.Post.ContentHTML, "文章暂不可用") || strings.Contains(data.Post.ContentHTML, "非公开信息") || strings.Contains(data.Post.ContentHTML, "伪造") {
			t.Fatalf("nonpublic post exposed: %s", data.Post.ContentHTML)
		}
	}
	for _, rawID := range []string{"0", "-1", "+1", "01", "1e3", "99999999999999999999999", "99999", ""} {
		source := `<aside data-np-widget="post" data-np-post="` + rawID + `"><p>伪造信息</p></aside>`
		data := pageView{Post: content.Post{ContentHTML: source}}
		s.prepareArticleWidgets(t.Context(), &data)
		if !strings.Contains(data.Post.ContentHTML, "文章暂不可用") || strings.Contains(data.Post.ContentHTML, "伪造") {
			t.Fatalf("invalid id survived: %q %s", rawID, data.Post.ContentHTML)
		}
	}
	data := pageView{Post: content.Post{ContentHTML: `<aside data-np-widget="post"><aside>伪造嵌套</aside><p>未闭合快照`}}
	s.prepareArticleWidgets(t.Context(), &data)
	if strings.Contains(data.Post.ContentHTML, "伪造") || strings.Contains(data.Post.ContentHTML, "快照") {
		t.Fatal("malformed fallback snapshot survived")
	}
}

func TestArticleWidgetMetadataEscapesTextAndRejectsExternalCovers(t *testing.T) {
	markup := renderArticleWidget(articleWidgetReference{id: 1, layout: "compact"}, content.Post{Title: `<script>evil()</script>`, Slug: "valid", Excerpt: `<img src=x onerror=evil()>`, AuthorName: `<b>author</b>`, Category: `<svg>`, CoverURL: "//remote.test/track"}, true)
	for _, unsafe := range []string{"<script", "<img", "<svg", "<b>", "//remote"} {
		if strings.Contains(markup, unsafe) {
			t.Fatalf("metadata became markup: %s", markup)
		}
	}
}

func TestArticleWidgetStylesOnlyEnterRealHead(t *testing.T) {
	source := `<!doctype html><HTML><HEAD><script>const fake = "</head>";</script><!-- </head> --></HEAD><BODY>body</BODY></HTML>`
	got := string(withArticleWidgetStyles([]byte(source)))
	if strings.Count(got, "newpress-article-widgets.css") != 1 || !strings.Contains(got, `<!-- </head> --><link rel="stylesheet"`) || !strings.Contains(got, `</HEAD><BODY>body`) {
		t.Fatalf("styles entered fake head: %s", got)
	}
	for _, source := range []string{"<body><head></head>body</body>", "<article>no head</article>"} {
		if got := string(withArticleWidgetStyles([]byte(source))); got != source {
			t.Fatal("headless theme changed")
		}
	}
}

func TestArticleWidgetStylesLoadOnlyForReferencedArticles(t *testing.T) {
	_, handler, repository, cookie, _ := newAuthenticatedContentServer(t)
	plain := createWidgetPost(t, repository, content.PostInput{Title: "普通文章", Slug: "widget-plain", Status: content.StatusPublish})
	response := serveCookieRequest(handler, http.MethodGet, "https://example.test/posts/"+plain.Slug, nil, nil)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "newpress-article-widgets.css") {
		t.Fatalf("plain article loaded widget resource: %d", response.Code)
	}
	response = serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/", cookie, nil)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "newpress-article-widgets.css") {
		t.Fatalf("admin list loaded widget resource: %d", response.Code)
	}
}

func TestArticleWidgetsAppearancePreviewResolvesReferencesAndStyles(t *testing.T) {
	s, handler, _, repository, cookie, _ := newPersistentAdminTestServer(t)
	child := createWidgetPost(t, repository, content.PostInput{Title: "外观预览读取的最新标题", Slug: "appearance-widget-child", Status: content.StatusPublish, PublishedAt: time.Now().Add(-2 * time.Hour), CoverURL: "/theme-assets/images/cover-core.svg"})
	parent := createWidgetPost(t, repository, content.PostInput{Title: "最新的预览文章", Slug: "appearance-widget-parent", Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour), ContentSource: "<p>预览前文</p>" + widgetSource(child.ID) + "<p>预览后文</p>"})
	before := s.themes.SettingsSnapshot()
	readPreview := func() string {
		t.Helper()
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/appearance/preview/?page=article", cookie, nil)
		var payload map[string]string
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("appearance article preview: %d %s", response.Code, response.Body.String())
		}
		return payload["html"]
	}
	markup := readPreview()
	if !strings.Contains(markup, `data-np-part="title">外观预览读取的最新标题`) || !strings.Contains(markup, `href="/posts/appearance-widget-child"`) || strings.Contains(markup, "伪造旧信息") || strings.Count(markup, "newpress-article-widgets.css") != 1 {
		t.Fatal("appearance preview did not resolve the public widget and load its stylesheet once")
	}
	if _, err := repository.ChangeStatus(t.Context(), child.ID, content.Mutation{ExpectedVersion: child.Version}, content.StatusPrivate); err != nil {
		t.Fatal(err)
	}
	markup = readPreview()
	if !strings.Contains(markup, "文章暂不可用") || strings.Contains(markup, "外观预览读取的最新标题") || strings.Contains(markup, "伪造旧信息") {
		t.Fatal("appearance preview retained withdrawn article metadata")
	}
	input := inputFromPost(parent)
	input.ContentSource = "<p>没有引用的普通预览正文</p>"
	if _, err := repository.Update(t.Context(), parent.ID, content.Mutation{ExpectedVersion: parent.Version}, input); err != nil {
		t.Fatal(err)
	}
	if markup = readPreview(); strings.Contains(markup, "newpress-article-widgets.css") || !strings.Contains(markup, "没有引用的普通预览正文") {
		t.Fatal("ordinary appearance preview loaded widget resources")
	}
	if after := s.themes.SettingsSnapshot(); after.Revision != before.Revision || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("article widget preview changed active theme settings")
	}
}

func TestAdminArticleWidgetsPublicPickerPermissionsPaginationAndDisabledAPIs(t *testing.T) {
	s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
	author, authorCookie, _ := createAndLoginTestUser(t, manager, owner, "widget-author", "widget-author@example.test", "作者", "author")
	_, readerCookie, _ := createAndLoginTestUser(t, manager, owner, "widget-reader", "widget-reader@example.test", "读者", "subscriber")
	for i := 0; i < 10; i++ {
		createWidgetPost(t, repository, content.PostInput{AuthorID: owner.UserID, Title: fmt.Sprintf("公开匹配 %d", i), Slug: fmt.Sprintf("picker-public-%d", i), Status: content.StatusPublish})
	}
	createWidgetPost(t, repository, content.PostInput{AuthorID: author.ID, Title: "私密匹配", Slug: "picker-private", Status: content.StatusPrivate})
	createWidgetPost(t, repository, content.PostInput{AuthorID: author.ID, Title: "未发布匹配", Slug: "picker-future", Status: content.StatusFuture, PublishedAt: time.Now().Add(time.Hour)})
	snapshot := s.siteSettings.Snapshot()
	snapshot.Settings.API.AdminContentEnabled = false
	snapshot.Settings.API.PublicPostsEnabled = false
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) != 0 {
		t.Fatalf("disable APIs: %v %v", err, fields)
	}
	target := "https://example.test/api/v1/admin/article-widgets"
	response := serveCookieRequest(handler, http.MethodGet, target, authorCookie, nil)
	var payload struct {
		Items      []articleWidgetSummary  `json:"items"`
		Pagination articleWidgetPagination `json:"pagination"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || response.Code != http.StatusOK || len(payload.Items) != 8 || payload.Pagination.Total != 10 || payload.Pagination.Pages != 2 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("picker blocked or wrong pagination: %d %v %s", response.Code, err, response.Body.String())
	}
	for _, forbidden := range []string{"content_source", "content_html", "私密匹配", "未发布匹配"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("picker leaked %s", forbidden)
		}
	}
	response = serveCookieRequest(handler, http.MethodGet, target+"?page=2", authorCookie, nil)
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || len(payload.Items) != 2 || payload.Pagination.Page != 2 {
		t.Fatal("second picker page failed")
	}
	response = serveCookieRequest(handler, http.MethodGet, target+"?q=picker-no-match", authorCookie, nil)
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || len(payload.Items) != 0 || payload.Pagination.Total != 0 {
		t.Fatal("picker search ignored")
	}
	if response := serveCookieRequest(handler, http.MethodGet, target, nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous picker: %d", response.Code)
	}
	if response := serveCookieRequest(handler, http.MethodGet, target, readerCookie, nil); response.Code != http.StatusForbidden {
		t.Fatalf("reader picker: %d", response.Code)
	}
	for _, query := range []string{"?page=0", "?page=1&page=2", "?status=private", "?q=" + strings.Repeat("x", 81)} {
		if response := serveCookieRequest(handler, http.MethodGet, target+query, authorCookie, nil); response.Code != http.StatusBadRequest {
			t.Fatalf("bad picker query accepted %s: %d", query, response.Code)
		}
	}
	if response := serveCookieRequest(handler, http.MethodPost, target, authorCookie, nil); response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET" {
		t.Fatalf("picker write allowed: %d", response.Code)
	}
}
