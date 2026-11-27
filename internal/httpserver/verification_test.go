package httpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/verification"
)

func enableTestVerification(t *testing.T, s *Server) {
	t.Helper()
	settings := s.verification.Store.Snapshot()
	settings.Enabled = true
	settings.DefaultProvider = "turnstile"
	for _, scene := range verification.Scenes {
		settings.Scenes[scene] = verification.Rule{Enabled: true}
	}
	settings.Providers = []verification.Provider{{ID: "turnstile", SiteKey: "public-fixture-key", Secret: "fixture-private-secret", MinScore: 0.5}}
	if _, err := s.verification.Store.Save(settings); err != nil {
		t.Fatal(err)
	}
	s.verification.HTTP = &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		scene := strings.Split(r.Form.Get("response"), ":")[0]
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"success":true,"hostname":"example.test","action":%q}`, scene)))}, nil
	})}
}
func mintHTTPVerification(t *testing.T, handler http.Handler, scene, suffix string) string {
	t.Helper()
	body := fmt.Sprintf(`{"scene":%q,"proof":{"token":%q}}`, scene, scene+":"+suffix)
	r := serveContentAPI(handler, nil, "POST", "https://example.test/api/v1/verification/verify", body, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"})
	var result struct {
		Token string `json:"token"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &result) != nil || result.Token == "" {
		t.Fatalf("mint failed: %d %s", r.Code, r.Body.String())
	}
	return result.Token
}

func TestVerificationSettingsAuthorizationPersistenceAndPublicPrivacy(t *testing.T) {
	s, handler, manager, _, cookie, principal := newPersistentAdminTestServer(t)
	path := filepath.Join(t.TempDir(), "verification-settings.json")
	store, err := verification.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.SetVerification(store)
	_, reader, readerPrincipal := createAndLoginTestUser(t, manager, principal, "verify-reader", "verify-reader@example.test", "读者", "subscriber")
	settings := store.Snapshot()
	settings.Providers = []verification.Provider{{ID: "turnstile", SiteKey: "public", Secret: "private-captcha-key", MinScore: 0.5}}
	b, _ := json.Marshal(settings)
	for _, c := range []struct {
		cookie       *http.Cookie
		csrf, origin string
	}{{nil, "", "https://example.test"}, {reader, readerPrincipal.CSRFToken, "https://example.test"}, {cookie, "", "https://example.test"}, {cookie, principal.CSRFToken, "https://other.test"}} {
		r := serveContentAPI(handler, c.cookie, "POST", "https://example.test/api/v1/admin/verification/settings", string(b), map[string]string{"Content-Type": "application/json", "Origin": c.origin, "X-CSRF-Token": c.csrf})
		if r.Code != 401 && r.Code != 403 {
			t.Fatal("unauthorized setting write", r.Code)
		}
	}
	r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/verification/settings", string(b), map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken})
	if r.Code != 200 || strings.Contains(r.Body.String(), "private-captcha-key") {
		t.Fatal("failed save or leaked credential", r.Code)
	}
	for _, path := range []string{"/api/v1/admin/verification/settings", "/admin/settings/", "/admin/settings/export/", "/api/v1/verification/config?scene=comment"} {
		r = serveContentAPI(handler, cookie, "GET", "https://example.test"+path, "", nil)
		if r.Code != 200 || strings.Contains(r.Body.String(), "private-captcha-key") {
			t.Fatal("secret leaked", path, r.Code)
		}
	}
	r = serveContentAPI(handler, nil, "GET", "https://example.test/api/v1/verification/config?scene=preview&preview=turnstile", "", nil)
	if r.Code != 401 {
		t.Fatal("public preview permitted", r.Code)
	}
	r = serveContentAPI(handler, reader, "GET", "https://example.test/api/v1/admin/verification/settings", "", nil)
	if r.Code != 403 {
		t.Fatal("reader read settings", r.Code)
	}
	reopened, err := verification.NewStore(path)
	if err != nil || !reopened.Snapshot().Providers[0].HasSecret {
		t.Fatal("credential was not persisted", err)
	}
}

func TestVerificationGuardsRegistrationRecoveryAndBothLoginEntrypoints(t *testing.T) {
	s, handler, _, _, _, _ := newPersistentAdminTestServer(t)
	mailbox := &accessMailbox{}
	s.mailSender = mailbox
	setRegistration(t, s, "open")
	enableTestVerification(t, s)
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"}
	for _, kind := range []string{"register", "recover"} {
		body := fmt.Sprintf(`{"kind":%q,"email":"new-verification@example.test"}`, kind)
		r := serveContentAPI(handler, nil, "POST", "https://example.test/api/v1/social/access/request", body, headers)
		if r.Code != 422 || !strings.Contains(r.Body.String(), "verification.required") {
			t.Fatal("email verification bypass", kind, r.Code, r.Body.String())
		}
	}
	if len(mailbox.messages) != 0 {
		t.Fatal("mail sent before verification")
	}
	login := `{"username":"owner","password":"Persistent owner password 2026"}`
	r := serveContentAPI(handler, nil, "POST", "https://example.test/api/v1/social/login", login, headers)
	if r.Code != 422 {
		t.Fatal("social login bypass", r.Code)
	}
	form := url.Values{"username": {"owner"}, "password": {"Persistent owner password 2026"}}
	req := httptest.NewRequest("POST", "https://example.test/admin/login/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://example.test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 422 {
		t.Fatal("admin login bypass", rec.Code)
	}
	token := mintHTTPVerification(t, handler, "register", "email")
	r = serveContentAPI(handler, nil, "POST", "https://example.test/api/v1/social/access/request", fmt.Sprintf(`{"kind":"register","email":"new-verification@example.test","verification_token":%q}`, token), headers)
	if r.Code != 202 || len(mailbox.messages) != 1 {
		t.Fatal("verified email failed", r.Code, r.Body.String())
	}
	raw, kind := accessMailToken(t, mailbox)
	requestID, _ := auth.RandomCredential()
	signup := registrationBrowser(t, handler)
	complete := map[string]any{"request_id": requestID, "confirm_password": "Verified reader password 2026", "kind": kind, "token": raw, "username": "new-verification", "display_name": "读者", "password": "Verified reader password 2026"}
	b, _ := json.Marshal(complete)
	r = serveContentAPI(handler, signup, "POST", "https://example.test/api/v1/social/access/register", string(b), headers)
	if r.Code != 422 {
		t.Fatal("completion bypass", r.Code)
	}
	complete["verification_token"] = token
	b, _ = json.Marshal(complete)
	r = serveContentAPI(handler, signup, "POST", "https://example.test/api/v1/social/access/register", string(b), headers)
	if r.Code != 422 {
		t.Fatal("email receipt reused at completion")
	}
	complete["verification_token"] = mintHTTPVerification(t, handler, "register", "complete")
	b, _ = json.Marshal(complete)
	r = serveContentAPI(handler, signup, "POST", "https://example.test/api/v1/social/access/register", string(b), headers)
	if r.Code != 200 {
		t.Fatal("completion failed", r.Code, r.Body.String())
	}
	token = mintHTTPVerification(t, handler, "login", "signin")
	r = serveContentAPI(handler, nil, "POST", "https://example.test/api/v1/social/login", strings.TrimSuffix(login, "}")+fmt.Sprintf(`,"verification_token":%q}`, token), headers)
	if r.Code != 200 {
		t.Fatal("verified login failed", r.Code, r.Body.String())
	}
}

func TestVerificationCommentAPIReceiptAndHTMLCannotBypass(t *testing.T) {
	s, handler, _, _, _ := newCommentHTTPTestServer(t)
	enableTestVerification(t, s)
	page := serveContentAPI(handler, nil, "GET", "https://example.test/posts/welcome-to-newpress", "", nil)
	body := publicCommentCreateAPIRequest{FormToken: extractCommentToken(t, page.Body.String()), AuthorName: "验证访客", Email: "verify@example.test", Content: "通过验证的评论"}
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "Idempotency-Key": "verification-comment-key-123"}
	b, _ := json.Marshal(body)
	r := serveContentAPI(handler, nil, "POST", "https://example.test/api/v1/posts/welcome-to-newpress/comments", string(b), headers)
	if r.Code != 422 {
		t.Fatal("API bypass", r.Code, r.Body.String())
	}
	form := url.Values{"form_token": {body.FormToken}, "parent_id": {"0"}, "author_name": {body.AuthorName}, "email": {body.Email}, "content": {body.Content}, "website": {""}}
	r = servePublicCommentForm(handler, "welcome-to-newpress", form, "https://example.test", "198.51.100.10:4000")
	if r.Code != 422 {
		t.Fatal("HTML bypass", r.Code)
	}
	body.VerificationToken = mintHTTPVerification(t, handler, "comment", "first")
	b, _ = json.Marshal(body)
	r = serveContentAPI(handler, nil, "POST", "https://example.test/api/v1/posts/welcome-to-newpress/comments", string(b), headers)
	if r.Code != 202 && r.Code != 201 {
		t.Fatal("verified comment failed", r.Code, r.Body.String())
	}
	body.VerificationToken = ""
	b, _ = json.Marshal(body)
	r = serveContentAPI(handler, nil, "POST", "https://example.test/api/v1/posts/welcome-to-newpress/comments", string(b), headers)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"replayed":true`) {
		t.Fatal("receipt lookup requires new captcha", r.Code, r.Body.String())
	}
	headers["Idempotency-Key"] = "verification-comment-key-456"
	r = serveContentAPI(handler, nil, "POST", "https://example.test/api/v1/posts/welcome-to-newpress/comments", string(b), headers)
	if r.Code != 422 {
		t.Fatal("fresh comment reused receipt", r.Code)
	}
}
