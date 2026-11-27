package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
)

func TestSocialMessagingEndToEndAndIsolation(t *testing.T) {
	_, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	user, bobCookie, bob := createAndLoginTestUser(t, manager, owner, "message-reader", "private-search-probe@example.test", "收件人", "subscriber")
	_, otherCookie, _ := createAndLoginTestUser(t, manager, owner, "message-other", "other@example.test", "其他成员", "subscriber")
	base := "https://example.test/api/v1/social"
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": owner.CSRFToken}
	bobHeaders := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": bob.CSRFToken}
	session := serveContentAPI(handler, bobCookie, "GET", base+"/session", "", nil)
	if session.Code != 200 || !strings.Contains(session.Body.String(), `"authenticated":true`) || !strings.Contains(session.Body.String(), `"can_admin":false`) {
		t.Fatalf("subscriber session = %d %s", session.Code, session.Body.String())
	}
	for _, secret := range []string{"private-search-probe", "password_hash", bobCookie.Value} {
		if strings.Contains(session.Body.String(), secret) {
			t.Fatal("session leaked identity secrets")
		}
	}
	contacts := serveContentAPI(handler, ownerCookie, "GET", base+"/contacts?q=private-search-probe", "", nil)
	if contacts.Code != 200 || !strings.Contains(contacts.Body.String(), `"items":[]`) {
		t.Fatalf("private email is searchable: %s", contacts.Body.String())
	}
	contacts = serveContentAPI(handler, ownerCookie, "GET", base+"/contacts?q=message-reader", "", nil)
	if contacts.Code != 200 || !strings.Contains(contacts.Body.String(), `"name":"收件人"`) || strings.Contains(contacts.Body.String(), "email") {
		t.Fatalf("public contacts = %s", contacts.Body.String())
	}
	path := fmt.Sprintf("%s/messages/%d", base, user.ID)
	payload := `{"content":"你好 [微笑] <script>alert(1)</script>","nonce":"http-message-nonce-000001"}`
	sent := serveContentAPI(handler, ownerCookie, "POST", path, payload, headers)
	if sent.Code != 201 {
		t.Fatalf("send = %d %s", sent.Code, sent.Body.String())
	}
	var body struct {
		Message socialMessage `json:"message"`
	}
	decodeAPIResponse(t, sent, &body)
	replay := serveContentAPI(handler, ownerCookie, "POST", path, payload, headers)
	var again struct {
		Message socialMessage `json:"message"`
	}
	decodeAPIResponse(t, replay, &again)
	if again.Message.ID != body.Message.ID {
		t.Fatal("retry created another message")
	}
	conflict := serveContentAPI(handler, ownerCookie, "POST", path, strings.Replace(payload, "你好", "不同消息", 1), headers)
	assertAPIError(t, conflict, 409, "messages.conflict")
	inbox := serveContentAPI(handler, bobCookie, "GET", base+"/inbox", "", nil)
	if inbox.Code != 200 || !strings.Contains(inbox.Body.String(), `"unread":1`) || strings.Contains(inbox.Body.String(), "http-message-nonce") {
		t.Fatalf("inbox DTO = %s", inbox.Body.String())
	}
	if inbox.Header().Get("Cache-Control") != "no-store" || inbox.Header().Get("Vary") != "Cookie" {
		t.Fatal("private inbox is cacheable")
	}
	foreign := serveContentAPI(handler, otherCookie, "GET", fmt.Sprintf("%s/messages/%d", base, owner.UserID), "", nil)
	if foreign.Code != 200 || !strings.Contains(foreign.Body.String(), `"items":[]`) {
		t.Fatalf("conversation leaked: %s", foreign.Body.String())
	}
	notices := serveContentAPI(handler, bobCookie, "GET", base+"/notifications?filter=unread", "", nil)
	if notices.Code != 200 || !strings.Contains(notices.Body.String(), `"unread":1`) || strings.Contains(notices.Body.String(), "script") || strings.Contains(notices.Body.String(), "你好") {
		t.Fatalf("notification DTO = %s", notices.Body.String())
	}
	read := serveContentAPI(handler, bobCookie, "POST", fmt.Sprintf("%s/messages/%d/read", base, owner.UserID), fmt.Sprintf(`{"through":%d}`, body.Message.ID), bobHeaders)
	if read.Code != 200 {
		t.Fatalf("read = %d %s", read.Code, read.Body.String())
	}
	poll := serveContentAPI(handler, ownerCookie, "GET", path+fmt.Sprintf("?after=%d", body.Message.ID), "", nil)
	if poll.Code != 200 || !strings.Contains(poll.Body.String(), fmt.Sprintf(`"read_through":%d`, body.Message.ID)) || !strings.Contains(poll.Body.String(), `"items":[]`) {
		t.Fatalf("read-only receipt poll = %s", poll.Body.String())
	}
	clear := serveContentAPI(handler, bobCookie, "POST", base+"/notifications", fmt.Sprintf(`{"action":"clear","through":%d}`, body.Message.ID), bobHeaders)
	if clear.Code != 200 {
		t.Fatalf("clear = %s", clear.Body.String())
	}
	history := serveContentAPI(handler, bobCookie, "GET", fmt.Sprintf("%s/messages/%d", base, owner.UserID), "", nil)
	if !strings.Contains(history.Body.String(), "你好") || strings.Contains(history.Body.String(), "<script>") {
		t.Fatalf("message lost or unescaped: %s", history.Body.String())
	}
	logout := serveContentAPI(handler, bobCookie, "POST", base+"/logout", `{}`, bobHeaders)
	if logout.Code != 200 {
		t.Fatalf("logout = %s", logout.Body.String())
	}
	assertAPIError(t, serveContentAPI(handler, bobCookie, "GET", base+"/inbox", "", nil), 401, "auth.authentication_required")
}

func TestSocialMessagingRejectsUnsafeRequests(t *testing.T) {
	_, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	peer, _, _ := createAndLoginTestUser(t, manager, owner, "message-recipient", "recipient@example.test", "目标成员", "subscriber")
	base := "https://example.test/api/v1/social"
	path := fmt.Sprintf("%s/messages/%d", base, peer.ID)
	valid := `{"content":"消息 [doge]","nonce":"shape-check-nonce-0000"}`
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": owner.CSRFToken}
	for _, test := range []struct {
		name, method, path, body string
		status                   int
		override                 map[string]string
	}{
		{"csrf", "POST", path, valid, 403, map[string]string{"X-CSRF-Token": "wrong"}},
		{"cross site", "POST", path, valid, 403, map[string]string{"Origin": "https://evil.test"}},
		{"content type", "POST", path, valid, 415, map[string]string{"Content-Type": "text/plain"}},
		{"unknown field", "POST", path, `{"content":"消息","nonce":"shape-check-nonce-0000","sender_id":900}`, 400, nil},
		{"duplicate field", "POST", path, `{"content":"一","content":"二","nonce":"shape-check-nonce-0000"}`, 400, nil},
		{"blank message", "POST", path, `{"content":"  ","nonce":"shape-check-nonce-0000"}`, 422, nil},
		{"short nonce", "POST", path, `{"content":"一","nonce":"tiny"}`, 422, nil},
		{"limit", "POST", path, `{"content":"` + strings.Repeat("字", 2001) + `","nonce":"shape-check-nonce-0000"}`, 422, nil},
		{"large body", "POST", path, `{"content":"` + strings.Repeat("a", 17000) + `","nonce":"shape-check-nonce-0000"}`, 413, nil},
		{"spoofed self", "GET", fmt.Sprintf("%s/messages/%d", base, owner.UserID), "", 404, nil},
		{"unknown peer", "GET", base + "/messages/999999", "", 404, nil},
		{"negative cursor", "GET", path + "?before=-1", "", 400, nil},
		{"two directions", "GET", path + "?before=3&after=1", "", 400, nil},
		{"duplicate cursor", "GET", path + "?before=1&before=2", "", 400, nil},
		{"unknown query", "GET", base + "/inbox?user=1", "", 400, nil},
		{"unknown filter", "GET", base + "/notifications?filter=secret", "", 400, nil},
		{"write query", "POST", path + "?user=1", valid, 400, nil},
		{"invalid read", "POST", path + "/read", `{"through":0}`, 422, nil},
		{"invalid notice action", "POST", base + "/notifications", `{"through":1,"action":"delete_messages"}`, 422, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := map[string]string{}
			for k, v := range headers {
				h[k] = v
			}
			for k, v := range test.override {
				h[k] = v
			}
			response := serveContentAPI(handler, cookie, test.method, test.path, test.body, h)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}
	inbox := serveContentAPI(handler, cookie, "GET", base+"/inbox", "", nil)
	if !strings.Contains(inbox.Body.String(), `"items":[]`) {
		t.Fatal("failed writes left private messages")
	}
	assertAPIError(t, serveContentAPI(handler, nil, "POST", path, valid, headers), 401, "auth.authentication_required")
}

func TestSocialLoginEstablishesRealSessionAndRevokesOnLogout(t *testing.T) {
	_, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
	_, _, _ = createAndLoginTestUser(t, manager, owner, "login-reader", "login-reader@example.test", "会话成员", "subscriber")
	payload, _ := json.Marshal(map[string]string{"username": "login-reader", "password": "Account password for login-reader 2026"})
	response := serveContentAPI(handler, nil, "POST", "https://example.test/api/v1/social/login", string(payload), map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"})
	if response.Code != 200 {
		t.Fatalf("login = %d %s", response.Code, response.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range response.Result().Cookies() {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie = %+v", cookie)
	}
	session := serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/social/session", "", nil)
	if !strings.Contains(session.Body.String(), `"authenticated":true`) {
		t.Fatalf("session = %s", session.Body.String())
	}
}
