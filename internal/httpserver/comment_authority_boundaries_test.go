package httpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
)

func TestCommentAuthoritySessionOutage(t *testing.T) {
	for _, boundary := range []string{"body", "committed", "get", "list", "revisions", "render"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
			_, _, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, nil)
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "comment-outage", "PRIVATE-COMMENT-PARENT")
			c := commentAuthorityFixture(t, s, post.ID)
			changed := false
			after := func() { changed = true; sessions.failAt = sessions.lookups.Load() + 1 }
			store := &commentAuthorityStore{Store: s.comments}
			method, path, body, headers := commentAuthorityMutationInput("api-edit", c, actor)
			switch boundary {
			case "committed":
				store.afterMutation = after
			case "get":
				method, body, store.afterGet = http.MethodGet, "", after
			case "list":
				method, path, body, store.afterList = http.MethodGet, "/api/v1/admin/comments?status=all", "", after
			case "revisions":
				method, path, body, store.afterRevisions = http.MethodGet, path+"/revisions", "", after
			case "render":
				method, path, body = http.MethodGet, "/admin/comments/?status=all", ""
				s.repository = &editorAuthorityRepository{Repository: repository, afterTerms: after}
			}
			s.comments = store
			r := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body))
			r.AddCookie(cookie)
			for key, value := range headers {
				r.Header.Set(key, value)
			}
			if boundary == "body" {
				r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
			}
			w, aborted := commentAuthorityServe(s.routes(), r)
			wantCalls := 0
			if boundary == "committed" {
				wantCalls = 1
			}
			if !changed || aborted || store.mutations != wantCalls || w.Code != 503 || w.Header().Get("Retry-After") != "5" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("WWW-Authenticate") != "" || w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "PRIVATE-") {
				t.Fatal("session outage lost privacy or cookie", changed, aborted, store.mutations, w.Code, w.Header())
			}
			sessions.failAt = 0
			if boundary == "committed" {
				w := contentAuthorityRequest(s.routes(), http.MethodGet, "/api/v1/admin/comments/"+strconv.FormatInt(c.ID, 10), "", cookie, nil, nil)
				if w.Code != 200 || !strings.Contains(w.Body.String(), "Updated comment") || store.mutations != 1 {
					t.Fatal("committed edit lost after recovery", w.Code)
				}
			}
		})
	}
}

func TestCommentAuthorityPreconditionAndConflict(t *testing.T) {
	for _, operation := range []string{"api-delete", "api-restore", "conflict"} {
		for _, validator := range []string{"", `"stale"`} {
			t.Run(operation+"/"+validator, func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, cookie, owner := newPersistentAdminTestServer(t)
				post := createOwnedTestPost(t, repository, owner.UserID, owner.Label(), "comment-version", "PRIVATE-COMMENT-PARENT")
				c := commentAuthorityFixture(t, s, post.ID)
				if operation == "api-delete" {
					var err error
					c, err = s.comments.ChangeStatus(t.Context(), c.ID, comment.Mutation{ExpectedVersion: c.Version}, comment.StatusTrash)
					if err != nil {
						t.Fatal(err)
					}
				}
				store := &commentAuthorityStore{Store: s.comments, afterGet: func() {
					if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Error(err)
					}
				}}
				s.comments = store
				method, path, body, headers := commentAuthorityMutationInput(operation, c, owner)
				headers["If-Match"] = validator
				if operation == "conflict" {
					method = http.MethodPut
					body = `{"version":9999,"author_name":"Other","author_email":"other@example.test","content_source":"Stale"}`
				}
				w := contentAuthorityRequest(handler, method, path, body, cookie, headers, nil)
				if w.Code != 401 || w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "current_") {
					t.Fatal("revoked version revealed", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestCommentAuthoritySameAccountFallback(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			t.Parallel()
			s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "comment-fallback", "comment-fallback@example.test", "Editor", "editor")
			token, _, err := manager.AuthenticateContext(t.Context(), "comment-fallback-two", user.Username, "Account password for "+user.Username+" 2026")
			if err != nil {
				t.Fatal(err)
			}
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "comment-fallback", "PRIVATE-COMMENT-PARENT")
			c := commentAuthorityFixture(t, s, post.ID)
			after := func() {
				if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
					t.Error(err)
				}
			}
			store := &commentAuthorityStore{Store: s.comments}
			s.comments = store
			method, path, body, headers := commentAuthorityMutationInput("api-edit", c, actor)
			if !write {
				method, body, store.afterGet = http.MethodGet, "", after
			}
			w := contentAuthorityRequest(handler, method, path, body, cookie, headers, func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
				if write {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
				}
			})
			if w.Code != 401 || store.mutations != 0 || strings.Contains(w.Body.String(), "PRIVATE-") {
				t.Fatal("same-account cookie took over", w.Code, store.mutations)
			}
			if p, err := manager.ResolveSession(t.Context(), token); err != nil || p.UserID != actor.UserID {
				t.Fatal("fallback not independently valid", err)
			}
		})
	}
}

type commentAuthorityLargeList struct{ comment.Store }

func (s commentAuthorityLargeList) ListAdmin(ctx context.Context, q comment.Query) (comment.List, error) {
	values := make([]comment.Comment, 200)
	for i := range values {
		values[i] = comment.Comment{ID: int64(i + 1), PostID: 1, Version: 1, AuthorName: "Reader", AuthorEmail: "private-stream@example.test", Status: comment.StatusPending, ContentSource: strings.Repeat("x", 12000)}
	}
	values[len(values)-1].ContentSource += "PRIVATE-COMMENT-STREAM-TAIL"
	return comment.List{Items: values, Total: len(values), StatusCounts: map[string]int{comment.StatusPending: len(values)}}, nil
}

func TestCommentAuthorityStream(t *testing.T) {
	for _, path := range []string{"/api/v1/admin/comments?per_page=200", "/admin/comments/"} {
		for _, change := range []string{"logout", "permission", "module", "cancel", "allowed"} {
			t.Run(path+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
				user, cookie, _ := createAndLoginTestUser(t, manager, owner, "comment-stream", "comment-stream@example.test", "Editor", "editor")
				s.comments = commentAuthorityLargeList{s.comments}
				var changed atomic.Bool
				local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx, cancel := context.WithCancel(r.Context())
					defer cancel()
					handler.ServeHTTP(&storageAfterWrite{ResponseWriter: w, after: func() {
						autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
						if change == "module" {
							accountAuthorityModule(t, s, "comments", false)
						}
						if change == "cancel" {
							cancel()
						}
						changed.Store(true)
					}}, r.WithContext(ctx))
				}))
				defer local.Close()
				r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, local.URL+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				r.AddCookie(cookie)
				response, err := local.Client().Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				got, readErr := io.ReadAll(response.Body)
				if !changed.Load() || response.StatusCode != 200 {
					t.Fatal("stream boundary not reached", response.StatusCode, len(got), readErr)
				}
				if change == "allowed" {
					if readErr != nil || !strings.Contains(string(got), "PRIVATE-COMMENT-STREAM-TAIL") {
						t.Fatal("allowed stream incomplete", readErr)
					}
					return
				}
				if !errors.Is(readErr, io.ErrUnexpectedEOF) || len(got) == 0 || len(got) > 1<<20 || strings.Contains(string(got), "PRIVATE-COMMENT-STREAM-TAIL") {
					t.Fatal("revoked stream completed", len(got), readErr)
				}
			})
		}
	}
}

func TestCommentAuthorityEncodingAndCompatibility(t *testing.T) {
	t.Run("encoding", func(t *testing.T) {
		s, _, manager, _, cookie, _ := newPersistentAdminTestServer(t)
		handler := s.sessionState(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			value := contentAuthorityMarshaler{after: func() {
				if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
					t.Error(err)
				}
			}}
			s.writeCommentJSON(w, r, 200, value, func() { w.Header().Set("ETag", `"private"`); w.Header().Set("Location", "/private") })
		}))
		w := contentAuthorityRequest(handler, http.MethodGet, "/api/v1/admin/comments", "", cookie, nil, nil)
		if w.Code != 401 || w.Header().Get("ETag") != "" || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "PRIVATE-") {
			t.Fatal("encoding bypassed check", w.Code, w.Header())
		}
	})
	t.Run("disabled-module", func(t *testing.T) {
		s, handler, _, repository, cookie, owner := newPersistentAdminTestServer(t)
		post := createOwnedTestPost(t, repository, owner.UserID, owner.Label(), "comment-paused", "Parent")
		c := commentAuthorityFixture(t, s, post.ID)
		accountAuthorityModule(t, s, "comments", false)
		method, path, body, headers := commentAuthorityMutationInput("api-edit", c, owner)
		w := contentAuthorityRequest(handler, method, path, body, cookie, headers, nil)
		if w.Code != 404 || !strings.Contains(w.Body.String(), `"module.disabled"`) {
			t.Fatal("disabled module allowed moderation", w.Code, w.Body.String())
		}
	})
	t.Run("local-preview", func(t *testing.T) {
		s, _, _, _, _ := newAuthenticatedContentServer(t)
		manager, err := auth.New(auth.Config{})
		if err != nil {
			t.Fatal(err)
		}
		s.auth = manager
		for _, path := range []string{"/admin/comments/", "/api/v1/admin/comments"} {
			w := contentAuthorityRequest(s.routes(), http.MethodGet, path, "", nil, nil, func(r *http.Request) { r.RemoteAddr = "127.0.0.1:41234" })
			if w.Code != 200 {
				t.Fatal("local preview failed", path, w.Code)
			}
		}
	})
}

func TestCommentAuthorityAfterParentRead(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			t.Parallel()
			s, handler, manager, repository, cookie, owner := newPersistentAdminTestServer(t)
			post := createOwnedTestPost(t, repository, owner.UserID, owner.Label(), "comment-parent-read", "PRIVATE-COMMENT-PARENT")
			c := commentAuthorityFixture(t, s, post.ID)
			changed := false
			s.repository = &editorAuthorityRepository{Repository: repository, afterPost: func() {
				changed = true
				if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
					t.Error(err)
				}
			}}
			method, path, body, headers := commentAuthorityMutationInput("api-edit", c, owner)
			if !write {
				method, body = http.MethodGet, ""
			}
			w := contentAuthorityRequest(handler, method, path, body, cookie, headers, nil)
			if !changed || w.Code != 401 || w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "PRIVATE-") {
				t.Fatal("parent read bypassed output authority", changed, w.Code)
			}
		})
	}
}

func TestCommentAuthorityModerationPermission(t *testing.T) {
	for _, boundary := range []string{"body", "committed", "get", "list", "revisions", "render"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
			manager, users, _, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilityCommentsModerate})
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "comment-moderation-role", "PRIVATE-COMMENT-PARENT")
			c := commentAuthorityFixture(t, s, post.ID)
			changed := false
			after := func() {
				changed = true
				if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: []string{auth.CapabilityAdminAccess}}}); err != nil {
					t.Fatal(err)
				}
			}
			store := &commentAuthorityStore{Store: s.comments}
			s.comments = store
			method, path, body, headers := commentAuthorityMutationInput("api-edit", c, actor)
			switch boundary {
			case "committed":
				store.afterMutation = after
			case "get":
				method, body, store.afterGet = http.MethodGet, "", after
			case "list":
				method, path, body, store.afterList = http.MethodGet, "/api/v1/admin/comments?status=all", "", after
			case "revisions":
				method, path, body, store.afterRevisions = http.MethodGet, path+"/revisions", "", after
			case "render":
				method, path, body = http.MethodGet, "/admin/comments/?status=all", ""
				s.repository = &editorAuthorityRepository{Repository: repository, afterTerms: after}
			}
			w := contentAuthorityRequest(s.routes(), method, path, body, cookie, headers, func(r *http.Request) {
				if boundary == "body" {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
				}
			})
			wantCalls := 0
			if boundary == "committed" {
				wantCalls = 1
			}
			if !changed || w.Code != 403 || store.mutations != wantCalls || w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "PRIVATE-") {
				t.Fatal("moderation permission not refreshed", changed, w.Code, store.mutations)
			}
			current, err := manager.ResolveSession(t.Context(), cookie.Value)
			if err != nil || !current.Can(auth.CapabilityAdminAccess) || current.Can(auth.CapabilityCommentsModerate) {
				t.Fatal("role test did not retain admin access", current, err)
			}
		})
	}
}
