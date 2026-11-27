package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/plugindispatch"
)

type webhookHTTPStore struct {
	calls     int
	review    plugindispatch.ReviewRequest
	failure   error
	afterRead func()
}

func (s *webhookHTTPStore) List(_ context.Context, q plugindispatch.Query) (plugindispatch.Page, error) {
	s.calls++
	if s.afterRead != nil {
		s.afterRead()
	}
	if s.failure != nil {
		return plugindispatch.Page{}, s.failure
	}
	return plugindispatch.Page{Items: []plugindispatch.Summary{}, Limit: q.Limit, Offset: q.Offset}, nil
}
func (s *webhookHTTPStore) Review(_ context.Context, r plugindispatch.ReviewRequest) (plugindispatch.Summary, error) {
	if !plugindispatch.ValidReview(r) {
		return plugindispatch.Summary{}, plugindispatch.ErrInvalid
	}
	s.review = r
	return plugindispatch.Summary{}, s.failure
}

func TestPluginWebhookHTTPPageIdentityAndLimits(t *testing.T) {
	s, h, c, p := newReviewedTaskHTTPTestServer(t)
	store := &webhookHTTPStore{}
	s.SetPluginWebhookStore(store)
	owner := strconv.FormatInt(p.UserID, 10)
	w := sharedReviewRequest(h, c, "/admin/plugin-webhooks/?plugin=reader")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "data-webhook-review") || !strings.Contains(w.Body.String(), "newpress-plugin-webhooks.js") || !strings.Contains(w.Body.String(), "本站实例发送关闭") || store.calls != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/admin/plugin-webhooks", "/api/v1/admin/plugin-webhook-session"} {
		for _, identity := range []string{"", "0" + owner, "999999999999999999"} {
			if w := sharedReviewRequest(h, c, path, identity); w.Code != 409 {
				t.Fatal("identity", w.Code)
			}
		}
		if w := sharedReviewRequest(h, c, path, owner, owner); w.Code != 409 {
			t.Fatal("duplicate identity", w.Code)
		}
		if w := sharedReviewRequest(h, nil, path); w.Code != 401 {
			t.Fatal("anonymous read", w.Code)
		}
	}
	w = sharedReviewRequest(h, c, "/api/v1/admin/plugin-webhook-session", owner)
	var session struct {
		UserID    string `json:"user_id"`
		CSRF      string `json:"csrf_token"`
		Available bool   `json:"available"`
		Enabled   bool   `json:"enabled"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil || session.UserID != owner || session.CSRF != p.CSRFToken || !session.Available || session.Enabled {
		t.Fatal("session projection", w.Code, err)
	}
	for _, query := range []string{"?state=retry", "?limit=0", "?offset=-1", "?job=bad", "?plugin=reader&plugin=other", "?raw=true"} {
		w = sharedReviewRequest(h, c, "/api/v1/admin/plugin-webhooks"+query, owner)
		if w.Code != 400 && w.Code != 422 {
			t.Fatal("query accepted", query, w.Code)
		}
	}
	for n := 1; n <= 31; n++ {
		w = sharedReviewRequest(h, c, "/api/v1/admin/plugin-webhooks", owner)
		want := 200
		if n == 31 {
			want = 429
		}
		if w.Code != want {
			t.Fatal("rate", n, w.Code)
		}
	}
	if w := sharedReviewRequest(h, c, "/api/v1/admin/plugin-webhook-session", owner); w.Code != 200 {
		t.Fatal("independent session rate", w.Code)
	}
	s.SetPluginWebhookStore(nil)
	w = sharedReviewRequest(h, c, "/admin/plugin-webhooks/")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "当前存储不提供 Webhook") {
		t.Fatal("file mode", w.Code)
	}
}

func TestPluginWebhookHTTPWriteCSRFIdentityAndReviewParameters(t *testing.T) {
	s, h, c, p := newReviewedTaskHTTPTestServer(t)
	store := &webhookHTTPStore{}
	s.SetPluginWebhookStore(store)
	id := strings.Repeat("a", 64)
	url := "https://example.test/api/v1/admin/plugin-webhooks/" + id + "/review"
	body := fmt.Sprintf(`{"expected_user_id":"%d","version":3,"decision":"not_received","reason":"checked remote ID"}`, p.UserID)
	for _, tc := range []struct {
		name, token, origin, body string
		status                    int
	}{
		{"csrf", "", "https://example.test", body, 403}, {"origin", p.CSRFToken, "https://else.test", body, 403},
		{"identity", p.CSRFToken, "https://example.test", strings.Replace(body, fmt.Sprintf(`"%d"`, p.UserID), `"99999"`, 1), 409},
		{"numeric identity", p.CSRFToken, "https://example.test", strings.Replace(body, fmt.Sprintf(`"%d"`, p.UserID), fmt.Sprint(p.UserID), 1), 400},
		{"blank reason", p.CSRFToken, "https://example.test", strings.Replace(body, "checked remote ID", "", 1), 422},
		{"resend", p.CSRFToken, "https://example.test", strings.Replace(body, "not_received", "resend", 1), 422},
		{"unknown", p.CSRFToken, "https://example.test", strings.Replace(body, `"version":3`, `"version":3,"send":true`, 1), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := serveAdminCommentAPI(h, c, tc.token, http.MethodPost, url, tc.body, "", tc.origin)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	w := serveAdminCommentAPI(h, c, p.CSRFToken, http.MethodPost, url, body, "", "https://example.test")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"recorded":true`) || store.review.ActorID != p.UserID || store.review.ID != id || store.review.Version != 3 {
		t.Fatal("review binding", w.Code, store.review)
	}
	store.failure = plugindispatch.ErrConflict
	if w := serveAdminCommentAPI(h, c, p.CSRFToken, http.MethodPost, url, body, "", "https://example.test"); w.Code != 409 {
		t.Fatal("stale version", w.Code)
	}
}

func TestPluginWebhookHTTPRevocationAfterReadAndCorruption(t *testing.T) {
	s, h, c, actor := newReviewedTaskHTTPTestServer(t)
	store := &webhookHTTPStore{failure: plugindispatch.ErrCorrupt}
	s.SetPluginWebhookStore(store)
	w := sharedReviewRequest(h, c, "/api/v1/admin/plugin-webhooks")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "plugins.webhooks_corrupt") || strings.Contains(w.Body.String(), `"items"`) {
		t.Fatal("corruption became empty page", w.Code, w.Body.String())
	}
	password := "Webhook reviewer test password"
	user, err := s.auth.CreateUser(t.Context(), actor, auth.UserInput{Username: "webhook-reviewer", Email: "webhook@example.test", Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"administrator"}}, password)
	if err != nil {
		t.Fatal(err)
	}
	token, p, err := s.auth.AuthenticateContext(t.Context(), "webhook-review", user.Username, password)
	if err != nil {
		t.Fatal(err)
	}
	store.failure = nil
	store.afterRead = func() {
		_, err := s.auth.UpdateUser(t.Context(), actor, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, Locale: user.Locale, Status: user.Status, RoleSlugs: []string{"editor"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	w = sharedReviewRequest(h, &http.Cookie{Name: auth.CookieName, Value: token}, "/api/v1/admin/plugin-webhooks", strconv.FormatInt(p.UserID, 10))
	if w.Code != 403 || strings.Contains(w.Body.String(), `"items"`) {
		t.Fatal("revoked data leaked", w.Code, w.Body.String())
	}
}

func TestPluginWebhookSessionReadBound(t *testing.T) {
	s, h, c, p := newReviewedTaskHTTPTestServer(t)
	s.SetPluginWebhookStore(&webhookHTTPStore{})
	for n := 1; n <= 121; n++ {
		w := sharedReviewRequest(h, c, "/api/v1/admin/plugin-webhook-session", strconv.FormatInt(p.UserID, 10))
		want := 200
		if n == 121 {
			want = 429
		}
		if w.Code != want {
			t.Fatal(n, w.Code)
		}
	}
}
