package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

const autosaveAuthorityMarker = "PRIVATE-AUTOSAVE-AUTHORITY"

type autosaveAuthorityRepository struct {
	content.Repository
	afterAutosave func()
	afterTerms    func()
	afterRender   func()
	saves         int
	deletes       int
}

func runAutosaveBoundary(after *func()) {
	if *after != nil {
		change := *after
		*after = nil
		change()
	}
}

func (r *autosaveAuthorityRepository) GetAutosave(ctx context.Context, id int64, owner string) (content.Autosave, error) {
	a, err := r.Repository.GetAutosave(ctx, id, owner)
	runAutosaveBoundary(&r.afterAutosave)
	return a, err
}

func (r *autosaveAuthorityRepository) ListTerms(ctx context.Context, query content.TermQuery) (content.TermList, error) {
	list, err := r.Repository.ListTerms(ctx, query)
	runAutosaveBoundary(&r.afterTerms)
	return list, err
}

func (r *autosaveAuthorityRepository) ResolveSnippets(ctx context.Context, keys []contenthtml.SnippetKey) (map[contenthtml.SnippetKey]string, error) {
	values, err := r.Repository.ResolveSnippets(ctx, keys)
	runAutosaveBoundary(&r.afterRender)
	return values, err
}

func (r *autosaveAuthorityRepository) SaveAutosave(ctx context.Context, a content.Autosave) (content.Autosave, error) {
	r.saves++
	return r.Repository.SaveAutosave(ctx, a)
}

func (r *autosaveAuthorityRepository) DeleteAutosave(ctx context.Context, id int64, owner string) error {
	r.deletes++
	return r.Repository.DeleteAutosave(ctx, id, owner)
}

func autosaveAuthorityFixture(t *testing.T, s *Server, repository content.Repository, p auth.Principal) (content.Post, content.Autosave, string) {
	t.Helper()
	post, err := repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Slug: "autosave-authority", Title: "Original post", ContentSource: "<p>Original post</p>", AuthorID: p.UserID, AuthorName: p.Label(), Actor: p.Username})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := post
	snapshot.Title = autosaveAuthorityMarker
	snapshot.ContentSource = `<p>` + autosaveAuthorityMarker + `</p><aside data-np-snippet="1" data-np-snippet-version="1"></aside>`
	owner, _ := autosaveOwnerKey(p)
	a, err := repository.SaveAutosave(t.Context(), content.Autosave{PostID: post.ID, OwnerKey: owner, SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return post, a, s.autosavePreviewURL(a, owner)
}

func autosaveAuthorityRevoke(t *testing.T, manager *auth.Manager, owner auth.Principal, user auth.User, cookie *http.Cookie, change string) {
	t.Helper()
	switch change {
	case "logout", "logout-with-fallback":
		if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
			t.Error(err)
		}
	case "permission", "suspension":
		role, status := "subscriber", user.Status
		if change == "suspension" {
			role, status = "editor", auth.UserStatusSuspended
		}
		if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: status, RoleSlugs: []string{role}}); err != nil {
			t.Error(err)
		}
	}
}

func TestAutosavePreviewRechecksAfterReadAndRendering(t *testing.T) {
	for _, boundary := range []string{"read", "render"} {
		for _, change := range []string{"logout", "logout-with-fallback", "permission", "suspension", "discard", "replacement", "canonical", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				s, handler, manager, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "autosave-session", "autosave-session@example.test", "Autosave editor", "editor")
				post, a, preview := autosaveAuthorityFixture(t, s, repository, actor)
				changed := false
				after := func() {
					changed = true
					autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
					switch change {
					case "discard":
						if err := repository.DeleteAutosave(t.Context(), post.ID, a.OwnerKey); err != nil {
							t.Fatal(err)
						}
					case "replacement":
						a.Snapshot.Title = "Replacement recovery"
						if _, err := repository.SaveAutosave(t.Context(), a); err != nil {
							t.Fatal(err)
						}
					case "canonical":
						if _, err := repository.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: actor.Username}, content.StatusPrivate); err != nil {
							t.Fatal(err)
						}
					}
				}
				wrapped := &autosaveAuthorityRepository{Repository: repository}
				if boundary == "read" {
					wrapped.afterAutosave = after
				} else {
					wrapped.afterRender = after
				}
				s.repository = wrapped
				r := httptest.NewRequest(http.MethodGet, "https://example.test"+preview, nil)
				r.AddCookie(cookie)
				if change == "logout-with-fallback" {
					r.AddCookie(ownerCookie)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				if !changed {
					t.Fatalf("%s boundary not reached: %d", boundary, response.Code)
				}
				if change == "allowed" {
					if response.Code != 200 || !strings.Contains(response.Body.String(), autosaveAuthorityMarker) {
						t.Fatal("authorized preview lost data", response.Code)
					}
					return
				}
				want := http.StatusSeeOther
				if change == "permission" {
					want = http.StatusForbidden
				} else if change == "discard" || change == "replacement" || change == "canonical" {
					want = http.StatusGone
				}
				if response.Code != want || strings.Contains(response.Body.String(), autosaveAuthorityMarker) {
					t.Errorf("revoked/stale preview exposed recovery: status=%d want=%d bytes=%d", response.Code, want, response.Body.Len())
				}
				if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") || response.Header().Get("Referrer-Policy") != "no-referrer" {
					t.Error("private preview lost response policy")
				}
			})
		}
	}
}

func TestAutosaveStatusRechecksAfterRead(t *testing.T) {
	for _, change := range []string{"logout", "permission", "suspension", "discard", "replacement", "allowed"} {
		t.Run(change, func(t *testing.T) {
			s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "autosave-session", "autosave-session@example.test", "Autosave editor", "editor")
			post, a, _ := autosaveAuthorityFixture(t, s, repository, actor)
			changed := false
			s.repository = &autosaveAuthorityRepository{Repository: repository, afterAutosave: func() {
				changed = true
				autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
				if change == "discard" {
					if err := repository.DeleteAutosave(t.Context(), post.ID, a.OwnerKey); err != nil {
						t.Fatal(err)
					}
				} else if change == "replacement" {
					if _, err := repository.SaveAutosave(t.Context(), a); err != nil {
						t.Fatal(err)
					}
				}
			}}
			response := serveCookieRequest(handler, http.MethodGet, fmt.Sprintf("https://example.test/admin/content/%d/autosave/", post.ID), cookie, nil)
			if !changed {
				t.Fatal("status storage boundary not reached")
			}
			want := 200
			if change == "permission" {
				want = 403
			} else if change == "logout" || change == "suspension" {
				want = 401
			}
			if response.Code != want {
				t.Errorf("revoked status: %d want %d", response.Code, want)
			}
			if change != "allowed" && strings.Contains(response.Body.String(), "preview/?token=") {
				t.Error("revoked or stale status issued a preview token")
			}
		})
	}
}

func TestAutosaveMutationsRecheckAfterBodyAndPreparation(t *testing.T) {
	for _, operation := range []string{"save-body", "discard-body", "save-preparation"} {
		for _, change := range []string{"logout", "permission", "allowed"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "autosave-session", "autosave-session@example.test", "Autosave editor", "editor")
				post, a, _ := autosaveAuthorityFixture(t, s, repository, actor)
				form := autosaveTestForm(actor.CSRFToken, post.Version, "New recovery point", post.Slug, "<p>New recovery</p>")
				if operation == "discard-body" {
					form = url.Values{"csrf_token": {actor.CSRFToken}, "version": {fmt.Sprint(post.Version)}, "action": {"discard"}}
				}
				r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("https://example.test/admin/content/%d/autosave/", post.ID), strings.NewReader(form.Encode()))
				r.AddCookie(cookie)
				r.Header.Set("Origin", "https://example.test")
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				changed := false
				after := func() { changed = true; autosaveAuthorityRevoke(t, manager, owner, user, cookie, change) }
				wrapped := &autosaveAuthorityRepository{Repository: repository}
				if operation == "save-preparation" {
					wrapped.afterTerms = after
				} else {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
				}
				s.repository = wrapped
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				if !changed {
					t.Fatalf("mutation boundary not reached: %d %s", response.Code, response.Body.String())
				}
				if change == "allowed" {
					want, saves, deletes := 200, 1, 0
					if operation == "discard-body" {
						want, saves, deletes = 303, 0, 1
					}
					if response.Code != want || wrapped.saves != saves || wrapped.deletes != deletes {
						t.Fatalf("authorized mutation refused: %d %s", response.Code, response.Body.String())
					}
					return
				}
				want := 403
				if change == "logout" {
					want = 303
				}
				current, err := repository.GetAutosave(t.Context(), post.ID, a.OwnerKey)
				if response.Code != want || wrapped.saves != 0 || wrapped.deletes != 0 || err != nil || !reflect.DeepEqual(current, a) {
					t.Errorf("revoked mutation changed recovery: status=%d saves=%d deletes=%d err=%v", response.Code, wrapped.saves, wrapped.deletes, err)
				}
				if change == "logout" && !strings.HasPrefix(response.Header().Get("Location"), "/admin/login/") {
					t.Error("logout did not return to login")
				}
			})
		}
	}
}

func TestAutosavePreviewRechecksExpirationAfterRead(t *testing.T) {
	s, handler, _, repository, cookie, actor := newPersistentAdminTestServer(t)
	_, _, preview := autosaveAuthorityFixture(t, s, repository, actor)
	u, err := url.Parse(preview)
	if err != nil {
		t.Fatal(err)
	}
	claims, ok := s.verifyPreviewToken(u.Query().Get("token"), time.Now())
	if !ok {
		t.Fatal("invalid preview fixture")
	}
	claims.ExpiresUnix = time.Now().Unix() + 2
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, s.previewSigningKey[:])
	_, _ = mac.Write(payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	u.RawQuery = url.Values{"token": {token}}.Encode()
	changed := false
	s.repository = &autosaveAuthorityRepository{Repository: repository, afterAutosave: func() {
		changed = true
		time.Sleep(time.Until(time.Unix(claims.ExpiresUnix, 0)))
	}}
	response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+u.String(), cookie, nil)
	if !changed || response.Code != 403 || strings.Contains(response.Body.String(), autosaveAuthorityMarker) {
		t.Fatal("expired preview token remained usable", response.Code)
	}
}

func TestAutosavePreviewRevocationDuringStream(t *testing.T) {
	for _, change := range []string{"logout", "permission", "discard", "replacement", "cancel", "allowed"} {
		t.Run(change, func(t *testing.T) {
			s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "autosave-session", "autosave-session@example.test", "Autosave editor", "editor")
			_, a, _ := autosaveAuthorityFixture(t, s, repository, actor)
			const tail = "PRIVATE-AUTOSAVE-STREAM-TAIL"
			a.Snapshot.ContentSource = "<p>" + strings.Repeat("x", 2<<20) + tail + "</p>"
			a, err := repository.SaveAutosave(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			preview := s.autosavePreviewURL(a, a.OwnerKey)
			var changed atomic.Bool
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				handler.ServeHTTP(&storageAfterWrite{ResponseWriter: w, after: func() {
					autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
					switch change {
					case "discard":
						if err := repository.DeleteAutosave(t.Context(), a.PostID, a.OwnerKey); err != nil {
							t.Error(err)
						}
					case "replacement":
						if _, err := repository.SaveAutosave(t.Context(), a); err != nil {
							t.Error(err)
						}
					case "cancel":
						cancel()
					}
					changed.Store(true)
				}}, r.WithContext(ctx))
			}))
			defer local.Close()
			r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, local.URL+preview, nil)
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
			if !changed.Load() || response.StatusCode != 200 || response.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("stream boundary not reached", response.StatusCode, len(got), readErr)
			}
			if change == "allowed" {
				if readErr != nil || !strings.Contains(string(got), tail) {
					t.Fatal("authorized preview was incomplete", len(got), readErr)
				}
				return
			}
			if !errors.Is(readErr, io.ErrUnexpectedEOF) || len(got) == 0 || len(got) > 1<<20 || strings.Contains(string(got), tail) || strings.Contains(string(got), "internal_server_error") {
				t.Fatalf("revoked preview continued: bytes=%d err=%v", len(got), readErr)
			}
		})
	}
}

func TestAutosaveSessionOutagePreservesRecoveryAndCookies(t *testing.T) {
	for _, boundary := range []string{"status", "preview-read", "preview-render", "save"} {
		t.Run(boundary, func(t *testing.T) {
			s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
			users, err := auth.NewFileUserStore("")
			if err != nil {
				t.Fatal(err)
			}
			sessions := &privateReadSessionStore{SessionStore: auth.NewMemorySessionStore()}
			const password = "Autosave outage password 2026"
			hash, err := auth.HashPassword(password)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := auth.NewWithContext(t.Context(), auth.Config{Username: "owner", PasswordHash: hash, Users: users, Sessions: sessions, BootstrapEmail: "owner@example.test"})
			if err != nil {
				t.Fatal(err)
			}
			token, actor, err := manager.AuthenticateContext(t.Context(), "autosave-outage", "owner", password)
			if err != nil {
				t.Fatal(err)
			}
			s.auth = manager
			post, a, preview := autosaveAuthorityFixture(t, s, repository, actor)
			changed := false
			fail := func() { changed = true; sessions.failAt = sessions.lookups.Load() + 1 }
			wrapped := &autosaveAuthorityRepository{Repository: repository}
			method, path, body := http.MethodGet, fmt.Sprintf("/admin/content/%d/autosave/", post.ID), ""
			switch boundary {
			case "preview-read", "preview-render":
				path = preview
			case "save":
				method = http.MethodPost
				body = autosaveTestForm(actor.CSRFToken, post.Version, "Unavailable mutation", post.Slug, "<p>New recovery</p>").Encode()
			}
			if boundary == "preview-render" {
				wrapped.afterRender = fail
			} else if boundary == "save" {
				wrapped.afterTerms = fail
			} else {
				wrapped.afterAutosave = fail
			}
			s.repository = wrapped
			r := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body))
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
			r.Header.Set("Origin", "https://example.test")
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			s.routes().ServeHTTP(response, r)
			if !changed || response.Code != 503 || response.Header().Get("Retry-After") != "5" || response.Header().Get("Set-Cookie") != "" || response.Header().Get("WWW-Authenticate") != "" || strings.Contains(response.Body.String(), "PRIVATE-") {
				t.Errorf("outage exposed recovery or erased session: %d %v", response.Code, response.Header())
			}
			current, err := repository.GetAutosave(t.Context(), post.ID, a.OwnerKey)
			if err != nil || !reflect.DeepEqual(current, a) || wrapped.saves != 0 || wrapped.deletes != 0 {
				t.Error("outage changed recovery", err)
			}
		})
	}
}

func TestAutosavePreviewRechecksCurrentAuthorScope(t *testing.T) {
	for _, own := range []bool{false, true} {
		t.Run(fmt.Sprint("own=", own), func(t *testing.T) {
			s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "autosave-session", "autosave-session@example.test", "Autosave editor", "editor")
			canonicalAuthor := owner
			if own {
				canonicalAuthor = actor
			}
			_, a, _ := autosaveAuthorityFixture(t, s, repository, canonicalAuthor)
			a.OwnerKey, _ = autosaveOwnerKey(actor)
			a, err := repository.SaveAutosave(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			preview := s.autosavePreviewURL(a, a.OwnerKey)
			changed := false
			s.repository = &autosaveAuthorityRepository{Repository: repository, afterRender: func() {
				changed = true
				if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: user.Status, RoleSlugs: []string{"author"}}); err != nil {
					t.Fatal(err)
				}
			}}
			response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+preview, cookie, nil)
			want := http.StatusForbidden
			if own {
				want = http.StatusOK
			}
			if !changed || response.Code != want || strings.Contains(response.Body.String(), autosaveAuthorityMarker) != own {
				t.Fatal("current author scope ignored", response.Code, want)
			}
		})
	}
}
