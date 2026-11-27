package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/messaging"
	"newpress.local/newpress/internal/notification"
	"strings"
	"testing"
	"time"
)

func TestNoticeCenterPaginationPrivacyAndIsolation(t *testing.T) {
	s, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	reader, cookie, principal := createAndLoginTestUser(t, manager, owner, "notice-reader", "notice-reader@example.test", "读者", "subscriber")
	post, err := s.repository.Create(t.Context(), content.PostInput{Title: "私密标题不可泄露", Slug: "notice-private", AuthorID: owner.UserID, Status: content.StatusPrivate})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 1; i <= 73; i++ {
		e := activity.New(activity.Publication, post.ID, post.ID, owner.UserID, int64(i), "publish", now)
		if err = s.notifications.Store.Project(t.Context(), e.Key, []notification.Delivery{{UserID: reader.ID, Kind: notification.Publication, Event: e}}, now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.messages.Send(t.Context(), messaging.Input{SenderID: owner.UserID, RecipientID: reader.ID, Content: "不会显示正文", Nonce: "notice-center-nonce-12345"}); err != nil {
		t.Fatal(err)
	}
	base := "https://example.test/api/v1/social/notice-center"
	cursor := ""
	seen := map[string]bool{}
	var boundary noticeBoundary
	for page := 0; page < 3; page++ {
		suffix := ""
		if cursor != "" {
			suffix = "?before=" + cursor
		}
		r := serveContentAPI(handler, cookie, http.MethodGet, base+suffix, "", nil)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		if strings.Contains(r.Body.String(), post.Title) || strings.Contains(r.Body.String(), post.Slug) || strings.Contains(r.Body.String(), "不会显示正文") || strings.Contains(r.Body.String(), "unsubscribe_key") {
			t.Fatal("private content in projection", r.Body.String())
		}
		var result struct {
			Items   []centerNotice `json:"items"`
			HasMore bool           `json:"has_more"`
			Next    string         `json:"next_cursor"`
			Through noticeBoundary `json:"through"`
			Unread  int            `json:"unread"`
		}
		if err = json.Unmarshal(r.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Unread != 74 {
			t.Fatal(result.Unread)
		}
		for _, n := range result.Items {
			if seen[n.ID] {
				t.Fatal("duplicate cursor entry", n.ID)
			}
			seen[n.ID] = true
		}
		boundary = result.Through
		if !result.HasMore {
			break
		}
		cursor = result.Next
	}
	if len(seen) != 74 {
		t.Fatalf("missing merge entries: %d", len(seen))
	}
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken}
	body := fmt.Sprintf(`{"action":"clear","through":{"message_id":%d,"activity_id":%d}}`, boundary.MessageID, boundary.ActivityID)
	assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, base, body, nil), 403, "auth.csrf_invalid")
	foreign := serveContentAPI(handler, ownerCookie, http.MethodGet, base, "", nil)
	if foreign.Code != 200 || !strings.Contains(foreign.Body.String(), `"items":[]`) {
		t.Fatal("cross-account read", foreign.Body.String())
	}
	r := serveContentAPI(handler, cookie, http.MethodPost, base, body, headers)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	r = serveContentAPI(handler, cookie, http.MethodGet, base, "", nil)
	if !strings.Contains(r.Body.String(), `"items":[]`) || !strings.Contains(r.Body.String(), `"unread":0`) {
		t.Fatal(r.Body.String())
	}
	for _, q := range []string{"?before=0.0.0", "?before=-2.0", "?filter=nope", "?filter=all&filter=all"} {
		if r = serveContentAPI(handler, cookie, http.MethodGet, base+q, "", nil); r.Code < 400 {
			t.Fatal("invalid query accepted", q)
		}
	}
}

func TestNotificationPreferencesAndUnsubscribe(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	ctx := t.Context()
	base := "https://example.test/api/v1/social/notification-preferences"
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken}
	r := serveContentAPI(handler, cookie, "GET", base, "", nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), "unsubscribe_key") || strings.Contains(r.Body.String(), "email_since") {
		t.Fatal(r.Code, r.Body.String())
	}
	body := `{"version":0,"reply":false,"mention":true,"publication":true,"moderation":true,"email":false,"weekly":false}`
	r = serveContentAPI(handler, cookie, "POST", base, body, headers)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	assertAPIError(t, serveContentAPI(handler, cookie, "POST", base, body, headers), 409, "notifications.conflict")
	assertAPIError(t, serveContentAPI(handler, cookie, "POST", base, `{"version":1}`, headers), 422, "notifications.invalid")
	p, _ := s.notifications.Store.Preferences(ctx, principal.UserID)
	p.Email = true
	p.Weekly = true
	p, err := s.notifications.Store.SavePreferences(ctx, p, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	token := notification.UnsubscribeToken(p)
	path := fmt.Sprintf("https://example.test/account/notifications/unsubscribe/?u=%d&t=%s", p.UserID, token)
	r = serveContentAPI(handler, nil, "GET", path, "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "确认退订邮件") {
		t.Fatal(r.Code, r.Body.String())
	}
	current, _ := s.notifications.Store.Preferences(ctx, p.UserID)
	if !current.Email {
		t.Fatal("GET unsubscribed account")
	}
	if r.Header().Get("Referrer-Policy") != "no-referrer" || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unsubscribe URL is cacheable")
	}
	values := url.Values{"u": {fmt.Sprint(p.UserID)}, "t": {token}}
	r = serveContentAPI(handler, nil, "POST", "https://example.test/account/notifications/unsubscribe/", values.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "null"})
	if r.Code != 200 || !strings.Contains(r.Body.String(), "已停止邮件订阅") {
		t.Fatal(r.Code, r.Body.String())
	}
	current, _ = s.notifications.Store.Preferences(ctx, p.UserID)
	if current.Email || current.Weekly {
		t.Fatal("unsubscribe not persisted")
	}
	current.Email = true
	current, err = s.notifications.Store.SavePreferences(ctx, current, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(handler, nil, "GET", path, "", nil)
	if r.Code != 400 {
		t.Fatal("old unsubscribe link accepted", r.Code)
	}
}
