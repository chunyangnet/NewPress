package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

func TestPublicCommentReceiptLookupNeverCreates(t *testing.T) {
	s, handler, users, repo, _, owner := newPersistentAdminTestServer(t)
	_, member, _ := createAndLoginTestUser(t, users, owner, "lookup-reader", "lookup@example.test", "查询读者", "subscriber")
	post, err := repo.Create(t.Context(), content.PostInput{Title: "关闭后核对", Slug: "closed-receipt", AuthorID: owner.UserID, Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour), CommentStatus: content.CommentOpen})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "comments.json")
	s.comments, err = comment.NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	setPolicy := func(open, login, api bool) {
		t.Helper()
		settings := s.siteSettings.Snapshot()
		settings.Settings.Discussion.PublicSubmissionsEnabled = open
		settings.Settings.Discussion.RequireLogin = login
		settings.Settings.Discussion.RequireModeration = false
		settings.Settings.Discussion.RateLimitPerTenMinutes = 1
		settings.Settings.API.PublicPostsEnabled = api
		if _, fields, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil || len(fields) > 0 {
			t.Fatal(fields, err)
		}
	}
	setPolicy(true, false, true)
	guest := int64(0)
	body := publicCommentCreateAPIRequest{ExpectedUserID: &guest, FormToken: s.commentGuard.formToken(post.ID, time.Now()), AuthorName: "原读者", Email: "reader@example.test", Content: "原内容\n\n保留空行"}
	endpoint := "https://example.test/api/v1/posts/" + post.Slug + "/comments"
	headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "Idempotency-Key": "closed-original"}
	raw, _ := json.Marshal(body)
	created := serveContentAPI(handler, nil, http.MethodPost, endpoint, string(raw), headers)
	var original publicCommentCreateAPIResponse
	if err := json.Unmarshal(created.Body.Bytes(), &original); err != nil || created.Code != http.StatusCreated || original.Comment == nil {
		t.Fatal(created.Code, created.Body.String(), err)
	}
	query := func(payload publicCommentCreateAPIRequest, key string, cookie *http.Cookie, origin string, want int, code string) *httptest.ResponseRecorder {
		t.Helper()
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(payload)
		headers := map[string]string{"Origin": origin, "Content-Type": "application/json"}
		if key != "" {
			headers["Idempotency-Key"] = key
		}
		response := serveContentAPI(handler, cookie, http.MethodPost, endpoint+"/receipt", string(raw), headers)
		if response.Code != want || (code != "" && !strings.Contains(response.Body.String(), code)) {
			t.Fatalf("%s: %d want %d / %s: %s", key, response.Code, want, code, response.Body.String())
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("receipt query changed comments, receipts or notification events", err)
		}
		return response
	}
	for _, open := range []bool{false, true} {
		setPolicy(open, true, true)
		// A guest receipt remains queryable after both closing and reopening,
		// despite a new login policy and an exhausted submission allowance.
		response := query(body, "closed-original", nil, "https://example.test", http.StatusOK, "")
		var result publicCommentCreateAPIResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || !result.Replayed || result.Comment == nil || result.Comment.ID != original.Comment.ID || response.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatal(result, err)
		}
		query(body, "not-yet-received", nil, "https://example.test", http.StatusNotFound, "comments.submission_not_found")
	}
	// Even with new submissions fully permitted, a missing receipt is only a
	// point-in-time miss, never authorization to create or acknowledge delivery.
	setPolicy(true, false, true)
	query(body, "still-not-received", nil, "https://example.test", http.StatusNotFound, "comments.submission_not_found")
	query(body, "closed-original", member, "https://example.test", http.StatusConflict, "comments.identity_changed")
	query(body, "closed-original", nil, "https://external.test", http.StatusForbidden, "comments.origin_rejected")
	query(body, "", nil, "https://example.test", http.StatusBadRequest, "comments.submission_identity_required")
	query(body, "short", nil, "https://example.test", http.StatusBadRequest, "request.invalid_idempotency_key")
	changed := body
	changed.ExpectedUserID = nil
	query(changed, "closed-original", nil, "https://example.test", http.StatusBadRequest, "comments.submission_identity_required")
	changed = body
	changed.Content += "改过正文"
	query(changed, "closed-original", nil, "https://example.test", http.StatusConflict, "comments.submission_conflict")
	changed = body
	changed.FormToken = "expired"
	query(changed, "closed-original", nil, "https://example.test", http.StatusForbidden, "comments.form_token_invalid")

	setPolicy(false, true, true)
	s.commentGuard = newCommentGuard()
	query(body, "closed-original", nil, "https://example.test", http.StatusForbidden, "comments.form_token_invalid")
	response := serveContentAPI(handler, nil, http.MethodGet, endpoint, "", nil)
	var listing publicCommentsAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || listing.Submission.Open || listing.Submission.FormToken == "" {
		t.Fatal(listing, err)
	}
	body.FormToken = listing.Submission.FormToken
	query(body, "closed-original", nil, "https://example.test", http.StatusOK, "")
	current, err := s.comments.ChangeStatus(t.Context(), original.Comment.ID, comment.Mutation{ExpectedVersion: 1}, comment.StatusTrash)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.comments.Delete(t.Context(), current.ID, comment.Mutation{ExpectedVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	s.comments, err = comment.NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	response = query(body, "closed-original", nil, "https://example.test", http.StatusOK, "")
	var result publicCommentCreateAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Status != "received" || result.Comment != nil || !result.Replayed {
		t.Fatal(result, err)
	}
	setPolicy(false, true, false)
	query(body, "closed-original", nil, "https://example.test", http.StatusNotFound, "request.not_found")
}

func TestClosedCommentPageOffersRecoveryWithoutComposer(t *testing.T) {
	s, handler, _, repo, _, owner := newPersistentAdminTestServer(t)
	post, err := repo.Create(t.Context(), content.PostInput{Title: "只读恢复", Slug: "closed-page", AuthorID: owner.UserID, Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour), CommentStatus: content.CommentClosed})
	if err != nil {
		t.Fatal(err)
	}
	for _, api := range []bool{true, false} {
		settings := s.siteSettings.Snapshot()
		settings.Settings.API.PublicPostsEnabled = api
		if _, fields, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil || len(fields) > 0 {
			t.Fatal(fields, err)
		}
		page := serveCookieRequest(handler, http.MethodGet, "https://example.test/posts/"+post.Slug, nil, nil)
		html := page.Body.String()
		if page.Code != http.StatusOK || !strings.Contains(html, "data-comment-closed") || strings.Contains(html, "data-comment-form") || strings.Contains(html, "data-comment-login-gate") || strings.Contains(html, "data-comment-reply ") {
			t.Fatal("closed page contains active composer or lacks recovery host", page.Code)
		}
		if strings.Contains(html, "data-comment-api=") != api || strings.Contains(html, "data-comment-token=") != api {
			t.Fatal("closed recovery does not follow the public API setting")
		}
		view, err := s.publicCommentsView(httptest.NewRequest(http.MethodGet, "https://example.test/posts/"+post.Slug, nil), post, 1, publicCommentForm{}, nil, adminNotice{})
		if err != nil || view.Open || (view.FormToken != "") != api || (api && !s.commentGuard.verifyFormToken(view.FormToken, post.ID, time.Now())) {
			t.Fatal("closed recovery token invalid", view, err)
		}
	}
}
