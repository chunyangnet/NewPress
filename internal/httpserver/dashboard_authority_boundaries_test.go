package httpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

func dashboardAuthorityIdentity(t *testing.T, s *Server, capabilities []string) (*auth.Manager, *auth.FileUserStore, *privateReadSessionStore, *http.Cookie, auth.Principal) {
	t.Helper()
	store, err := auth.NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	if capabilities != nil {
		if err = store.EnsureRoles(t.Context(), append(auth.BuiltInRoles(), auth.Role{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: capabilities})); err != nil {
			t.Fatal(err)
		}
	}
	const password = "Dashboard authority password 2026"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	sessions := &privateReadSessionStore{SessionStore: auth.NewMemorySessionStore()}
	manager, err := auth.NewWithContext(t.Context(), auth.Config{Username: "owner", PasswordHash: hash, Users: store, Sessions: sessions, BootstrapEmail: "owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	token, owner, err := manager.AuthenticateContext(t.Context(), "dashboard-authority-owner", "owner", password)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: auth.CookieName, Value: token}
	actor := owner
	if capabilities != nil {
		_, cookie, actor = createAndLoginTestUser(t, manager, owner, "dashboard-role-user", "dashboard-role@example.test", "Dashboard", "dashboard-role")
	}
	s.auth = manager
	return manager, store, sessions, cookie, actor
}

func TestDashboardAuthorityPartialRoles(t *testing.T) {
	for _, capability := range []string{"dashboard-only", "write-only", auth.CapabilityContentRead, auth.CapabilityCommentsModerate, auth.CapabilitySystemRead, auth.CapabilityAppearanceManage, auth.CapabilityPluginsManage, auth.CapabilitySessionsRead} {
		t.Run(capability, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
			caps := []string{auth.CapabilityAdminAccess, auth.CapabilityDashboardRead}
			if capability == "write-only" {
				caps = []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn}
			} else if capability != "dashboard-only" {
				caps = append(caps, capability)
			}
			_, store, _, cookie, actor := dashboardAuthorityIdentity(t, s, caps)
			createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "private-partial", "PRIVATE-PARTIAL-DASHBOARD")
			changed := false
			wrapped := &editorAuthorityRepository{Repository: repository, afterTerms: func() {
				changed = true
				if capability != "dashboard-only" && capability != "write-only" {
					if err := store.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: []string{auth.CapabilityAdminAccess, auth.CapabilityDashboardRead}}}); err != nil {
						t.Error(err)
					}
				}
			}}
			s.repository = wrapped
			path, method, body := "/admin/", http.MethodGet, ""
			headers := map[string]string{}
			want := 403
			if capability == "dashboard-only" {
				want = 200
			}
			if capability == "write-only" {
				want = 422
				path, method, body = "/admin/quick-draft/", http.MethodPost, quickDraftValues(actor.CSRFToken, "", "", "Unsaved own text").Encode()
				headers = map[string]string{"Origin": "https://example.test", "Content-Type": "application/x-www-form-urlencoded"}
			}
			w := contentAuthorityRequest(s.routes(), method, path, body, cookie, headers, nil)
			if !changed || w.Code != want || strings.Contains(w.Body.String(), "PRIVATE-PARTIAL-DASHBOARD") {
				t.Fatal("partial role failed", capability, w.Code, changed)
			}
		})
	}
}

func TestDashboardAuthorityCommentsDisabled(t *testing.T) {
	s, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	s.comments = &accountAuthorityComments{Store: s.comments, afterSnapshot: func() { accountAuthorityModule(t, s, "comments", false) }, skipSnapshots: 1}
	w := contentAuthorityRequest(handler, http.MethodGet, "/admin/", "", cookie, nil, nil)
	if w.Code != 403 {
		t.Fatal("disabled comment queue returned", w.Code)
	}
}

func TestDashboardAuthorityStream(t *testing.T) {
	for _, change := range []string{"logout", "permission", "cancel", "allowed"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "dashboard-stream", "dashboard-stream@example.test", "Editor", "editor")
			const tail = "PRIVATE-DASHBOARD-STREAM-TAIL"
			form := quickDraftValues(actor.CSRFToken, "", "", strings.Repeat("x", content.MaxContentBytes-128)+tail)
			var changed atomic.Bool
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				handler.ServeHTTP(&storageAfterWrite{ResponseWriter: w, after: func() {
					autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
					if change == "cancel" {
						cancel()
					}
					changed.Store(true)
				}}, r.WithContext(ctx))
			}))
			defer local.Close()
			r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, local.URL+"/admin/quick-draft/", strings.NewReader(form.Encode()))
			if err != nil {
				t.Fatal(err)
			}
			r.AddCookie(cookie)
			r.Header.Set("Origin", s.config.PublicURL)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response, err := local.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			got, readErr := io.ReadAll(response.Body)
			if !changed.Load() || response.StatusCode != 422 {
				t.Fatal("stream boundary not reached", response.StatusCode, len(got), readErr)
			}
			if change == "allowed" {
				if readErr != nil || !strings.Contains(string(got), tail) {
					t.Fatal("allowed validation page lost", readErr)
				}
				return
			}
			if !errors.Is(readErr, io.ErrUnexpectedEOF) || len(got) == 0 || len(got) > 1<<20 || strings.Contains(string(got), tail) {
				t.Fatal("revoked dashboard stream completed", len(got), readErr)
			}
		})
	}
}

func TestDashboardAccountAuthoritySessionOutage(t *testing.T) {
	for _, operation := range []string{"dashboard", "quick-body", "quick-conflict", "quick-committed", "own-posts", "own-comments"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
			_, _, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, nil)
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "outage-own-post", "PRIVATE-OUTAGE-DASHBOARD")
			if _, err := s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, UserID: actor.UserID, AuthorName: actor.Label(), AuthorEmail: "outage@example.test", ContentSource: "PRIVATE-OUTAGE-COMMENT", Status: comment.StatusPending}); err != nil {
				t.Fatal(err)
			}
			changed := false
			fail := func() { changed = true; sessions.failAt = sessions.lookups.Load() + 1 }
			wrapped := &dashboardAuthorityRepository{contentAuthorityRepository: &contentAuthorityRepository{editorAuthorityRepository: &editorAuthorityRepository{Repository: repository}}, conflict: operation == "quick-conflict"}
			path, method, body := "/admin/", http.MethodGet, ""
			headers := map[string]string{}
			if strings.HasPrefix(operation, "quick") {
				path, method, body = "/admin/quick-draft/", http.MethodPost, quickDraftValues(actor.CSRFToken, "Outage draft", "", "PRIVATE-OUTAGE-INPUT").Encode()
				headers = map[string]string{"Origin": "https://example.test", "Content-Type": "application/x-www-form-urlencoded"}
			}
			if operation == "dashboard" || operation == "own-posts" {
				wrapped.afterList = fail
			}
			if operation == "quick-conflict" || operation == "quick-committed" {
				wrapped.afterCreate = fail
			}
			if operation == "own-posts" {
				path = "/api/v1/social/own-content?kind=posts"
			}
			if operation == "own-comments" {
				path = "/api/v1/social/own-content?kind=comments"
				s.comments = &accountAuthorityComments{Store: s.comments, afterList: fail}
			}
			s.repository = wrapped
			w := contentAuthorityRequest(s.routes(), method, path, body, cookie, headers, func(r *http.Request) {
				if operation == "quick-body" {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: fail}
				}
			})
			if !changed || w.Code != 503 || w.Header().Get("Retry-After") != "5" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("WWW-Authenticate") != "" || strings.Contains(w.Body.String(), "PRIVATE-") {
				t.Fatal("outage lost privacy or session", operation, w.Code, w.Header(), w.Body.String())
			}
			wantCalls := 0
			if operation == "quick-conflict" || operation == "quick-committed" {
				wantCalls = 1
			}
			if wrapped.creates != wantCalls {
				t.Fatal("outage continued writing", wrapped.creates)
			}
			sessions.failAt = 0
			if operation == "quick-committed" {
				w = contentAuthorityRequest(s.routes(), http.MethodGet, "/admin/", "", cookie, nil, nil)
				if w.Code != 200 || !strings.Contains(w.Body.String(), "Outage draft") || wrapped.creates != 1 {
					t.Fatal("committed draft unavailable after recovery", w.Code)
				}
			}
		})
	}
}

func TestDashboardAuthorityLocalReadOnly(t *testing.T) {
	s, _, _, _, _ := newAuthenticatedContentServer(t)
	manager, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s.auth = manager
	w := contentAuthorityRequest(s.routes(), http.MethodGet, "/admin/", "", nil, nil, func(r *http.Request) { r.RemoteAddr = "127.0.0.1:41234" })
	if w.Code != 200 || !strings.Contains(w.Body.String(), "本机只读预览") {
		t.Fatal("local dashboard changed", w.Code)
	}
}

func TestDashboardAuthorityNarrowedScope(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%v", empty), func(t *testing.T) {
			t.Parallel()
			s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "dashboard-scope", "dashboard-scope@example.test", "Editor", "editor")
			if !empty {
				createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "dashboard-own", "PRIVATE-OWN-DASHBOARD")
			}
			changed := false
			s.repository = &editorAuthorityRepository{Repository: repository, afterTerms: func() {
				changed = true
				contentAuthorityNarrow(t, manager, owner, user)
			}}
			w := contentAuthorityRequest(handler, http.MethodGet, "/admin/", "", cookie, nil, nil)
			if !changed || w.Code != 403 || strings.Contains(w.Body.String(), "PRIVATE-OWN-DASHBOARD") {
				t.Fatal("unscoped dashboard survived narrowed authority", changed, w.Code)
			}
		})
	}
}

func TestDashboardAccountAuthorityCancellationAndSameAccountFallback(t *testing.T) {
	for _, path := range []string{"/admin/", "/api/v1/social/own-content?kind=posts", "/api/v1/social/own-content?kind=comments"} {
		for _, change := range []string{"cancel", "fallback"} {
			t.Run(path+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "same-account-read", "same-account-read@example.test", "Editor", "editor")
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "same-account-read", "PRIVATE-SAME-ACCOUNT-READ")
				if _, err := s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, UserID: actor.UserID, AuthorName: actor.Label(), AuthorEmail: actor.Email, ContentSource: "PRIVATE-SAME-ACCOUNT-COMMENT", Status: comment.StatusPending}); err != nil {
					t.Fatal(err)
				}
				var fallback *http.Cookie
				if change == "fallback" {
					token, _, err := manager.AuthenticateContext(t.Context(), "same-account-read-two", user.Username, "Account password for "+user.Username+" 2026")
					if err != nil {
						t.Fatal(err)
					}
					fallback = &http.Cookie{Name: auth.CookieName, Value: token}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				changed := false
				after := func() {
					changed = true
					if change == "cancel" {
						cancel()
					} else if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Error(err)
					}
				}
				switch path {
				case "/admin/":
					s.repository = &editorAuthorityRepository{Repository: repository, afterTerms: after}
				case "/api/v1/social/own-content?kind=posts":
					s.repository = &contentAuthorityRepository{editorAuthorityRepository: &editorAuthorityRepository{Repository: repository}, afterList: after}
				default:
					s.comments = &accountAuthorityComments{Store: s.comments, afterList: after}
				}
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil).WithContext(ctx)
				r.AddCookie(cookie)
				if fallback != nil {
					r.AddCookie(fallback)
				}
				aborted := false
				func() {
					defer func() {
						if v := recover(); v == http.ErrAbortHandler {
							aborted = true
						} else if v != nil {
							panic(v)
						}
					}()
					handler.ServeHTTP(w, r)
				}()
				if !changed || strings.Contains(w.Body.String(), "PRIVATE-SAME-ACCOUNT") {
					t.Fatal("read boundary not enforced", changed, w.Code)
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancelled read produced a response", aborted, w.Code, w.Body.String())
					}
					return
				}
				want := http.StatusUnauthorized
				if path == "/admin/" {
					want = http.StatusSeeOther
				}
				if aborted || w.Code != want {
					t.Fatal("same-account fallback took over", aborted, w.Code)
				}
				if p, err := manager.ResolveSession(t.Context(), fallback.Value); err != nil || p.UserID != actor.UserID {
					t.Fatal("fallback session was not independently valid", p.UserID, err)
				}
			})
		}
	}
}
