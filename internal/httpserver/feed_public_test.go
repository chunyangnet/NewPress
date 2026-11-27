package httpserver

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

type decodedFeed struct {
	XMLName xml.Name
	Version string `xml:"version,attr"`
	Channel struct {
		Self struct {
			Href string `xml:"href,attr"`
		} `xml:"http://www.w3.org/2005/Atom link"`
		Title       string `xml:"title"`
		Link        string `xml:"link"`
		Description string `xml:"description"`
		Language    string `xml:"language"`
		Items       []struct {
			Title       string   `xml:"title"`
			Link        string   `xml:"link"`
			Description string   `xml:"description"`
			Creator     string   `xml:"http://purl.org/dc/elements/1.1/ creator"`
			GUID        rssGUID  `xml:"guid"`
			PublishedAt string   `xml:"pubDate"`
			Categories  []string `xml:"category"`
		} `xml:"item"`
	} `xml:"channel"`
}

func newFeedServer(t *testing.T) (*Server, *content.MemoryRepository) {
	t.Helper()
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel")
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	repository := content.NewMemoryRepository(false)
	return New(config.Config{SiteTitle: "阅读 & 创作", SiteTagline: "订阅最新内容", PublicURL: "https://example.test", ActiveTheme: "chunnel"}, repository, themes, plugin.NewManager(t.TempDir())), repository
}

func getFeed(t *testing.T, handler http.Handler, path, method, etag string) (*httptest.ResponseRecorder, decodedFeed) {
	t.Helper()
	r := httptest.NewRequest(method, "https://example.test"+path, nil)
	r.Header.Set("If-None-Match", etag)
	r.Host = "untrusted.example"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var feed decodedFeed
	if w.Code == 200 && method != http.MethodHead {
		if err := xml.Unmarshal(w.Body.Bytes(), &feed); err != nil {
			t.Fatalf("decode feed: %v: %s", err, w.Body)
		}
	}
	return w, feed
}

func TestRSSPublicProjectionFilteringAndStableIdentity(t *testing.T) {
	server, repository := newFeedServer(t)
	handler := server.routes()
	input := content.PostInput{Title: "标题 <安全> & 内容", Slug: "feed-article", Excerpt: `<img src=x onerror=alert(1)> & 摘要`, ContentSource: "<p>body-must-stay-private-to-feed</p>", AuthorName: "作者 & 同伴", Category: "阅读", Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour).Truncate(time.Second)}
	post, err := repository.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{content.StatusDraft, content.StatusPending, content.StatusPrivate, content.StatusTrash, content.StatusFuture} {
		_, err := repository.Create(t.Context(), content.PostInput{Title: "hidden-" + status, Slug: "hidden-" + status, Status: status, PublishedAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.Create(t.Context(), content.PostInput{Type: content.TypePage, Title: "hidden-page", Slug: "hidden-page", Status: content.StatusPublish}); err != nil {
		t.Fatal(err)
	}
	response, feed := getFeed(t, handler, "/feed.xml", "GET", "")
	if response.Code != 200 || feed.XMLName.Local != "rss" || feed.Version != "2.0" || len(feed.Channel.Items) != 1 {
		t.Fatalf("feed = %d %s", response.Code, response.Body)
	}
	if feed.Channel.Title != "阅读 & 创作" || feed.Channel.Link != "https://example.test/" || feed.Channel.Description != "订阅最新内容" || feed.Channel.Language != "zh-CN" || feed.Channel.Self.Href != "https://example.test/feed.xml" {
		t.Fatalf("channel = %#v", feed.Channel)
	}
	item := feed.Channel.Items[0]
	if item.Title != post.Title || item.Creator != post.AuthorName || item.Description != html.EscapeString(post.Excerpt) || item.Link != "https://example.test/posts/feed-article" || item.GUID.IsPermaLink || len(item.Categories) != 1 || item.Categories[0] != "阅读" {
		t.Fatalf("item = %#v", item)
	}
	if date, err := time.Parse(time.RFC1123Z, item.PublishedAt); err != nil || !date.Equal(post.PublishedAt) {
		t.Fatalf("pubDate = %q: %v", item.PublishedAt, err)
	}
	for _, forbidden := range []string{"hidden-", "body-must-stay-private-to-feed", "untrusted.example", "content_source", "actor"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("feed leaks %s", forbidden)
		}
	}
	if response.Header().Get("Content-Type") != "application/rss+xml; charset=utf-8" || response.Header().Get("Cache-Control") != "public, no-cache" || response.Header().Get("ETag") == "" {
		t.Fatalf("headers = %v", response.Header())
	}
	filtered, result := getFeed(t, handler, "/feed.xml?category="+post.Categories[0].Slug+"&limit=1", "GET", "")
	if filtered.Code != 200 || len(result.Channel.Items) != 1 || !strings.Contains(result.Channel.Self.Href, "limit=1") {
		t.Fatalf("filtered feed = %d %s", filtered.Code, filtered.Body)
	}
	empty, result := getFeed(t, handler, "/feed.xml?category=missing", "GET", "")
	if empty.Code != 200 || len(result.Channel.Items) != 0 {
		t.Fatalf("empty feed = %d %s", empty.Code, empty.Body)
	}
	input.Slug = "renamed-feed-article"
	post, err = repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	updated, result := getFeed(t, handler, "/feed.xml", "GET", response.Header().Get("ETag"))
	if updated.Code != 200 || result.Channel.Items[0].GUID.Value != item.GUID.Value || result.Channel.Items[0].Link != "https://example.test/posts/renamed-feed-article" {
		t.Fatalf("rename = %d %s", updated.Code, updated.Body)
	}
	if _, err := repository.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusPrivate); err != nil {
		t.Fatal(err)
	}
	private, result := getFeed(t, handler, "/feed.xml", "GET", updated.Header().Get("ETag"))
	if private.Code != 200 || len(result.Channel.Items) != 0 {
		t.Fatalf("withdrawn article remained = %d %s", private.Code, private.Body)
	}
}

func TestRSSBoundsConditionsAndSettings(t *testing.T) {
	server, repository := newFeedServer(t)
	handler := server.routes()
	for i := range 25 {
		_, err := repository.Create(t.Context(), content.PostInput{Title: fmt.Sprintf("Article %02d", i), Slug: fmt.Sprintf("article-%d", i), Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Duration(30-i) * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
	}
	response, feed := getFeed(t, handler, "/feed.xml", "GET", "")
	if response.Code != 200 || len(feed.Channel.Items) != 20 || feed.Channel.Items[0].Title != "Article 24" {
		t.Fatalf("bounded ordering = %d %s", response.Code, response.Body)
	}
	all, feed := getFeed(t, handler, "/feed.xml?limit=100", "GET", "")
	if all.Code != 200 || len(feed.Channel.Items) != 25 {
		t.Fatalf("explicit limit = %d %s", all.Code, all.Body)
	}
	for _, method := range []string{"GET", "HEAD"} {
		conditional, _ := getFeed(t, handler, "/feed.xml", method, "W/"+response.Header().Get("ETag"))
		if conditional.Code != 304 || conditional.Body.Len() != 0 {
			t.Fatalf("conditional %s = %d %s", method, conditional.Code, conditional.Body)
		}
	}
	head, _ := getFeed(t, handler, "/feed.xml", "HEAD", "")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("ETag") != response.Header().Get("ETag") {
		t.Fatalf("HEAD = %d %s", head.Code, head.Body)
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=-1", "limit=abc", "limit=1&limit=2", "category=a&category=b", "page=2", "category=%00", "category=%FF", "category=%ZZ", "category=" + strings.Repeat("a", 161)} {
		invalid, _ := getFeed(t, handler, "/feed.xml?"+query, "GET", "")
		if invalid.Code != 400 || invalid.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("query %q = %d", query, invalid.Code)
		}
	}
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest("GET", "https://example.test/", nil))
	if !strings.Contains(page.Body.String(), `rel="alternate" type="application/rss+xml"`) || !strings.Contains(page.Body.String(), "RSS 订阅") {
		t.Fatal("missing discoverable subscription entry")
	}
	snapshot := server.siteSettings.Snapshot()
	settings := snapshot.Settings
	settings.API.PublicPostsEnabled = false
	if _, fields, err := server.siteSettings.Save(snapshot.Revision, settings); err != nil || len(fields) != 0 {
		t.Fatalf("disable API = %v %v", fields, err)
	}
	disabled, _ := getFeed(t, handler, "/feed.xml", "GET", response.Header().Get("ETag"))
	if disabled.Code != 404 || disabled.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("disabled = %d", disabled.Code)
	}
	page = httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest("GET", "https://example.test/", nil))
	if strings.Contains(page.Body.String(), `href="/feed.xml"`) {
		t.Fatal("disabled feed still advertised")
	}
	snapshot = server.siteSettings.Snapshot()
	settings = snapshot.Settings
	settings.API.PublicPostsEnabled = true
	settings.Operations.MaintenanceEnabled = true
	if _, fields, err := server.siteSettings.Save(snapshot.Revision, settings); err != nil || len(fields) != 0 {
		t.Fatalf("enable maintenance = %v %v", fields, err)
	}
	maintenance, _ := getFeed(t, handler, "/feed.xml", "GET", "")
	if maintenance.Code != 503 || maintenance.Header().Get("Retry-After") != "300" {
		t.Fatalf("maintenance = %d", maintenance.Code)
	}
}

type unavailableFeedRepository struct{ content.Repository }

func (r unavailableFeedRepository) SearchPublished(context.Context, content.PublishedQuery) (content.PublishedPage, error) {
	return content.PublishedPage{}, errors.New("private-storage-diagnostic")
}

func TestRSSRepositoryFailure(t *testing.T) {
	server, repository := newFeedServer(t)
	server.repository = unavailableFeedRepository{repository}
	w, _ := getFeed(t, server.routes(), "/feed.xml", "GET", "")
	if w.Code != 500 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "private-storage-diagnostic") {
		t.Fatalf("failure = %d %s", w.Code, w.Body)
	}
}
