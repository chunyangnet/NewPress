package httpserver

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"newpress.local/newpress/internal/comment"
)

func TestCommentEditorRejectsMalformedEmailWithoutRevision(t *testing.T) {
	for name, email := range map[string]string{"bad_byte": "bad\xff@example.test", "truncated": "bad\xe4\xb8@example.test", "surrogate": "bad\xed\xa0\x80@example.test"} {
		t.Run(name, func(t *testing.T) {
			_, handler, store, cookie, principal := newCommentHTTPTestServer(t)
			root, err := store.Create(t.Context(), comment.CreateInput{PostID: 1, AuthorName: "读者", AuthorEmail: "reader@example.test", Status: comment.StatusApproved, ContentSource: "原评论"})
			if err != nil {
				t.Fatal(err)
			}
			values := commentEditorEditValues(principal.CSRFToken, root.Version, root.AuthorName, email, "不应覆盖", "approved")
			response := serveCommentEditorForm(handler, cookie, http.MethodPost, "https://example.test/admin/comments/"+strconv.FormatInt(root.ID, 10)+"/edit/", values, "https://example.test")
			if response.Code != http.StatusUnprocessableEntity {
				t.Errorf("invalid email status=%d want=422", response.Code)
			}
			got, err := store.Get(t.Context(), root.ID)
			if err != nil || got != root {
				t.Errorf("invalid email changed comment: %#v, %v", got, err)
			}
			revisions, err := store.ListRevisions(t.Context(), root.ID, 20)
			if err != nil || len(revisions) != 0 {
				t.Errorf("invalid email created revisions: %d, %v", len(revisions), err)
			}
		})
	}
	t.Run("valid_unicode", func(t *testing.T) {
		_, handler, store, cookie, principal := newCommentHTTPTestServer(t)
		root, err := store.Create(t.Context(), comment.CreateInput{PostID: 1, AuthorName: "读者", AuthorEmail: "reader@example.test", Status: comment.StatusApproved, ContentSource: "原评论"})
		if err != nil {
			t.Fatal(err)
		}
		values := commentEditorEditValues(principal.CSRFToken, root.Version, "读者 🌐 �", "读者�@example.test", "有效正文 中文 🌐 �", "approved")
		response := serveCommentEditorForm(handler, cookie, http.MethodPost, "https://example.test/admin/comments/"+strconv.FormatInt(root.ID, 10)+"/edit/", values, "https://example.test")
		if response.Code != http.StatusSeeOther {
			t.Fatalf("valid Unicode email status=%d", response.Code)
		}
		updated, err := store.Get(t.Context(), root.ID)
		if err != nil || updated.AuthorEmail != values.Get("author_email") {
			t.Errorf("valid email changed: %#v, %v", updated, err)
		}
	})
}

func TestPublicCommentFormRejectsMalformedEmailBeforeStorage(t *testing.T) {
	for name, email := range map[string]string{"bad_byte": "bad\xff@example.test", "truncated": "bad\xe4\xb8@example.test", "surrogate": "bad\xed\xa0\x80@example.test"} {
		t.Run(name, func(t *testing.T) {
			_, handler, store, _, _ := newCommentHTTPTestServer(t)
			article := serveCommentRequest(handler, nil, http.MethodGet, "https://example.test/posts/welcome-to-newpress", "", "", "198.51.100.10:4000")
			if article.Code != http.StatusOK {
				t.Fatal(article.Code)
			}
			formToken := extractCommentToken(t, article.Body.String())
			before, err := store.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			values := url.Values{"form_token": {formToken}, "parent_id": {"0"}, "author_name": {"访客"}, "email": {email}, "content": {"不应保存的评论"}, "website": {""}}
			response := servePublicCommentForm(handler, "welcome-to-newpress", values, "https://example.test", "198.51.100.10:4000")
			if response.Code != http.StatusUnprocessableEntity {
				t.Errorf("invalid public email status=%d want=422", response.Code)
			}
			after, err := store.Snapshot(t.Context())
			if err != nil || after.Total != before.Total || !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Errorf("invalid public email changed comments: %#v, %v", after, err)
			}
		})
	}
}
