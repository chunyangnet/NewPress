package httpserver

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
)

type cookieBudgetStore struct {
	auth.SessionStore
	gets, deletes int
}

func (s *cookieBudgetStore) Get(ctx context.Context, id string) (auth.SessionRecord, error) {
	s.gets++
	return s.SessionStore.Get(ctx, id)
}

func (s *cookieBudgetStore) Delete(ctx context.Context, id string) error {
	s.deletes++
	return s.SessionStore.Delete(ctx, id)
}

func TestSessionCookieWorkIsBoundedBeforeStorage(t *testing.T) {
	const password = "Cookie budget regression password 2026"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	store := &cookieBudgetStore{SessionStore: auth.NewMemorySessionStore()}
	manager, err := auth.New(auth.Config{Username: "owner", PasswordHash: hash, Sessions: store})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := manager.Authenticate("test", "owner", password)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auth: manager}
	for _, name := range []string{"distinct", "identical", "valid first", "valid last", "separate headers"} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/admin/session", nil)
			for i := range 64 {
				value := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(string(rune('A'+i)), 32)))
				if name == "identical" {
					value = strings.Repeat("A", 43)
				}
				if name == "valid first" && i == 0 || name == "valid last" && i == 63 {
					value = token
				}
				cookie := &http.Cookie{Name: auth.CookieName, Value: value}
				if name == "separate headers" {
					r.Header.Add("Cookie", cookie.String())
				} else {
					r.AddCookie(cookie)
				}
			}
			store.gets, store.deletes = 0, 0
			w := httptest.NewRecorder()
			reached := false
			s.sessionState(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })).ServeHTTP(w, r)
			if reached || w.Code != http.StatusBadRequest || store.gets != 0 {
				t.Errorf("cookie flood reached storage/handler: status=%d lookups=%d reached=%t", w.Code, store.gets, reached)
			}
			if w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != "no-store" {
				t.Error("rejected cookie flood changed browser state or allowed caching")
			}
			store.gets = 0
			if _, _, ok := s.currentAdminSession(r); ok || store.gets != 0 {
				t.Errorf("uncached session lookup bypassed cookie budget: lookups=%d", store.gets)
			}
			w = httptest.NewRecorder()
			if s.revokeBrowserSessions(w, r) || w.Code != http.StatusBadRequest || store.deletes != 0 {
				t.Errorf("cookie flood partially revoked sessions: status=%d deletes=%d", w.Code, store.deletes)
			}
		})
	}
	if _, ok := manager.ValidateSession(token); !ok {
		t.Fatal("rejected cookie flood revoked a valid session")
	}
}

func TestSessionCookieBudgetPreservesLegacyFallbackAndDeduplicates(t *testing.T) {
	const password = "Legacy cookie regression password 2026"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	store := &cookieBudgetStore{SessionStore: auth.NewMemorySessionStore()}
	manager, err := auth.New(auth.Config{Username: "owner", PasswordHash: hash, Sessions: store})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := manager.Authenticate("test", "owner", password)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auth: manager}
	r := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/admin/session", nil)
	stale := strings.Repeat("A", 43)
	for _, value := range []string{stale, stale, "stale-legacy-session-token-that-is-invalid", token} {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: value})
	}
	// Unrelated application cookies do not consume the session lookup budget.
	for range 10 {
		r.AddCookie(&http.Cookie{Name: "preference", Value: "light"})
	}
	w := httptest.NewRecorder()
	current, ok := s.freshSessionRequest(w, r)
	if !ok {
		t.Fatalf("legacy cookie fallback rejected: %d", w.Code)
	}
	_, got, ok := s.currentAdminSession(current)
	if !ok || got != token || store.gets != 2 {
		t.Fatalf("legacy fallback failed or repeated storage work: valid=%t lookups=%d", ok, store.gets)
	}
	if !s.revokeBrowserSessions(w, r) || store.deletes != 2 {
		t.Fatalf("revocation repeated storage work: deletes=%d", store.deletes)
	}
	if _, ok := manager.ValidateSession(token); ok {
		t.Fatal("bounded logout left the active session valid")
	}
}
