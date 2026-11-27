package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestDiscoveryHTMLAndAPISearchWholeCollection(t *testing.T) {
	server, handler, repository, _, _ := newAuthenticatedContentServer(t)
	pageSize := server.siteSettings.Snapshot().Settings.Reading.ExplorePosts
	for i := 0; i < pageSize+3; i++ {
		title := fmt.Sprintf("Discoverable %03d", i)
		if i == 0 {
			title += " unique-oldest-match"
		}
		if _, err := repository.Create(t.Context(), content.PostInput{Title: title, Slug: fmt.Sprintf("discovery-%d", i), Status: content.StatusPublish, Category: "Search category", PublishedAt: time.Now().Add(-time.Duration(1000-i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.Create(t.Context(), content.PostInput{Title: "private-only-marker", Slug: "private-search", Status: content.StatusPrivate, Category: "Hidden"}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "https://example.test"+path, nil))
		return w
	}
	result := get("/explore/?q=unique-oldest-match")
	if result.Code != 200 || !strings.Contains(result.Body.String(), `data-server-results`) || !strings.Contains(result.Body.String(), `href="/posts/discovery-0"`) || !strings.Contains(result.Body.String(), "1 条结果") {
		t.Fatalf("older search = %d %s", result.Code, result.Body)
	}
	result = get("/explore/?q=Discoverable&page=2&sort=oldest&view=list")
	ids := regexp.MustCompile(`data-post-id="([0-9]+)"`).FindAllStringSubmatch(result.Body.String(), -1)
	if result.Code != 200 || len(ids) != 3 || !strings.Contains(result.Body.String(), `data-results-view="list"`) || !strings.Contains(result.Body.String(), `aria-current="page">2`) || !strings.Contains(result.Body.String(), `q=Discoverable&amp;sort=oldest&amp;view=list`) {
		t.Fatalf("SSR page = %d %s", result.Code, result.Body)
	}
	for _, path := range []string{"/explore/?category=does-not-exist", "/explore/?q=private-only-marker"} {
		result = get(path)
		if result.Code != 200 || !strings.Contains(result.Body.String(), "还没有匹配内容") {
			t.Fatalf("empty search = %d %s", result.Code, result.Body)
		}
	}
	for _, path := range []string{"/explore/?page=0", "/explore/?q=a&q=b", "/explore/?sort=random", "/explore/?q=" + url.QueryEscape(strings.Repeat("字", 81)), "/explore/?view=unknown", "/explore/?unexpected=1"} {
		if result = get(path); result.Code != 400 {
			t.Fatalf("invalid query %s = %d", path, result.Code)
		}
	}
	result = get("/explore/?q=Discoverable&page=99&sort=oldest")
	if result.Code != 302 || !strings.Contains(result.Header().Get("Location"), "page=2") {
		t.Fatalf("out-of-range page = %d %q", result.Code, result.Header().Get("Location"))
	}
	api := get("/api/v1/posts?q=Discoverable&limit=6&page=2&sort=oldest")
	var body struct {
		Items   []publicPostResponse `json:"items"`
		Total   int                  `json:"total"`
		Page    int                  `json:"page"`
		HasMore bool                 `json:"has_more"`
	}
	decodeAPIResponse(t, api, &body)
	if body.Total != pageSize+3 || body.Page != 2 || len(body.Items) != 6 || body.Items[0].Slug != "discovery-6" || !body.HasMore {
		t.Fatalf("API pagination = %#v", body)
	}
	if api.Header().Get("ETag") == "" {
		t.Fatal("missing representation ETag")
	}
	conditional := httptest.NewRequest("GET", "https://example.test/api/v1/posts?q=Discoverable&limit=6&page=2&sort=oldest", nil)
	conditional.Header.Set("If-None-Match", api.Header().Get("ETag"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, conditional)
	if w.Code != 304 {
		t.Fatalf("conditional result = %d", w.Code)
	}
	for _, path := range []string{"/api/v1/posts?author=-1", "/api/v1/posts?page=0", "/api/v1/posts?sort=unsafe", "/api/v1/posts?q=a&q=b"} {
		if result = get(path); result.Code != 400 {
			t.Fatalf("invalid API query = %d %s", result.Code, result.Body)
		}
	}
}

func TestPublicAuthorProfilesUseAccountIdentity(t *testing.T) {
	_, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
	user, _, _ := createAndLoginTestUser(t, manager, owner, "actual-author", "private-author@example.test", "真实创作者", "author")
	_, err := repository.Create(context.Background(), content.PostInput{AuthorID: user.ID, AuthorName: user.DisplayName, Title: "Own published work", Slug: "actual-author-post", Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.Create(context.Background(), content.PostInput{AuthorID: owner.UserID, AuthorName: user.DisplayName, Title: "Same name different owner", Slug: "different-owner-post", Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil))
		return w
	}
	page := get("/authors/" + strconv.FormatInt(user.ID, 10) + "/")
	if page.Code != 200 || !strings.Contains(page.Body.String(), user.DisplayName) || !strings.Contains(page.Body.String(), "Own published work") || !strings.Contains(page.Body.String(), fmt.Sprintf(`data-message-user="%d"`, user.ID)) {
		t.Fatalf("author profile = %d %s", page.Code, page.Body)
	}
	for _, private := range []string{user.Email, user.PasswordHash, "Same name different owner", "12.4k", "86.2k"} {
		if private != "" && strings.Contains(page.Body.String(), private) {
			t.Fatalf("profile exposed %q", private)
		}
	}
	for _, path := range []string{"/authors/0/", "/authors/99999999/", "/authors/01/", "/authors/unknown/"} {
		if got := get(path); got.Code != 404 {
			t.Fatalf("unknown profile %s = %d", path, got.Code)
		}
	}
}
