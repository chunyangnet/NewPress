package httpserver

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/navigation"
)

type navigationAuthorityStore struct {
	navigation.Store
	afterSnapshot func()
	replacements  int
}

func (s *navigationAuthorityStore) Snapshot(ctx context.Context) (navigation.Snapshot, error) {
	result, err := s.Store.Snapshot(ctx)
	if s.afterSnapshot != nil {
		change := s.afterSnapshot
		s.afterSnapshot = nil
		change()
	}
	return result, err
}

func (s *navigationAuthorityStore) ReplaceAll(ctx context.Context, mutation navigation.RegistryMutation, inputs []navigation.RegistryMenuInput) (navigation.Snapshot, error) {
	s.replacements++
	return s.Store.ReplaceAll(ctx, mutation, inputs)
}

func navigationAuthorityRequest(t *testing.T, operation string, cookie *http.Cookie, principal auth.Principal, before navigation.Snapshot) *http.Request {
	t.Helper()
	payload := marshalNavigationImportDocument(t, validNavigationImportDocument())
	const endpoint = "https://example.test/admin/tools/navigation-import/"
	if operation != "apply-body" {
		return newNavigationImportPreviewRequest(t, endpoint, cookie, url.Values{
			"csrf_token": {principal.CSRFToken}, "action": {"preview"}, "object_policy": {navigationImportDetachObjects},
		}, "navigation.json", payload, "https://example.test")
	}
	fields := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"apply"}, "object_policy": {navigationImportDetachObjects},
		"expected_revision": {navigation.SnapshotRevision(before)},
		"document":          {base64.RawURLEncoding.EncodeToString(payload)}, "confirm": {"replace"},
	}
	request := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(fields.Encode()))
	request.AddCookie(cookie)
	request.Header.Set("Origin", "https://example.test")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

func TestNavigationImportRechecksOriginalAuthority(t *testing.T) {
	for _, operation := range []string{"preview-body", "apply-body", "preview-snapshot"} {
		for _, change := range []string{"permission", "logout", "logout-with-fallback", "suspension", "allowed"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				s, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "navigation-actor", "navigation-actor@example.test", "Navigation actor", "administrator")
				if _, err := s.navigation.Create(t.Context(), navigation.MenuInput{Name: "Private navigation fixture", Slug: "private-navigation-fixture"}, owner.Username); err != nil {
					t.Fatal(err)
				}
				before, err := s.navigation.Snapshot(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				changed := false
				changeAuthority := func() {
					changed = true
					switch change {
					case "logout", "logout-with-fallback":
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Fatal(err)
						}
					case "permission", "suspension":
						status, role := user.Status, "editor"
						if change == "suspension" {
							status, role = auth.UserStatusSuspended, "administrator"
						}
						if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: status, RoleSlugs: []string{role}}); err != nil {
							t.Fatal(err)
						}
					}
				}
				wrapped := &navigationAuthorityStore{Store: s.navigation}
				s.navigation = wrapped
				request := navigationAuthorityRequest(t, operation, cookie, actor, before)
				if change == "logout-with-fallback" {
					request.AddCookie(ownerCookie)
				}
				if operation == "preview-snapshot" {
					wrapped.afterSnapshot = changeAuthority
				} else {
					request.Body = &mediaAuthorityBody{ReadCloser: request.Body, onRead: changeAuthority}
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				after, err := wrapped.Store.Snapshot(t.Context())
				if err != nil || !changed {
					t.Fatalf("fixture did not reach authorization boundary: changed=%v error=%v", changed, err)
				}
				if change == "allowed" {
					if operation == "apply-body" {
						if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/tools/?notice=navigation-imported" || wrapped.replacements != 1 || after.MenuCount != 2 {
							t.Fatalf("authorized replacement failed: status=%d replacements=%d menus=%d", response.Code, wrapped.replacements, after.MenuCount)
						}
					} else if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "private-navigation-fixture") {
						t.Fatalf("authorized preview failed: %d", response.Code)
					}
					return
				}
				want := http.StatusSeeOther
				if change == "permission" {
					want = http.StatusForbidden
				} else if !strings.HasPrefix(response.Header().Get("Location"), "/admin/login/") {
					t.Error("revoked original session did not redirect to login")
				}
				if response.Code != want || wrapped.replacements != 0 || navigation.SnapshotRevision(before) != navigation.SnapshotRevision(after) || strings.Contains(response.Body.String(), "private-navigation-fixture") {
					t.Fatalf("revoked request reached privileged navigation data: status=%d replacements=%d", response.Code, wrapped.replacements)
				}
			})
		}
	}
}

func TestNavigationImportSessionOutagePreservesCookie(t *testing.T) {
	for _, operation := range []string{"preview-body", "apply-body", "preview-snapshot"} {
		t.Run(operation, func(t *testing.T) {
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			sessions := &observedSessionStore{SessionStore: auth.NewMemorySessionStore()}
			const password = "Navigation outage fixture 2026"
			hash, err := auth.HashPassword(password)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := auth.New(auth.Config{Username: "owner", PasswordHash: hash, Sessions: sessions})
			if err != nil {
				t.Fatal(err)
			}
			token, actor, err := manager.Authenticate("navigation-outage", "owner", password)
			if err != nil {
				t.Fatal(err)
			}
			s.auth = manager
			wrapped := &navigationAuthorityStore{Store: s.navigation}
			s.navigation = wrapped
			before, err := wrapped.Store.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			cookie := &http.Cookie{Name: auth.CookieName, Value: token}
			request := navigationAuthorityRequest(t, operation, cookie, actor, before)
			const privateError = "private navigation session storage details"
			outage := func() { sessions.getErr = errors.New(privateError) }
			if operation == "preview-snapshot" {
				wrapped.afterSnapshot = outage
			} else {
				request.Body = &mediaAuthorityBody{ReadCloser: request.Body, onRead: outage}
			}
			response := httptest.NewRecorder()
			handler := s.routes()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable || wrapped.replacements != 0 || strings.Contains(response.Body.String(), privateError) || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Retry-After") != "5" {
				t.Fatalf("outage did not stop the request and preserve its cookie: status=%d replacements=%d", response.Code, wrapped.replacements)
			}
			sessions.getErr = nil
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, navigationAuthorityRequest(t, operation, cookie, actor, before))
			want := http.StatusOK
			if operation == "apply-body" {
				want = http.StatusSeeOther
			}
			if response.Code != want {
				t.Fatalf("valid session did not recover: %d", response.Code)
			}
		})
	}
}
