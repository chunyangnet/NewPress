package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

func TestPublicCommentAtomicRetryLifecycle(t *testing.T) {
	s, handler, users, repo, _, owner := newPersistentAdminTestServer(t)
	_, member, _ := createAndLoginTestUser(t, users, owner, "retry-reader", "retry@example.test", "登录读者", "subscriber")
	post, err := repo.Create(t.Context(), content.PostInput{Title: "可确认评论", Slug: "comment-retry", AuthorID: owner.UserID, Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour), CommentStatus: content.CommentOpen})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "comments.json")
	s.comments, err = comment.NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	settings := s.siteSettings.Snapshot()
	settings.Settings.Discussion.RequireLogin = false
	settings.Settings.Discussion.RequireModeration = false
	settings.Settings.Discussion.RateLimitPerTenMinutes = 1
	if _, fields, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	endpoint := "https://example.test/api/v1/posts/" + post.Slug + "/comments"
	body := publicCommentCreateAPIRequest{FormToken: s.commentGuard.formToken(post.ID, time.Now()), AuthorName: "原读者", Email: "guest@example.test", Content: strings.Repeat("原", 120)}
	send := func(key string, payload publicCommentCreateAPIRequest, cookie *http.Cookie, want int) (publicCommentCreateAPIResponse, *httptest.ResponseRecorder) {
		t.Helper()
		raw, _ := json.Marshal(payload)
		headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/json"}
		if key != "" {
			headers["Idempotency-Key"] = key
		}
		response := serveContentAPI(handler, cookie, http.MethodPost, endpoint, string(raw), headers)
		if response.Code != want {
			t.Fatalf("%s: %d want %d: %s", key, response.Code, want, response.Body.String())
		}
		var result publicCommentCreateAPIResponse
		_ = json.Unmarshal(response.Body.Bytes(), &result)
		return result, response
	}
	first, _ := send("retry-original", body, nil, http.StatusCreated)
	if first.SubmissionKey != "retry-original" || first.Replayed || first.Comment == nil {
		t.Fatal(first)
	}
	for range 3 {
		replay, response := send("retry-original", body, nil, http.StatusOK)
		if !replay.Replayed || replay.Comment.ID != first.Comment.ID || response.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatal(replay)
		}
	}
	// Confirmations do not spend another rate-limit allowance; new writes do.
	send("another-request", body, nil, http.StatusTooManyRequests)
	changed := body
	changed.Content = "改成新内容"
	_, response := send("retry-original", changed, nil, http.StatusConflict)
	if !strings.Contains(response.Body.String(), "comments.submission_conflict") {
		t.Fatal(response.Body.String())
	}
	_, response = send("retry-original", body, member, http.StatusConflict)
	if !strings.Contains(response.Body.String(), "comments.submission_conflict") {
		t.Fatal(response.Body.String())
	}
	guestID := int64(0)
	changed = body
	changed.ExpectedUserID = &guestID
	_, response = send("new-with-wrong-account", changed, member, http.StatusConflict)
	if !strings.Contains(response.Body.String(), "comments.identity_changed") {
		t.Fatal(response.Body.String())
	}
	// A process restart changes form tokens and source hashing, but not receipts.
	s.comments, err = comment.NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	s.commentGuard = newCommentGuard()
	send("retry-original", body, nil, http.StatusForbidden)
	get := serveContentAPI(handler, nil, http.MethodGet, endpoint, "", nil)
	var listing publicCommentsAPIResponse
	if err := json.Unmarshal(get.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	body.FormToken = listing.Submission.FormToken
	replay, _ := send("retry-original", body, nil, http.StatusOK)
	if replay.Comment.ID != first.Comment.ID {
		t.Fatal(replay)
	}
	// A later policy change rejects new writes, while old receipts remain usable.
	settings = s.siteSettings.Snapshot()
	settings.Settings.Discussion.PublicSubmissionsEnabled = false
	settings.Settings.Discussion.RequireLogin = true
	settings.Settings.Discussion.MaxLength = 100
	if _, fields, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	send("retry-original", body, nil, http.StatusOK)
	send("fresh-after-closed", body, nil, http.StatusConflict)
	get = serveContentAPI(handler, nil, http.MethodGet, endpoint, "", nil)
	if err := json.Unmarshal(get.Body.Bytes(), &listing); err != nil || listing.Submission.Open || listing.Submission.FormToken == "" {
		t.Fatal(listing, err)
	}
	current, err := s.comments.Edit(t.Context(), first.Comment.ID, comment.Mutation{ExpectedVersion: 1, Actor: "editor"}, comment.EditInput{AuthorName: "审核后称呼", AuthorEmail: body.Email, ContentSource: "审核后正文"})
	if err != nil {
		t.Fatal(err)
	}
	replay, _ = send("retry-original", body, nil, http.StatusOK)
	if replay.Comment.AuthorName != current.AuthorName || !strings.Contains(replay.Comment.ContentHTML, "审核后正文") {
		t.Fatal(replay)
	}
	for _, status := range []string{comment.StatusPending, comment.StatusSpam, comment.StatusTrash} {
		current, err = s.comments.ChangeStatus(t.Context(), current.ID, comment.Mutation{ExpectedVersion: current.Version}, status)
		if err != nil {
			t.Fatal(err)
		}
		replay, _ = send("retry-original", body, nil, http.StatusOK)
		if status == comment.StatusPending {
			if replay.Status != status {
				t.Fatal(replay)
			}
		} else if replay.Status != "received" || replay.Comment != nil {
			t.Fatal("hidden comment leaked", replay)
		}
	}
	if err := s.comments.Delete(t.Context(), current.ID, comment.Mutation{ExpectedVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	s.comments, err = comment.NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	replay, _ = send("retry-original", body, nil, http.StatusOK)
	if !replay.Replayed || replay.Status != "received" || replay.Comment != nil {
		t.Fatal(replay)
	}
	if _, err := s.comments.Get(t.Context(), current.ID); !errors.Is(err, comment.ErrNotFound) {
		t.Fatal("deleted comment recreated", err)
	}
}

func TestPublicCommentRetryConcurrentAndRejectedRequests(t *testing.T) {
	s, handler, _, repo, _, owner := newPersistentAdminTestServer(t)
	post, err := repo.Create(t.Context(), content.PostInput{Title: "并发评论", Slug: "concurrent-retry", AuthorID: owner.UserID, Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour), CommentStatus: content.CommentOpen})
	if err != nil {
		t.Fatal(err)
	}
	s.comments, _ = comment.NewFileStore("", false)
	settings := s.siteSettings.Snapshot()
	settings.Settings.Discussion.RequireLogin = false
	settings.Settings.Discussion.RequireModeration = false
	settings.Settings.Discussion.RateLimitPerTenMinutes = 20
	if _, fields, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	endpoint := "https://example.test/api/v1/posts/" + post.Slug + "/comments"
	body := publicCommentCreateAPIRequest{FormToken: s.commentGuard.formToken(post.ID, time.Now()), AuthorName: "读者", Email: "guest@example.test", Content: "相同内容"}
	raw, _ := json.Marshal(body)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			response := serveContentAPI(handler, nil, http.MethodPost, endpoint, string(raw), map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "Idempotency-Key": "concurrent-request"})
			if response.Code != http.StatusOK && response.Code != http.StatusCreated {
				t.Error(response.Code, response.Body.String())
			}
		}()
	}
	close(start)
	workers.Wait()
	list, err := s.comments.ListAdmin(t.Context(), comment.Query{PostID: post.ID})
	if err != nil || list.Total != 1 {
		t.Fatal(list, err)
	}
	for _, key := range []string{"short", "invalid key", strings.Repeat("a", 129)} {
		response := serveContentAPI(handler, nil, http.MethodPost, endpoint, string(raw), map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "Idempotency-Key": key})
		if response.Code != http.StatusBadRequest {
			t.Fatal(key, response.Code)
		}
	}
	// A known receipt still requires same-origin proof and a valid token.
	response := serveContentAPI(handler, nil, http.MethodPost, endpoint, string(raw), map[string]string{"Origin": "https://evil.test", "Content-Type": "application/json", "Idempotency-Key": "concurrent-request"})
	if response.Code != http.StatusForbidden {
		t.Fatal(response.Code)
	}
	body.Website = "https://bot.example.test"
	raw, _ = json.Marshal(body)
	response = serveContentAPI(handler, nil, http.MethodPost, endpoint, string(raw), map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "Idempotency-Key": "honeypot-request"})
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	list, err = s.comments.ListAdmin(t.Context(), comment.Query{PostID: post.ID})
	if err != nil || list.Total != 1 {
		t.Fatal("honeypot wrote", list, err)
	}
	// Moderation can delete a newly committed comment before the HTTP read.
	// The receipt still confirms delivery and must never publish stale content.
	s.comments = deleteImmediatelyAfterSubmission{s.comments}
	body.Website = ""
	raw, _ = json.Marshal(body)
	response = serveContentAPI(handler, nil, http.MethodPost, endpoint, string(raw), map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "Idempotency-Key": "deleted-before-response"})
	var result publicCommentCreateAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || result.Status != "received" || result.Comment != nil || !result.Replayed || response.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal(response.Code, result, err)
	}
}

type deleteImmediatelyAfterSubmission struct{ comment.Store }

func (store deleteImmediatelyAfterSubmission) CreateOnce(ctx context.Context, request comment.Submission) (comment.SubmissionResult, error) {
	result, err := store.Store.CreateOnce(ctx, request)
	if err == nil && !result.Replayed {
		err = store.Store.Delete(ctx, result.Receipt.CommentID, comment.Mutation{ExpectedVersion: 1})
	}
	return result, err
}
