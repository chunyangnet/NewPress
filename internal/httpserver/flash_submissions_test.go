package httpserver

import (
	"encoding/json"
	"net/http"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func TestFlashSubmissionReviewLifecycle(t *testing.T) {
	_, handler, manager, repo, editorCookie, editor := newPersistentAdminTestServer(t)
	_, cookie, author := createAndLoginTestUser(t, manager, editor, "flash-author", "flash-author@example.test", "投稿者", "subscriber")
	body := flashWriteInput{ExpectedUserID: author.UserID, Action: "save", Title: "投稿标题", Body: "投稿正文", Comments: true}
	send := func(c *http.Cookie, p auth.Principal, admin bool, key string, want int) {
		t.Helper()
		endpoint := "/api/v1/flash/submissions"
		if admin {
			endpoint = "/api/v1/admin/flash"
		}
		payload, _ := json.Marshal(body)
		result := serveContentAPI(handler, c, "POST", "https://example.test"+endpoint, string(payload), map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": p.CSRFToken, "Idempotency-Key": key})
		if result.Code != want {
			t.Fatalf("%s: %d %s", body.Action, result.Code, result.Body.String())
		}
		if want == 200 {
			var response struct{ ID, Version int64 }
			if err := json.Unmarshal(result.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			body.ID, body.Version = response.ID, response.Version
		}
	}
	send(cookie, author, false, "flash-save-00001", 200)
	body.Action = "publish"
	send(cookie, author, false, "flash-publish-denied", 403)
	body.Action = "submit"
	send(cookie, author, false, "flash-submit-0001", 200)
	body.Action = "save"
	send(cookie, author, false, "flash-save-locked", 409)
	body.ExpectedUserID, body.Action, body.ReviewNote = editor.UserID, "reject", "请补充来源"
	send(editorCookie, editor, true, "flash-reject-0001", 200)
	post, err := repo.GetByID(t.Context(), body.ID)
	if err != nil || post.Status != content.StatusDraft || content.FlashMetadata(post).ReviewNote != "请补充来源" {
		t.Fatalf("reject: %#v %v", post, err)
	}
	body.ExpectedUserID, body.Action, body.ReviewNote = author.UserID, "submit", ""
	body.SourceURL = "https://example.test/original"
	send(cookie, author, false, "flash-resubmit-01", 200)
	body.ExpectedUserID, body.Action = editor.UserID, "publish"
	send(editorCookie, editor, true, "flash-publish-01", 200)
	post, err = repo.GetByID(t.Context(), body.ID)
	if err != nil || post.Status != content.StatusPublish || content.FlashMetadata(post).ReviewNote != "" {
		t.Fatalf("publish: %#v %v", post, err)
	}
	body.Action = "unpublish"
	send(editorCookie, editor, true, "flash-unpublish-1", 200)
	body.Action = "trash"
	send(editorCookie, editor, true, "flash-trash-0001", 200)
	body.Action = "publish"
	send(editorCookie, editor, true, "flash-trash-denied", 409)
	body.Action = "restore"
	send(editorCookie, editor, true, "flash-restore-01", 200)
}
