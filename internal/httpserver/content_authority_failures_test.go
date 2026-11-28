package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func TestContentAuthorityCancellation(t *testing.T) {
	for _, boundary := range []string{"body", "list", "read", "find"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			s, handler, repository, cookie, actor := newAuthenticatedContentServer(t)
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "cancel-api", "PRIVATE-CANCEL-API")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wrapped := &contentAuthorityRepository{editorAuthorityRepository: &editorAuthorityRepository{Repository: repository}}
			method, path, body := http.MethodGet, fmt.Sprintf("/api/v1/admin/contents/%d", post.ID), ""
			headers := authenticatedAPIHeaders(actor)
			if boundary == "list" {
				path = "/api/v1/admin/contents"
				wrapped.afterList = cancel
			}
			if boundary == "read" {
				wrapped.afterPost = cancel
			}
			if boundary == "body" || boundary == "find" {
				method, path, body = http.MethodPost, "/api/v1/admin/contents", `{"title":"Created","slug":"cancel-created"}`
			}
			if boundary == "find" {
				wrapped.afterFind = cancel
				headers["Idempotency-Key"] = "cancel-content-key"
			}
			s.repository = wrapped
			r := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body)).WithContext(ctx)
			r.AddCookie(cookie)
			for key, value := range headers {
				r.Header.Set(key, value)
			}
			if boundary == "body" {
				r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: cancel}
			}
			w := httptest.NewRecorder()
			func() {
				defer func() {
					if v := recover(); v != nil && v != http.ErrAbortHandler {
						panic(v)
					}
				}()
				handler.ServeHTTP(w, r)
			}()
			if ctx.Err() == nil || w.Body.Len() != 0 || wrapped.mutations != 0 {
				t.Fatal("cancelled content request completed", w.Code, w.Body.Len(), wrapped.mutations)
			}
		})
	}
}

func TestContentAuthoritySessionOutage(t *testing.T) {
	for _, boundary := range []string{"body", "list", "read", "find", "submit"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
			users, err := auth.NewFileUserStore("")
			if err != nil {
				t.Fatal(err)
			}
			sessions := &privateReadSessionStore{SessionStore: auth.NewMemorySessionStore()}
			const password = "Content API outage password 2026"
			hash, err := auth.HashPassword(password)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := auth.NewWithContext(t.Context(), auth.Config{Username: "owner", PasswordHash: hash, Users: users, Sessions: sessions, BootstrapEmail: "owner@example.test"})
			if err != nil {
				t.Fatal(err)
			}
			token, actor, err := manager.AuthenticateContext(t.Context(), "content-outage", "owner", password)
			if err != nil {
				t.Fatal(err)
			}
			s.auth = manager
			cookie := &http.Cookie{Name: auth.CookieName, Value: token}
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "api-outage", "PRIVATE-API-OUTAGE")
			changed := false
			fail := func() { changed = true; sessions.failAt = sessions.lookups.Load() + 1 }
			wrapped := &contentAuthorityRepository{editorAuthorityRepository: &editorAuthorityRepository{Repository: repository}}
			path, method, body := fmt.Sprintf("/api/v1/admin/contents/%d", post.ID), http.MethodGet, ""
			headers := authenticatedAPIHeaders(actor)
			switch boundary {
			case "list":
				path = "/api/v1/admin/contents"
				wrapped.afterList = fail
			case "read":
				wrapped.afterPost = fail
			case "body", "find", "submit":
				path, method, body = "/api/v1/admin/contents", http.MethodPost, `{"title":"PRIVATE-OUTAGE-CREATED","slug":"outage-created"}`
			}
			if boundary == "find" || boundary == "submit" {
				headers["Idempotency-Key"] = "content-outage-key"
			}
			if boundary == "find" {
				wrapped.afterFind = fail
			}
			if boundary == "submit" {
				wrapped.afterSubmit = fail
			}
			s.repository = wrapped
			w := contentAuthorityRequest(s.routes(), method, path, body, cookie, headers, func(r *http.Request) {
				if boundary == "body" {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: fail}
				}
			})
			if !changed || w.Code != 503 || w.Header().Get("Retry-After") != "5" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("WWW-Authenticate") != "" || w.Header().Get("ETag") != "" || w.Header().Get("Idempotency-Replayed") != "" || strings.Contains(w.Body.String(), "PRIVATE-") {
				t.Fatal("session outage leaked result or cleared cookie", w.Code, w.Header(), w.Body.String())
			}
			want := 0
			if boundary == "submit" {
				want = 1
			}
			if wrapped.mutations != want {
				t.Fatal("mutation crossed failed authority check", wrapped.mutations)
			}
			if boundary == "submit" {
				sessions.failAt = 0
				w = contentAuthorityRequest(s.routes(), method, path, body, cookie, headers, nil)
				if w.Code != 201 || w.Header().Get("Idempotency-Replayed") != "true" || wrapped.mutations != 1 {
					t.Fatal("committed receipt recovery failed", w.Code, wrapped.mutations)
				}
			}
		})
	}
}

type contentAuthorityConflictRepository struct {
	*editorAuthorityRepository
	afterConflict func()
}

func (r *contentAuthorityConflictRepository) Update(context.Context, int64, content.Mutation, content.PostInput) (content.Post, error) {
	r.afterPost = r.afterConflict
	return content.Post{}, content.ErrVersionConflict
}

func TestContentAuthorityConflictReload(t *testing.T) {
	s, handler, repository, cookie, actor := newAuthenticatedContentServer(t)
	post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "conflict-api", "PRIVATE-CONFLICT")
	s.repository = &contentAuthorityConflictRepository{editorAuthorityRepository: &editorAuthorityRepository{Repository: repository}, afterConflict: func() {
		if err := s.auth.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
			t.Error(err)
		}
	}}
	headers := authenticatedAPIHeaders(actor)
	headers["If-Match"] = contentAPIETag(post)
	w := contentAuthorityRequest(handler, http.MethodPatch, fmt.Sprintf("/api/v1/admin/contents/%d", post.ID), `{"title":"Changed"}`, cookie, headers, nil)
	if w.Code != 401 || w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "current_version") {
		t.Fatal("conflict reload leaked version", w.Code, w.Body.String())
	}
}

func TestContentAuthorityObjectScopeAfterRead(t *testing.T) {
	for _, own := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodPatch} {
			t.Run(fmt.Sprintf("%s/own=%v", method, own), func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "api-scope", "api-scope@example.test", "Editor", "editor")
				author := owner
				if own {
					author = actor
				}
				post := createOwnedTestPost(t, repository, author.UserID, author.Label(), "api-scope", "PRIVATE-OBJECT-SCOPE")
				wrapped := &editorAuthorityRepository{Repository: repository, afterPost: func() { contentAuthorityNarrow(t, manager, owner, user) }}
				s.repository = wrapped
				headers := authenticatedAPIHeaders(actor)
				headers["If-Match"] = contentAPIETag(post)
				w := contentAuthorityRequest(handler, method, fmt.Sprintf("/api/v1/admin/contents/%d", post.ID), `{"title":"Changed"}`, cookie, headers, nil)
				want := 403
				if own {
					want = 200
				}
				if w.Code != want || !own && (wrapped.mutations != 0 || strings.Contains(w.Body.String(), "PRIVATE-OBJECT-SCOPE")) {
					t.Fatal("current object scope ignored", w.Code, wrapped.mutations)
				}
			})
		}
	}
}

func TestContentAuthorityMutationRejectsFallbackSession(t *testing.T) {
	for _, sameAccount := range []bool{false, true} {
		t.Run(fmt.Sprintf("same-account=%v", sameAccount), func(t *testing.T) {
			t.Parallel()
			s, handler, manager, repository, fallback, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "api-fallback", "api-fallback@example.test", "Editor", "editor")
			if sameAccount {
				token, _, err := manager.AuthenticateContext(t.Context(), "api-fallback-two", user.Username, "Account password for "+user.Username+" 2026")
				if err != nil {
					t.Fatal(err)
				}
				fallback = &http.Cookie{Name: auth.CookieName, Value: token}
			}
			wrapped := &editorAuthorityRepository{Repository: repository, afterFind: func() { autosaveAuthorityRevoke(t, manager, owner, user, cookie, "logout") }}
			s.repository = wrapped
			headers := authenticatedAPIHeaders(actor)
			headers["Idempotency-Key"] = "content-fallback-key"
			w := contentAuthorityRequest(handler, http.MethodPost, "/api/v1/admin/contents", `{"title":"Created"}`, cookie, headers, func(r *http.Request) { r.AddCookie(fallback) })
			if w.Code != 401 || wrapped.mutations != 0 {
				t.Fatal("fallback took over mutation", w.Code, wrapped.mutations)
			}
		})
	}
}
