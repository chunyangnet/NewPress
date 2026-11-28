package httpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
)

func waitForAIImageWorker(t *testing.T, s *Server, userID int64) {
	t.Helper()
	// The handler may return before cancellation reaches its worker. The slot
	// is released only after media writes and accounting have finished.
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !s.aiState.acquire(userID) {
		select {
		case <-deadline.C:
			t.Fatal("image worker did not release its slot")
		case <-tick.C:
		}
	}
	s.aiState.release(userID)
}

func TestAIImagesRechecksAuthorityAfterGeneration(t *testing.T) {
	for _, change := range []string{"media-permission", "disabled", "logout", "cancel", "allowed"} {
		t.Run(change, func(t *testing.T) {
			s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "image-session", "image-session@example.test", "Image editor", "editor")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var data bytes.Buffer
			if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 32, 24))); err != nil {
				t.Fatal(err)
			}
			encoded := base64.StdEncoding.EncodeToString(data.Bytes())
			var calls atomic.Int32
			s.aiImages = &aieditor.ImageClient{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				switch change {
				case "media-permission", "disabled":
					input := auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: user.Status, RoleSlugs: []string{"editor"}}
					if change == "media-permission" {
						input.RoleSlugs = []string{"author"}
					} else {
						input.Status = auth.UserStatusSuspended
					}
					if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, input); err != nil {
						t.Error("change image actor:", err)
					}
				case "logout":
					if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Error("revoke image session:", err)
					}
				case "cancel":
					cancel()
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"` + encoded + `"}]}`))}, nil
			})}}
			body := `{"protocol":"openai_images","base_url":"https://images.example.test","api_key":"fixture-key","model":"gpt-image-1","prompt":"Fixture image"}`
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/api/v1/admin/ai/images", strings.NewReader(body))
			request.AddCookie(cookie)
			for key, value := range authenticatedAPIHeaders(actor) {
				request.Header.Set(key, value)
			}
			before := s.media.Snapshot().Count
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			waitForAIImageWorker(t, s, actor.UserID)
			if calls.Load() != 1 {
				t.Fatalf("provider calls=%d, body=%s", calls.Load(), response.Body.String())
			}
			if change == "allowed" {
				if !strings.Contains(response.Body.String(), "event: done") || s.media.Snapshot().Count != before+1 {
					t.Fatalf("authorized editor could not save image: %s", response.Body.String())
				}
				return
			}
			if s.media.Snapshot().Count != before || strings.Contains(response.Body.String(), "event: image") || strings.Contains(response.Body.String(), "event: done") {
				t.Fatalf("revoked/cancelled image request persisted media: delta=%d stream=%s", s.media.Snapshot().Count-before, response.Body.String())
			}
			if change != "cancel" && !strings.Contains(response.Body.String(), "event: error") {
				t.Fatalf("missing terminal error: %s", response.Body.String())
			}
		})
	}
}

func TestAIImagesEnvironmentSessionAndStorageFailure(t *testing.T) {
	for _, change := range []string{"allowed", "logout", "storage-error"} {
		t.Run(change, func(t *testing.T) {
			s, _, _, _, _ := newCommentHTTPTestServer(t)
			sessions := &observedSessionStore{SessionStore: auth.NewMemorySessionStore()}
			const password = "Image environment administrator 2026"
			hash, err := auth.HashPassword(password)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := auth.New(auth.Config{Username: "image-env", PasswordHash: hash, Sessions: sessions})
			if err != nil {
				t.Fatal(err)
			}
			token, actor, err := manager.AuthenticateContext(t.Context(), "image-env", "image-env", password)
			if err != nil {
				t.Fatal(err)
			}
			s.auth = manager
			encoded := base64.StdEncoding.EncodeToString(httpTestPNG(t, 4, 3))
			failure := errors.New("private session storage failure")
			s.aiImages = &aieditor.ImageClient{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
				if change == "logout" {
					if err := manager.RevokeSessionContext(t.Context(), token); err != nil {
						t.Error(err)
					}
				} else if change == "storage-error" {
					sessions.getErr = failure
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"` + encoded + `"}]}`))}, nil
			})}}
			cookie := &http.Cookie{Name: auth.CookieName, Value: token}
			body := `{"protocol":"openai_images","base_url":"https://images.example.test","api_key":"fixture-key","model":"gpt-image-1","prompt":"Fixture image"}`
			response := serveContentAPI(s.routes(), cookie, http.MethodPost, "https://example.test/api/v1/admin/ai/images", body, authenticatedAPIHeaders(actor))
			waitForAIImageWorker(t, s, actor.UserID)
			if change == "allowed" {
				if !strings.Contains(response.Body.String(), "event: done") || s.media.Snapshot().Count != 1 {
					t.Fatalf("environment administrator could not generate image: %s", response.Body.String())
				}
			} else if !strings.Contains(response.Body.String(), "event: error") || s.media.Snapshot().Count != 0 || strings.Contains(response.Body.String(), failure.Error()) {
				t.Fatalf("environment session failure was not private and closed: %s", response.Body.String())
			}
			if change == "storage-error" && !strings.Contains(response.Body.String(), "auth.session_unavailable") {
				t.Fatal("session outage was not distinguished from revocation")
			}
		})
	}
}

func TestAIImagesRejectsRevocationWhileReadingBody(t *testing.T) {
	s, handler, manager, _, cookie, actor := newPersistentAdminTestServer(t)
	var calls atomic.Int32
	s.aiImages = &aieditor.ImageClient{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected provider call")
	})}}
	body := `{"protocol":"openai_images","base_url":"https://images.example.test","api_key":"fixture-key","model":"gpt-image-1","prompt":"Fixture image"}`
	request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/admin/ai/images", strings.NewReader(body))
	request.AddCookie(cookie)
	for key, value := range authenticatedAPIHeaders(actor) {
		request.Header.Set(key, value)
	}
	request.Body = &mediaAuthorityBody{ReadCloser: request.Body, onRead: func() {
		if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
			t.Fatal(err)
		}
	}}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	waitForAIImageWorker(t, s, actor.UserID)
	if calls.Load() != 0 || !strings.Contains(response.Body.String(), "event: error") || s.media.Snapshot().Count != 0 {
		t.Fatalf("revoked request called provider: calls=%d, stream=%s", calls.Load(), response.Body.String())
	}
}
