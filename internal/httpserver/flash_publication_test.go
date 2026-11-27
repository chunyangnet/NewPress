package httpserver

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func flashPublicationRequest(t *testing.T, handler http.Handler, cookie *http.Cookie, principal auth.Principal, key string, body flashWriteInput) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/flash", string(payload), map[string]string{
		"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": principal.CSRFToken, "Idempotency-Key": key,
	})
}

func flashPublicationBody(principal auth.Principal, post content.Post, action, raw string) flashWriteInput {
	return flashWriteInput{ExpectedUserID: principal.UserID, ID: post.ID, Version: post.Version, Action: action,
		Title: post.Title, Body: post.ContentSource, Comments: post.CommentStatus == content.CommentOpen, PublishedAt: raw}
}

func TestFlashPublicationPreservesExactInstant(t *testing.T) {
	for _, storage := range []string{"memory", "file"} {
		t.Run(storage, func(t *testing.T) {
			s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
			file := filepath.Join(t.TempDir(), "content.json")
			if storage == "file" {
				repository, err := content.NewFileRepository(file, false)
				if err != nil {
					t.Fatal(err)
				}
				s.repository = repository
			}
			for i, test := range []struct{ zone, instant string }{
				{"UTC", "2026-02-03T12:34:56.123456789Z"},
				{"America/New_York", "2026-11-01T05:30:17.123456789Z"},
				{"America/New_York", "2026-11-01T06:30:43.987654321Z"},
				{"Asia/Kathmandu", "2026-02-03T12:34:56.987654321Z"},
				{"Asia/Shanghai", time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)},
			} {
				t.Run(fmt.Sprint(i), func(t *testing.T) {
					setPublicationTestTimezone(t, s, test.zone)
					original := publicationTestInstant(t, test.instant)
					post, err := s.repository.Create(t.Context(), content.PostInput{Type: content.TypeFlash, AuthorID: owner.UserID,
						Title: "Keep the exact publication instant", Slug: fmt.Sprintf("exact-flash-%d", i), ContentSource: "Original body", Status: content.StatusPublish, PublishedAt: original})
					if err != nil {
						t.Fatal(err)
					}
					page := serveContentAPI(handler, cookie, "GET", fmt.Sprintf("https://example.test/admin/flash/?edit=%d", post.ID), "", nil)
					if page.Code != http.StatusOK {
						t.Fatalf("editor: %d %s", page.Code, page.Body.String())
					}
					body := flashPublicationBody(owner, post, "save", publicationHTMLInput(t, page.Body.String(), "published_at"))
					body.PublishedTimezone = publicationHTMLInput(t, page.Body.String(), "published_timezone")
					if body.PublishedTimezone != test.zone {
						t.Fatalf("editor timezone: got=%q want=%q", body.PublishedTimezone, test.zone)
					}
					body.Title = "Title changed without changing publication time"
					response := flashPublicationRequest(t, handler, cookie, owner, fmt.Sprintf("exact-flash-save-%d", i), body)
					if response.Code != http.StatusOK {
						t.Fatalf("save: %d %s", response.Code, response.Body.String())
					}
					if storage == "file" {
						reloaded, err := content.NewFileRepository(file, false)
						if err != nil {
							t.Fatal(err)
						}
						s.repository = reloaded
					}
					saved, err := s.repository.GetByID(t.Context(), post.ID)
					if err != nil || !saved.PublishedAt.Equal(original) || saved.Title != body.Title || saved.Version != post.Version+1 || saved.Status != post.Status {
						t.Errorf("save changed publication instant: got=%s want=%s version=%d err=%v", saved.PublishedAt.Format(time.RFC3339Nano), original.Format(time.RFC3339Nano), saved.Version, err)
					}
				})
			}
		})
	}
}

func TestFlashPublicationRejectsInvalidLocalTimes(t *testing.T) {
	for _, test := range []struct{ name, raw, problem string }{
		{"gap", "2026-03-08T02:30", "不存在"},
		{"repeat", "2026-11-01T01:30", "出现两次"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
			setPublicationTestTimezone(t, s, "America/New_York")
			body := flashWriteInput{ExpectedUserID: owner.UserID, Action: "publish", Title: "Reject ambiguous time", Body: "News body", PublishedAt: test.raw}
			body.PublishedTimezone = "America/New_York"
			key := "invalid-flash-time-" + test.name
			response := flashPublicationRequest(t, handler, cookie, owner, key, body)
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), test.problem) {
				t.Fatalf("invalid time accepted: %d %s", response.Code, response.Body.String())
			}
			result, err := s.repository.ListAdmin(t.Context(), content.AdminQuery{Type: content.TypeFlash, Limit: 20})
			if err != nil || result.Total != 0 {
				t.Fatalf("failed validation persisted content: %#v %v", result, err)
			}
			body.PublishedAt = "2026-11-01T02:30"
			response = flashPublicationRequest(t, handler, cookie, owner, key, body)
			if response.Code != http.StatusOK {
				t.Fatalf("validation consumed retry key: %d %s", response.Code, response.Body.String())
			}
			var saved struct{ ID int64 }
			if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
				t.Fatal(err)
			}
			post, err := s.repository.GetByID(t.Context(), saved.ID)
			if err != nil || !post.PublishedAt.Equal(publicationTestInstant(t, "2026-11-01T07:30:00Z")) {
				t.Fatalf("corrected time: %s %v", post.PublishedAt, err)
			}
		})
	}
}

func TestFlashPublicationClearsSchedule(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	post, err := s.repository.Create(t.Context(), content.PostInput{Type: content.TypeFlash, AuthorID: owner.UserID, Title: "Scheduled news",
		Slug: "clear-flash-schedule", ContentSource: "News body", Status: content.StatusFuture, PublishedAt: time.Now().Add(48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	rejected := flashPublicationRequest(t, handler, cookie, owner, "clear-flash-schedule", flashPublicationBody(owner, post, "save", ""))
	if rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("scheduled save accepted an empty time: %d %s", rejected.Code, rejected.Body.String())
	}
	unchanged, err := s.repository.GetByID(t.Context(), post.ID)
	if err != nil || unchanged.Version != post.Version || !unchanged.PublishedAt.Equal(post.PublishedAt) {
		t.Fatalf("failed scheduled save changed the record: %#v %v", unchanged, err)
	}
	response := flashPublicationRequest(t, handler, cookie, owner, "clear-flash-schedule", flashPublicationBody(owner, post, "publish", ""))
	if response.Code != http.StatusOK {
		t.Fatalf("publish now: %d %s", response.Code, response.Body.String())
	}
	saved, err := s.repository.GetByID(t.Context(), post.ID)
	if err != nil || saved.Status != content.StatusPublish || saved.PublishedAt.Before(before) || saved.PublishedAt.After(time.Now()) {
		t.Fatalf("clearing schedule did not publish now: status=%s time=%s err=%v", saved.Status, saved.PublishedAt, err)
	}
	public := serveContentAPI(handler, nil, "GET", "https://example.test"+content.PublicPath(saved), "", nil)
	if public.Code != http.StatusOK {
		t.Fatalf("published news is inaccessible: %d", public.Code)
	}
}

func TestFlashPublicationTimezoneBindingAndReplay(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	setPublicationTestTimezone(t, s, "America/New_York")
	body := flashWriteInput{ExpectedUserID: owner.UserID, Action: "publish", Title: "Timezone-bound flash", Body: "News body", PublishedAt: "2026-11-01T02:30"}
	key := "flash-timezone-binding"
	for _, marker := range []string{"", "UTC"} {
		body.PublishedTimezone = marker
		response := flashPublicationRequest(t, handler, cookie, owner, key, body)
		var problem struct {
			Error struct {
				Code    string `json:"code"`
				Details struct {
					Timezone string `json:"published_timezone"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || response.Code != http.StatusUnprocessableEntity || problem.Error.Code != "flash.timezone_changed" || problem.Error.Details.Timezone != "America/New_York" {
			t.Fatalf("missing/stale timezone %q: %d %s err=%v", marker, response.Code, response.Body.String(), err)
		}
	}
	result, err := s.repository.ListAdmin(t.Context(), content.AdminQuery{Type: content.TypeFlash, Limit: 20})
	if err != nil || result.Total != 0 {
		t.Fatalf("timezone validation persisted content: %#v %v", result, err)
	}
	body.PublishedTimezone = "America/New_York"
	response := flashPublicationRequest(t, handler, cookie, owner, key, body)
	var receipt struct {
		ID, Version int64
		Replayed    bool
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil || response.Code != http.StatusOK || receipt.Replayed {
		t.Fatalf("confirmed timezone save: %d %s err=%v", response.Code, response.Body.String(), err)
	}
	setPublicationTestTimezone(t, s, "UTC")
	replay := flashPublicationRequest(t, handler, cookie, owner, key, body)
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"replayed":true`) {
		t.Fatalf("changed timezone prevented receipt confirmation: %d %s", replay.Code, replay.Body.String())
	}
	post, err := s.repository.GetByID(t.Context(), receipt.ID)
	if err != nil || post.Version != receipt.Version || !post.PublishedAt.Equal(publicationTestInstant(t, "2026-11-01T07:30:00Z")) {
		t.Fatalf("replay changed the committed instant: %#v %v", post, err)
	}
	body.PublishedTimezone = "UTC"
	conflict := flashPublicationRequest(t, handler, cookie, owner, key, body)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("same key accepted a changed timezone: %d %s", conflict.Code, conflict.Body.String())
	}
}

func TestFlashWorkflowActionsPreservePublication(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	for i, test := range []struct{ action, from, to string }{
		{"withdraw", content.StatusPending, content.StatusDraft},
		{"reject", content.StatusPending, content.StatusDraft},
		{"unpublish", content.StatusFuture, content.StatusDraft},
		{"trash", content.StatusPublish, content.StatusTrash},
		{"restore", content.StatusTrash, content.StatusDraft},
	} {
		t.Run(test.action, func(t *testing.T) {
			at := time.Now().Add(-time.Hour)
			if test.from == content.StatusFuture {
				at = time.Now().Add(48 * time.Hour)
			}
			post, err := s.repository.Create(t.Context(), content.PostInput{Type: content.TypeFlash, AuthorID: owner.UserID, Title: "Saved title",
				Slug: fmt.Sprintf("workflow-flash-%d", i), ContentSource: "Saved body", Status: test.from, PublishedAt: at,
				Flash: &content.FlashFields{SourceName: "Saved source", Important: true}})
			if err != nil {
				t.Fatal(err)
			}
			body := flashPublicationBody(owner, post, test.action, "unfinished-date")
			body.Title, body.Body, body.SourceName, body.ReviewNote = "Unsaved title", "Unsaved body", "Unsaved source", "Please check the source"
			response := flashPublicationRequest(t, handler, cookie, owner, "flash-workflow-"+test.action, body)
			if response.Code != http.StatusOK {
				t.Fatalf("state action parsed an unsaved timestamp: %d %s", response.Code, response.Body.String())
			}
			saved, err := s.repository.GetByID(t.Context(), post.ID)
			if err != nil || saved.Status != test.to || !saved.PublishedAt.Equal(at) || saved.Title != post.Title || saved.ContentSource != post.ContentSource || content.FlashMetadata(saved).SourceName != "Saved source" || !content.FlashMetadata(saved).Important {
				t.Fatalf("state action changed saved editorial fields: %#v %v", saved, err)
			}
		})
	}
}

func TestFlashPublicationLegacyReceiptRemainsReplayable(t *testing.T) {
	s, handler, _, repo, cookie, owner := newPersistentAdminTestServer(t)
	setPublicationTestTimezone(t, s, "UTC")
	const key = "legacy-flash-publication"
	// This is the exact server encoding used before published_timezone existed.
	// Its fingerprint must still confirm an already committed request.
	payload := fmt.Sprintf(`{"expected_user_id":%d,"id":0,"version":0,"action":"publish","title":"Legacy flash","body":"Original body","source_name":"","source_url":"","important":false,"review_note":"","published_at":"2026-11-01T02:30","comments":false}`, owner.UserID)
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	receipt, err := repo.Submit(t.Context(), content.Submission{
		Key: digest(fmt.Sprint(owner.UserID) + "\x00/api/v1/admin/flash\x00" + key), Fingerprint: digest(payload), Actor: owner.Username, Action: "create",
		Input: content.PostInput{Type: content.TypeFlash, AuthorID: owner.UserID, Title: "Legacy flash", ContentSource: "Original body", Slug: "legacy-time-receipt",
			Status: content.StatusPublish, PublishedAt: publicationTestInstant(t, "2026-11-01T07:30:00Z")},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/flash", payload, map[string]string{
		"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": owner.CSRFToken, "Idempotency-Key": key,
	})
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"replayed":true`) {
		t.Fatalf("legacy receipt fingerprint changed: %d %s", response.Code, response.Body.String())
	}
	post, err := repo.GetByID(t.Context(), receipt.Receipt.PostID)
	if err != nil || post.Version != receipt.Receipt.Version || !post.PublishedAt.Equal(publicationTestInstant(t, "2026-11-01T07:30:00Z")) {
		t.Fatalf("legacy replay rewrote content: %#v %v", post, err)
	}
}
