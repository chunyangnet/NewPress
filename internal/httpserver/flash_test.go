package httpserver

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestFlashDiscussionAndEngagementVisibility(t *testing.T) {
	_, handler, manager, repo, _, owner := newPersistentAdminTestServer(t)
	_, cookie, reader := createAndLoginTestUser(t, manager, owner, "flash-reader", "flash-reader@example.test", "快报读者", "subscriber")
	post, err := repo.Create(t.Context(), content.PostInput{Type: content.TypeFlash, Title: "快报评论测试", Slug: "flash-discussion", ContentSource: "可讨论的快报正文", Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour), CommentStatus: content.CommentOpen, Flash: &content.FlashFields{ReviewNote: "private review note"}})
	if err != nil {
		t.Fatal(err)
	}
	page := serveContentAPI(handler, nil, "GET", "https://example.test/flash/flash-discussion/", "", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "/api/v1/flash/flash-discussion/comments") || strings.Contains(page.Body.String(), "private review note") {
		t.Fatalf("flash page: %d %s", page.Code, page.Body.String())
	}
	api := serveContentAPI(handler, nil, "GET", "https://example.test/api/v1/flash/flash-discussion/comments", "", nil)
	if api.Code != 200 {
		t.Fatalf("comments: %d %s", api.Code, api.Body.String())
	}
	headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": reader.CSRFToken}
	result := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/social/engagement", fmt.Sprintf(`{"kind":"bookmark","target_id":%d,"active":true}`, post.ID), headers)
	if result.Code != 200 {
		t.Fatalf("bookmark: %d %s", result.Code, result.Body.String())
	}
	library := serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/social/library", "", nil)
	if library.Code != 200 || !strings.Contains(library.Body.String(), content.PublicPath(post)) {
		t.Fatalf("flash library: %d %s", library.Code, library.Body.String())
	}
	_, err = repo.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusDraft)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/flash/flash-discussion/", "/api/v1/flash/flash-discussion/comments"} {
		response := serveContentAPI(handler, nil, "GET", "https://example.test"+path, "", nil)
		if response.Code != 404 {
			t.Fatalf("private %s: %d", path, response.Code)
		}
	}
	result = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/social/engagement", fmt.Sprintf(`{"kind":"like","target_id":%d,"active":true}`, post.ID), headers)
	if result.Code != 404 {
		t.Fatalf("private like: %d", result.Code)
	}
}
