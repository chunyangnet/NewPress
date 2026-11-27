package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
)

type capturedAccessMail struct{ recipient, subject, body string }
type accessMailbox struct {
	messages []capturedAccessMail
	fail     bool
}

func (m *accessMailbox) Send(_ context.Context, to, subject, body string) error {
	if m.fail {
		return errors.New("relay-password-should-never-leak")
	}
	m.messages = append(m.messages, capturedAccessMail{to, subject, body})
	return nil
}
func accessMailToken(t *testing.T, m *accessMailbox) (string, string) {
	t.Helper()
	if len(m.messages) == 0 {
		t.Fatal("mail was not sent")
	}
	for _, line := range strings.Split(m.messages[len(m.messages)-1].body, "\n") {
		if strings.HasPrefix(line, "https://") {
			u, err := url.Parse(line)
			if err != nil {
				t.Fatal(err)
			}
			if u.Host != "example.test" || u.Path != "/account/access/" || u.RawQuery != "" {
				t.Fatal("unsafe link", line)
			}
			v, _ := url.ParseQuery(u.Fragment)
			return v.Get("token"), v.Get("kind")
		}
	}
	t.Fatal("link missing")
	return "", ""
}
func setRegistration(t *testing.T, s *Server, mode string) {
	t.Helper()
	snap := s.siteSettings.Snapshot()
	settings := snap.Settings
	settings.Accounts = &site.AccountSettings{Registration: mode}
	if _, fields, err := s.siteSettings.Save(snap.Revision, settings); err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
}

func TestAccountAccessHTTPRegistrationRecoveryAndInvitation(t *testing.T) {
	s, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	mailbox := &accessMailbox{}
	s.mailSender = mailbox
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"}
	base := "https://example.test/api/v1/social/access"
	r := serveContentAPI(handler, nil, "POST", base+"/request", `{"kind":"register","email":"new-reader@example.test"}`, headers)
	if r.Code != 403 || len(mailbox.messages) != 0 {
		t.Fatal("default registration was open")
	}
	setRegistration(t, s, "open")
	r = serveContentAPI(handler, nil, "POST", base+"/request", `{"kind":"register","email":"new-reader@example.test"}`, headers)
	if r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	raw, kind := accessMailToken(t, mailbox)
	if strings.Contains(r.Body.String(), raw) || strings.Contains(r.Body.String(), "new-reader") {
		t.Fatal("public response disclosed credentials")
	}
	requestID, _ := auth.RandomCredential()
	signup := registrationBrowser(t, handler)
	complete := fmt.Sprintf(`{"request_id":%q,"kind":%q,"token":%q,"username":"new-reader","display_name":"读者","password":"New reader password 2026","confirm_password":"New reader password 2026"}`, requestID, kind, raw)
	r = serveContentAPI(handler, signup, "POST", base+"/register", complete, headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	users, err := manager.ListUsers(t.Context(), auth.UserQuery{Search: "new-reader"})
	if err != nil || len(users.Items) != 1 {
		t.Fatal(users, err)
	}
	reader := users.Items[0]
	if !reader.HasRole("subscriber") || reader.HasRole("administrator") || reader.EmailVerifiedAt == nil {
		t.Fatal("self registration capabilities")
	}
	login, _, err := manager.Authenticate("reader-client", "new-reader", "New reader password 2026")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: auth.CookieName, Value: login}
	r = serveContentAPI(handler, signup, "POST", base+"/register", complete, headers)
	if r.Code != 200 {
		t.Fatal("registration receipt replay failed", r.Code)
	}
	r = serveContentAPI(handler, nil, "POST", base+"/request", `{"kind":"recover","email":"absent@example.test"}`, headers)
	unknown := r.Body.String()
	if r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	missingToken, missingKind := accessMailToken(t, mailbox)
	r = serveContentAPI(handler, nil, "POST", base+"/complete", fmt.Sprintf(`{"kind":%q,"token":%q,"password":"Unknown reader password 2026"}`, missingKind, missingToken), headers)
	if r.Code != 400 {
		t.Fatal("unknown email created a user")
	}
	// Separate fixture identity avoids the intentional per-email one-minute cooldown.
	u, oldCookie, _ := createAndLoginTestUser(t, manager, owner, "reset-reader", "reset@example.test", "重置读者", "subscriber")
	r = serveContentAPI(handler, nil, "POST", base+"/request", `{"kind":"recover","email":"reset@example.test"}`, headers)
	if r.Code != 202 || r.Body.String() != unknown {
		t.Fatal("recovery account enumeration")
	}
	raw, kind = accessMailToken(t, mailbox)
	r = serveContentAPI(handler, nil, "POST", base+"/complete", fmt.Sprintf(`{"kind":%q,"token":%q,"password":"Recovered reader password 2026"}`, kind, raw), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if _, ok := manager.ValidateSession(oldCookie.Value); ok {
		t.Fatal("old session survived")
	}
	updated, err := manager.GetUser(t.Context(), u.ID)
	if err != nil || updated.EmailVerifiedAt == nil {
		t.Fatal(err)
	}
	if _, ok := manager.ValidateSession(cookie.Value); !ok {
		t.Fatal("another account session was revoked")
	}
	adminHeaders := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": owner.CSRFToken}
	invites := "https://example.test/api/v1/admin/invitations"
	r = serveContentAPI(handler, ownerCookie, "POST", invites, `{"email":"invited@example.test"}`, adminHeaders)
	if r.Code != 201 {
		t.Fatal(r.Code, r.Body.String())
	}
	raw, kind = accessMailToken(t, mailbox)
	r = serveContentAPI(handler, ownerCookie, "GET", invites, "", nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), raw) || !strings.Contains(r.Body.String(), "invited@example.test") {
		t.Fatal("invitation listing", r.Code, r.Body.String())
	}
	var listing struct {
		Items []struct {
			ID string `json:"id"`
		}
	}
	decodeAPIResponse(t, r, &listing)
	r = serveContentAPI(handler, ownerCookie, "POST", invites+"/revoke", fmt.Sprintf(`{"id":%q}`, listing.Items[0].ID), adminHeaders)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	requestID, _ = auth.RandomCredential()
	r = serveContentAPI(handler, signup, "POST", base+"/register", fmt.Sprintf(`{"request_id":%q,"kind":%q,"token":%q,"username":"revoked-reader","password":"Revoked reader password 2026","confirm_password":"Revoked reader password 2026"}`, requestID, kind, raw), headers)
	if r.Code != 400 {
		t.Fatal("revoked invitation redeemed")
	}
}

func TestAccountAccessHTTPProtocolAndPrivateBoundaries(t *testing.T) {
	s, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	s.mailSender = &accessMailbox{}
	setRegistration(t, s, "open")
	_, readerCookie, _ := createAndLoginTestUser(t, manager, owner, "limited-reader", "limited@example.test", "读者", "subscriber")
	base := "https://example.test/api/v1/social/access/request"
	valid := `{"kind":"register","email":"protocol@example.test"}`
	for _, tc := range []struct {
		name, body, origin, ctype string
		status                    int
	}{
		{"origin", valid, "https://attacker.test", "application/json", 403},
		{"missing origin", valid, "", "application/json", 403},
		{"duplicate", `{"kind":"register","kind":"recover","email":"protocol@example.test"}`, "https://example.test", "application/json", 400},
		{"role injection", `{"kind":"register","email":"protocol@example.test","role":"administrator"}`, "https://example.test", "application/json", 400},
		{"content type", valid, "https://example.test", "text/plain", 415},
		{"oversize", `{"kind":"register","email":"` + strings.Repeat("a", 2200) + `"}`, "https://example.test", "application/json", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := serveContentAPI(handler, nil, "POST", base, tc.body, map[string]string{"Origin": tc.origin, "Content-Type": tc.ctype})
			if r.Code != tc.status {
				t.Fatal(r.Code, r.Body.String())
			}
		})
	}
	invites := "https://example.test/api/v1/admin/invitations"
	for _, cookie := range []*http.Cookie{nil, readerCookie} {
		r := serveContentAPI(handler, cookie, "GET", invites, "", nil)
		if r.Code != 401 && r.Code != 403 {
			t.Fatal("invitation list exposed")
		}
	}
	r := serveContentAPI(handler, ownerCookie, "POST", invites, `{"email":"x@example.test"}`, map[string]string{"Origin": "https://example.test", "Content-Type": "application/json"})
	if r.Code != 403 {
		t.Fatal("invitation write missed CSRF")
	}
	for _, path := range []string{"https://example.test/account/access/", "https://example.test/api/v1/social/access"} {
		r = serveContentAPI(handler, nil, "GET", path, "", nil)
		if r.Code != 200 || !strings.Contains(r.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("cache boundary", r.Code, r.Header())
		}
		if strings.Contains(path, "/account/access/") && r.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("confirmation page may send referrers")
		}
	}
	s.mailSender = &accessMailbox{fail: true}
	r = serveContentAPI(handler, nil, "POST", base, valid, map[string]string{"Origin": "https://example.test", "Content-Type": "application/json"})
	if r.Code != 503 || strings.Contains(r.Body.String(), "relay-password") {
		t.Fatal("mail failure leaked", r.Code, r.Body.String())
	}
	s.mailSender = nil
	r = serveContentAPI(handler, nil, "GET", "https://example.test/api/v1/social/access", "", nil)
	var settings map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &settings)
	if settings["email_available"] != false {
		t.Fatal("unconfigured mail advertised as ready")
	}
}
