package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/content"
)

func TestHomeModuleOrderAndVisibilityControls(t *testing.T) {
	t.Parallel()
	values := map[string]any{
		"home_top_order": "notice,hero,notice,feed,unknown", "home_main_order": "community,series",
		"home_hero_visibility": "mobile", "home_series_visibility": "desktop", "home_stats_visibility": "invalid",
		"home_channels_visibility": "hidden", "sidebar_enabled": false,
	}
	regions := homeEditor(values)
	if regions[0].Order != "notice,hero,feed,channels,categories,reading_guide,carousel,search_banner" || regions[1].Order != "community,series,spotlight" {
		t.Fatalf("order did not normalize duplicates and missing modules: %#v", regions)
	}
	if regions[0].Modules[1].Visibility != "mobile" || regions[0].Modules[3].Visibility != "hidden" {
		t.Fatal("device visibility was lost")
	}
	if homeRequested(values, "channels") || homeRequested(values, "stats") || homeRequested(values, "unregistered") || !homeRequested(values, "hero") {
		t.Fatal("module gates do not respect existing controls")
	}
	if got := homeEditor(nil); len(got[0].Modules)+len(got[1].Modules)+len(got[2].Modules) != 19 {
		t.Fatal("default layout does not contain the complete module catalog")
	}
	values["home_hero_visibility"] = "hidden"
	if homeRequested(values, "hero") {
		t.Fatal("hidden module was requested")
	}
}

func TestHomeContentUsesOnlyPublicSelections(t *testing.T) {
	t.Parallel()
	s, handler, _, repository, _, owner := newPersistentAdminTestServer(t)
	create := func(slug, status string, published time.Time, postType string) content.Post {
		t.Helper()
		post, err := repository.Create(t.Context(), content.PostInput{
			Title: slug, Slug: slug, Status: status, Type: postType, PublishedAt: published,
			AuthorID: owner.UserID, AuthorName: owner.Label(), Category: "公开分类", TagNames: []string{"公开标签"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return post
	}
	now := time.Now().Add(-time.Hour)
	one := create("home-public-one", content.StatusPublish, now, content.TypePost)
	two := create("home-public-two", content.StatusPublish, now.Add(time.Minute), content.TypePost)
	private := create("home-private-sentinel", content.StatusPrivate, now, content.TypePost)
	draft := create("home-draft-sentinel", content.StatusDraft, now, content.TypePost)
	future := create("home-future-sentinel", content.StatusPublish, now.Add(48*time.Hour), content.TypePost)
	page := create("home-page-sentinel", content.StatusPublish, now, content.TypePage)
	_, err := repository.SaveSeries(t.Context(), 0, content.Mutation{Actor: owner.Username}, content.SeriesInput{
		Kind: content.SeriesKindTopic, Status: content.StatusPublish, Title: "可阅读专题", Slug: "home-readable-series", PostIDs: []int64{one.ID, private.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Empty selections newer than the readable one must not consume the display limit.
	for i := 0; i < 5; i++ {
		_, err := repository.SaveSeries(t.Context(), 0, content.Mutation{Actor: owner.Username}, content.SeriesInput{
			Kind: content.SeriesKindTopic, Status: content.StatusPublish, Title: "没有公开文章", Slug: fmt.Sprintf("home-empty-%d", i), PostIDs: []int64{private.ID},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	store, err := community.NewFileStore(filepath.Join(t.TempDir(), "circles.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetCommunityStore(store)
	s.config.CommunityEnabled = true
	for _, visibility := range []string{"public", "members"} {
		_, err = store.Apply(t.Context(), "home-"+visibility, community.Actor{ID: owner.UserID, Admin: true}, community.Command{
			Action: "create", Key: "home-create-circle-" + visibility, Name: "home-circle-" + visibility,
			Visibility: visibility, JoinPolicy: "open", OwnerID: owner.UserID,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	values := s.themes.SettingsSnapshot().Values
	values["home_featured_ids"] = fmt.Sprintf("%d,%d,%d,%d,%d,%d,%d", private.ID, draft.ID, future.ID, page.ID, two.ID, one.ID, two.ID)
	values["home_series_count"] = 2
	values["home_spotlight_visibility"] = "both"
	if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) != 0 {
		t.Fatalf("save selections: %v %v", fields, err)
	}
	view, err := s.homePage(httptest.NewRequest("GET", "https://example.test/", nil), values)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Featured) != 2 || view.Featured[0].ID != two.ID || view.Featured[1].ID != one.ID || view.Home.TotalPosts != 2 {
		t.Fatalf("public selection or total: %#v, %d", view.Featured, view.Home.TotalPosts)
	}
	spotlight := func() bool {
		for _, block := range view.Home.Main {
			if block.Key == "spotlight" {
				return true
			}
		}
		return false
	}
	if spotlight() {
		t.Fatal("spotlight should hide when hero consumes both selected posts")
	}
	values["home_featured_ids"] = fmt.Sprintf("%d", one.ID)
	view, err = s.homePage(httptest.NewRequest("GET", "https://example.test/", nil), values)
	if err != nil || len(view.Featured) != 1 || spotlight() {
		t.Fatalf("consumed single pick should not leave an empty spotlight: %v %#v", err, view.Home.Main)
	}
	values["home_featured_ids"] = fmt.Sprintf("%d,%d", two.ID, one.ID)
	view, err = s.homePage(httptest.NewRequest("GET", "https://example.test/", nil), values)
	if err != nil {
		t.Fatal("restore featured selection", err)
	}
	if len(view.Home.Series) != 1 || view.Home.Series[0].Slug != "home-readable-series" || view.Home.Series[0].Count() != 1 {
		t.Fatalf("readable series: %#v", view.Home.Series)
	}
	if len(view.Home.Circles) != 1 || view.Home.Circles[0].Name != "home-circle-public" || len(view.Home.Authors) != 1 || view.Home.Authors[0].ID != owner.UserID {
		t.Fatalf("public community or creator projection: %#v", view.Home)
	}
	response := serveCookieRequest(handler, "GET", "https://example.test/", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("home: %d", response.Code)
	}
	for _, hidden := range []string{private.Title, draft.Title, future.Title, page.Title, "home-circle-members", "没有公开文章"} {
		if strings.Contains(response.Body.String(), hidden) {
			t.Fatalf("public homepage leaked %q", hidden)
		}
	}
	values["home_feed_visibility"] = "mobile"
	for _, key := range []string{"series", "spotlight", "community"} {
		values["home_"+key+"_visibility"] = "hidden"
	}
	view, err = s.homePage(httptest.NewRequest("GET", "https://example.test/", nil), values)
	if err != nil || view.Home.HasDesktopMain || !view.Home.HasDesktopSidebar || len(view.Home.Main) != 1 {
		t.Fatalf("mobile-only content must not reserve a desktop column: %v %#v", err, view.Home.Main)
	}
	values["sidebar_enabled"] = false
	view, err = s.homePage(httptest.NewRequest("GET", "https://example.test/", nil), values)
	if err != nil || len(view.Home.Aside) != 0 || view.Home.HasDesktopSidebar {
		t.Fatal("disabled sidebar still has modules")
	}
}

func TestHomeFiltersAndPaginationPreserveSelection(t *testing.T) {
	t.Parallel()
	s, handler, _, repository, _, _ := newPersistentAdminTestServer(t)
	pageSize := s.siteSettings.Snapshot().Settings.Reading.HomePosts
	for i := 0; i < pageSize+3; i++ {
		_, err := repository.Create(t.Context(), content.PostInput{
			Title: fmt.Sprintf("Home findable %03d", i), Slug: fmt.Sprintf("home-findable-%d", i), Status: content.StatusPublish,
			Category: "reading", PublishedAt: time.Now().Add(-time.Duration(100-i) * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	path := "https://example.test/?q=Home&category=reading&sort=oldest&view=list&page=2"
	response := serveCookieRequest(handler, "GET", path, nil, nil)
	body := response.Body.String()
	ids := regexp.MustCompile(`data-post-id="([0-9]+)"`).FindAllStringSubmatch(body, -1)
	if response.Code != 200 || len(ids) != 3 || !strings.Contains(body, `aria-current="page">2`) || !strings.Contains(body, `category=reading&amp;q=Home&amp;sort=oldest&amp;view=list#latest`) {
		t.Fatalf("filtered second page: status=%d cards=%d", response.Code, len(ids))
	}
	view, err := s.homePage(httptest.NewRequest("GET", path, nil), s.themes.SettingsSnapshot().Values)
	if err != nil || view.Posts[0].Slug != fmt.Sprintf("home-findable-%d", pageSize) || !strings.Contains(view.Home.GridURL, "category=reading&q=Home&sort=oldest&view=grid#latest") || strings.Contains(view.Home.GridURL, "page=") {
		t.Fatalf("sort or view link lost selection: %v %#v", err, view.Home)
	}
	for _, query := range []string{"page=0", "q=one&q=two", "sort=random", "view=unknown", "foreign=1"} {
		if got := serveCookieRequest(handler, "GET", "https://example.test/?"+query, nil, nil); got.Code != 400 {
			t.Fatalf("invalid homepage query %q: %d", query, got.Code)
		}
	}
	response = serveCookieRequest(handler, "GET", "https://example.test/?category=reading&page=99&sort=oldest&view=grid", nil, nil)
	if response.Code != 302 || !strings.Contains(response.Header().Get("Location"), "category=reading&page=2&sort=oldest&view=grid#latest") {
		t.Fatalf("out-of-range page: %d %s", response.Code, response.Header().Get("Location"))
	}
}

func TestAppearancePreviewDoesNotPersistAndRequiresPermission(t *testing.T) {
	t.Parallel()
	s, handler, users, repository, cookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, _ := createAndLoginTestUser(t, users, owner, "home-preview-reader", "reader@example.test", "读者", "subscriber")
	before := s.themes.SettingsSnapshot()
	if _, fields, err := s.themes.SaveSettings(before.Values); err != nil || len(fields) != 0 {
		t.Fatalf("save initial settings: %v %v", fields, err)
	}
	before = s.themes.SettingsSnapshot()
	var diskBefore bytes.Buffer
	s.themes.ExportSettings(&diskBefore)
	form := url.Values{"csrf_token": {owner.CSRFToken}}
	for key, value := range before.Values {
		form.Set(key, fmt.Sprint(value))
	}
	form.Set("hero_title", "仅预览中的标题")
	form.Set("home_feed_visibility", "hidden")
	form.Set("home_module_gap", "24")
	form.Set("visual_style", "liquid-glass")
	previewURL := "https://example.test/admin/appearance/preview/?page=home"
	response := serveContentForm(handler, cookie, previewURL, "https://example.test", form)
	var result map[string]string
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("preview: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(result["html"], "仅预览中的标题") || !strings.Contains(result["html"], `data-style="liquid-glass"`) || strings.Contains(result["html"], `data-home-module="feed"`) || !strings.Contains(result["css"], "--home-gap:24px") {
		t.Fatal("preview did not apply candidate settings")
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("preview must not enter a shared cache")
	}
	for _, item := range []struct {
		name, origin, key, value string
		want                     int
	}{
		{"origin", "https://other.test", "", "", 403},
		{"csrf", "https://example.test", "csrf_token", "wrong", 403},
		{"unsafe URL", "https://example.test", "home_notice_url", "javascript:alert(1)", 422},
		{"foreign module", "https://example.test", "home_top_order", "hero,admin", 422},
		{"bad device", "https://example.test", "home_feed_visibility", "tablet", 422},
		{"size", "https://example.test", "hero_title", strings.Repeat("x", 520<<10), 400},
	} {
		t.Run(item.name, func(t *testing.T) {
			candidate := make(url.Values)
			for key, value := range form {
				candidate[key] = slices.Clone(value)
			}
			if item.key != "" {
				candidate.Set(item.key, item.value)
			}
			if got := serveContentForm(handler, cookie, previewURL, item.origin, candidate); got.Code != item.want {
				t.Fatalf("HTTP %d, want %d", got.Code, item.want)
			}
		})
	}
	if got := serveCookieRequest(handler, "GET", previewURL, readerCookie, nil); got.Code != 403 {
		t.Fatalf("reader can access appearance preview: %d", got.Code)
	}
	if got := serveCookieRequest(handler, "GET", previewURL, nil, nil); got.Code == 200 {
		t.Fatal("anonymous visitor can access appearance preview")
	}
	for _, query := range []string{"page=unknown", "page=home&page=article", "page=home&foreign=1"} {
		if got := serveCookieRequest(handler, "GET", "https://example.test/admin/appearance/preview/?"+query, cookie, nil); got.Code != 400 {
			t.Fatalf("invalid preview query %q: %d", query, got.Code)
		}
	}
	articleURL := "https://example.test/admin/appearance/preview/?page=article"
	if got := serveCookieRequest(handler, "GET", articleURL, cookie, nil); got.Code != 404 {
		t.Fatalf("empty article preview: %d", got.Code)
	}
	if _, err := repository.Create(t.Context(), content.PostInput{Title: "预览公开文章", Slug: "home-preview-article", Status: content.StatusPublish}); err != nil {
		t.Fatal(err)
	}
	if got := serveCookieRequest(handler, "GET", articleURL, cookie, nil); got.Code != 200 || !strings.Contains(got.Body.String(), "预览公开文章") {
		t.Fatalf("article preview: %d", got.Code)
	}
	after := s.themes.SettingsSnapshot()
	if after.Revision != before.Revision || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("preview changed the active settings")
	}
	if err := s.themes.Load(); err != nil {
		t.Fatal(err)
	}
	var diskAfter bytes.Buffer
	s.themes.ExportSettings(&diskAfter)
	if !bytes.Equal(diskBefore.Bytes(), diskAfter.Bytes()) {
		t.Fatal("preview changed persisted settings")
	}
}
