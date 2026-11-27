package httpserver

import (
	"net/http"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestRSSLatestArticlesAreNotDisplacedByOldPinnedPosts(t *testing.T) {
	server, repository := newFeedServer(t)
	base := time.Now().Add(-24 * time.Hour)
	pinned := true
	for _, in := range []content.PostInput{
		{Title: "旧置顶文章", Slug: "feed-old-pin", Status: content.StatusPublish, PublishedAt: base, Pinned: &pinned},
		{Title: "新置顶文章", Slug: "feed-recent-pin", Status: content.StatusPublish, PublishedAt: base.Add(time.Hour), Pinned: &pinned},
		{Title: "最新文章", Slug: "feed-latest", Status: content.StatusPublish, PublishedAt: base.Add(2 * time.Hour)},
	} {
		if _, err := repository.Create(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	response, feed := getFeed(t, server.routes(), "/feed.xml?limit=2", http.MethodGet, "")
	if response.Code != http.StatusOK || len(feed.Channel.Items) != 2 {
		t.Fatalf("feed = %d %s", response.Code, response.Body)
	}
	if feed.Channel.Items[0].Title != "最新文章" || feed.Channel.Items[1].Title != "新置顶文章" {
		t.Fatalf("pin priority displaced the latest feed entries: %+v", feed.Channel.Items)
	}
}
