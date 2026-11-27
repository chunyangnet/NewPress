package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
)

type observedSessionStore struct {
	auth.SessionStore
	getErr, createErr, deleteErr, listErr, readyErr error
	deleteUserErr                                   error
	gets, touches                                   int
	deleteOnce                                      bool
	onDeleteError                                   func()
}

func (s *observedSessionStore) Get(ctx context.Context, id string) (auth.SessionRecord, error) {
	s.gets++
	if s.getErr != nil {
		return auth.SessionRecord{}, s.getErr
	}
	return s.SessionStore.Get(ctx, id)
}
func (s *observedSessionStore) Touch(ctx context.Context, id string, now time.Time, ttl time.Duration) error {
	s.touches++
	return s.SessionStore.Touch(ctx, id, now, ttl)
}
func (s *observedSessionStore) Create(ctx context.Context, r auth.SessionRecord, p auth.SessionPolicy) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.SessionStore.Create(ctx, r, p)
}
func (s *observedSessionStore) Delete(ctx context.Context, id string) error {
	if s.deleteErr != nil {
		err := s.deleteErr
		if s.deleteOnce {
			s.deleteErr = nil
		}
		if s.onDeleteError != nil {
			s.onDeleteError()
		}
		return err
	}
	return s.SessionStore.Delete(ctx, id)
}
func (s *observedSessionStore) DeleteUser(ctx context.Context, userID int64, username, exceptID string, now time.Time, idleTTL time.Duration) (int, error) {
	if s.deleteUserErr != nil {
		return 0, s.deleteUserErr
	}
	return s.SessionStore.DeleteUser(ctx, userID, username, exceptID, now, idleTTL)
}
func (s *observedSessionStore) List(ctx context.Context, now time.Time, ttl time.Duration) ([]auth.SessionRecord, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.SessionStore.List(ctx, now, ttl)
}
func (s *observedSessionStore) Ready(ctx context.Context) error {
	if s.readyErr != nil {
		return s.readyErr
	}
	return s.SessionStore.Ready(ctx)
}

func TestSessionHTTPOutageRecoveryDurableLogoutAndRequestCache(t *testing.T) {
	server, _, _, _, _, _ := newPersistentAdminTestServer(t)
	sessions := &observedSessionStore{SessionStore: auth.NewMemorySessionStore()}
	users, err := auth.NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	const password = "Session HTTP owner password 2026"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := auth.New(auth.Config{Username: "owner", PasswordHash: hash, Users: users, Sessions: sessions})
	if err != nil {
		t.Fatal(err)
	}
	token, principal, err := manager.Authenticate("ip", "owner", password)
	if err != nil {
		t.Fatal(err)
	}
	server.auth = manager
	handler := server.routes()
	cookie := &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}
	response := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/", cookie, nil)
	if response.Code != http.StatusOK || sessions.gets != 1 || sessions.touches != 1 {
		t.Fatalf("request did not share one authentication result: status=%d lookups=%d touches=%d", response.Code, sessions.gets, sessions.touches)
	}
	failure := errors.New("private injected storage error")
	sessions.getErr = failure
	response = serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/session", cookie, nil)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "auth.session_unavailable") ||
		response.Header().Get("Set-Cookie") != "" || response.Header().Get("WWW-Authenticate") != "" ||
		strings.Contains(response.Body.String(), failure.Error()) {
		t.Fatalf("outage was not an explicit, private, cookie-preserving failure: %d %s", response.Code, response.Body.String())
	}
	sessions.getErr = nil
	response = serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/session", cookie, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("valid cookie did not recover after outage: %d", response.Code)
	}
	sessions.listErr = failure
	response = serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/sessions", cookie, nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("inventory outage appeared to be an empty successful list: %d", response.Code)
	}
	sessions.listErr = nil
	sessions.deleteErr = failure
	logout := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "https://example.test/admin/logout/", strings.NewReader(url.Values{"csrf_token": {principal.CSRFToken}}.Encode()))
		r.AddCookie(cookie)
		r.Header.Set("Origin", "https://example.test")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	response = logout()
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("failed durable logout appeared successful: %d", response.Code)
	}
	sessions.deleteErr = nil
	response = logout()
	if response.Code != http.StatusSeeOther || response.Header().Get("Set-Cookie") == "" {
		t.Fatalf("retried logout did not clear the cookie: %d", response.Code)
	}
	if _, ok := manager.ValidateSession(token); ok {
		t.Fatal("successful HTTP logout left the session active")
	}
	oldToken, _, err := manager.Authenticate("replacement", "owner", password)
	if err != nil {
		t.Fatal(err)
	}
	newToken, _, err := manager.Authenticate("replacement", "owner", password)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	replacement := httptest.NewRequest(http.MethodPost, "https://example.test/admin/login/", nil).WithContext(ctx)
	replacement.AddCookie(&http.Cookie{Name: auth.CookieName, Value: oldToken})
	sessions.deleteErr, sessions.deleteOnce, sessions.onDeleteError = failure, true, cancel
	response = httptest.NewRecorder()
	if server.replaceBrowserSession(response, replacement, newToken) || response.Code != http.StatusServiceUnavailable || response.Header().Get("Set-Cookie") != "" {
		t.Fatal("failed browser replacement issued a successful cookie")
	}
	sessions.onDeleteError = nil
	current, err := manager.ResolveSession(t.Context(), oldToken)
	if err != nil {
		t.Fatalf("failed replacement erased the previous login: %v", err)
	}
	if _, err := manager.ResolveSession(t.Context(), newToken); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("cancelled replacement did not clean up undisclosed new login: %v", err)
	}
	target, err := manager.CreateUser(t.Context(), current, auth.UserInput{Username: "revocation-target", Email: "revocation-target@example.test", DisplayName: "Target",
		Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"subscriber"}}, password)
	if err != nil {
		t.Fatal(err)
	}
	target, err = manager.UpdateOAuthBinding(t.Context(), target, "github", secretDigest("revocation failure binding"))
	if err != nil {
		t.Fatal(err)
	}
	sessions.deleteUserErr = failure
	currentCookie := &http.Cookie{Name: auth.CookieName, Value: oldToken}
	remove := url.Values{"csrf_token": {current.CSRFToken}, "action": {"oauth-remove"}, "id": {strconv.FormatInt(target.ID, 10)},
		"version": {strconv.FormatInt(target.Version, 10)}, "provider": {"github"}}
	response = serveUserForm(handler, currentCookie, remove, "application/x-www-form-urlencoded", "https://example.test")
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Set-Cookie") != "" || strings.Contains(response.Body.String(), failure.Error()) {
		t.Fatalf("admin binding removal hid a revocation outage: %d", response.Code)
	}
	target, err = manager.GetUser(t.Context(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	rotate := url.Values{"csrf_token": {current.CSRFToken}, "action": {"password"}, "id": {strconv.FormatInt(target.ID, 10)},
		"version": {strconv.FormatInt(target.Version, 10)}, "username": {target.Username}, "email": {target.Email},
		"display_name": {target.DisplayName}, "status": {target.Status}, "locale": {target.Locale}, "role": {"subscriber"},
		"password": {password}, "password_confirm": {password}}
	response = serveUserForm(handler, currentCookie, rotate, "application/x-www-form-urlencoded", "https://example.test")
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Set-Cookie") != "" || strings.Contains(response.Body.String(), failure.Error()) {
		t.Fatalf("admin password change hid a revocation outage: %d", response.Code)
	}
	sessions.deleteUserErr = nil
	server.SetPluginWebhookStore(&webhookHTTPStore{afterRead: func() { sessions.getErr = failure }})
	response = sharedReviewRequest(handler, currentCookie, "/api/v1/admin/plugin-webhooks")
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), `"items"`) || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("post-read authentication outage released sensitive records: %d", response.Code)
	}
	sessions.getErr = nil
	sessions.createErr = failure
	r := httptest.NewRequest(http.MethodPost, "https://example.test/admin/login/", strings.NewReader(url.Values{"username": {"owner"}, "password": {password}}.Encode()))
	r.Header.Set("Origin", "https://example.test")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, r)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("failed login persistence was not a service failure: %d", response.Code)
	}
	sessions.readyErr = failure
	for path, want := range map[string]int{"/health/live": http.StatusOK, "/health/ready": http.StatusServiceUnavailable} {
		response = serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, cookie, nil)
		if response.Code != want {
			t.Fatalf("%s status=%d, want %d", path, response.Code, want)
		}
	}
}
