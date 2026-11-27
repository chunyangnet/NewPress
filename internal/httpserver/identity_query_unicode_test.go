package httpserver

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

var identitySearchRoutes = []struct {
	path      string
	badStatus int
	marker    string
}{
	{"/admin/users/?", http.StatusBadRequest, "unicode-reader-marker"},
	{"/api/v1/social/contacts?", http.StatusBadRequest, "unicode-reader-marker"},
	{"/admin/comments/?status=all&", http.StatusBadRequest, "unicode-comment-marker"},
	{"/api/v1/admin/comments?status=all&", http.StatusBadRequest, "unicode-comment-marker"},
	{"/api/v1/social/own-content?kind=comments&", http.StatusUnprocessableEntity, "unicode-comment-marker"},
}

func TestIdentitySearchHTTPRejectsMalformedUTF8(t *testing.T) {
	_, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	for _, route := range identitySearchRoutes {
		for name, search := range map[string]string{"bad_byte": "\xff", "truncated": "\xe4\xb8", "surrogate": "\xed\xa0\x80", "embedded": "before\xffafter"} {
			t.Run(route.path+"/"+name, func(t *testing.T) {
				response := serveContentAPI(handler, cookie, http.MethodGet, "https://example.test"+route.path+"q="+url.QueryEscape(search), "", nil)
				if response.Code != route.badStatus {
					t.Fatalf("invalid search status=%d want=%d", response.Code, route.badStatus)
				}
			})
		}
	}
}

func TestIdentitySearchHTTPPreservesUnicodeAndPrivacy(t *testing.T) {
	server, handler, manager, repository, cookie, owner := newPersistentAdminTestServer(t)
	_, err := manager.CreateUser(t.Context(), owner, auth.UserInput{
		Username: "unicode-reader", Email: "private-unicode@example.test", DisplayName: "unicode-reader-marker 中文 🌐 �",
		Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"subscriber"},
	}, "Unicode search reader password 2026")
	if err != nil {
		t.Fatal(err)
	}
	post, err := repository.Create(t.Context(), content.PostInput{Title: "评论搜索文章", Slug: "unicode-comment-search", Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.comments.Create(t.Context(), comment.CreateInput{
		PostID: post.ID, UserID: owner.UserID, AuthorName: owner.Label(), AuthorEmail: owner.Email,
		Status: comment.StatusApproved, ContentSource: "unicode-comment-marker 中文 🌐 �",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.comments.Create(t.Context(), comment.CreateInput{
		PostID: post.ID, AuthorName: "访客", AuthorEmail: "guest-private@example.test",
		Status: comment.StatusApproved, ContentSource: "other-comment-marker 中文 🌐 �",
	}); err != nil {
		t.Fatal(err)
	}
	for _, route := range identitySearchRoutes {
		for _, needle := range []string{"中文", "🌐", "�"} {
			t.Run(route.path+"/"+needle, func(t *testing.T) {
				response := serveContentAPI(handler, cookie, http.MethodGet, "https://example.test"+route.path+"q="+url.QueryEscape(" \u3000"+needle+"\t"), "", nil)
				body := response.Body.String()
				if response.Code != http.StatusOK || !strings.Contains(body, route.marker) {
					t.Fatalf("valid search status=%d marker present=%v", response.Code, strings.Contains(body, route.marker))
				}
				if strings.Contains(route.path, "/contacts") && strings.Contains(body, "private-unicode@example.test") {
					t.Error("contact search leaked a private email")
				}
				if strings.Contains(route.path, "/own-content") && (strings.Contains(body, "other-comment-marker") || strings.Contains(body, owner.Email)) {
					t.Error("own comment search leaked another writer or private identity")
				}
			})
		}
	}
}
