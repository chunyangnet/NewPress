package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

type editorAuthorityRepository struct {
	content.Repository
	afterPost, afterFind, afterAutosave, afterTerms func()
	afterSubmit                                     func()
	mutations                                       int
}

func (r *editorAuthorityRepository) GetByID(ctx context.Context, id int64) (content.Post, error) {
	p, err := r.Repository.GetByID(ctx, id)
	runAutosaveBoundary(&r.afterPost)
	return p, err
}

func (r *editorAuthorityRepository) FindSubmission(ctx context.Context, key, fingerprint string) (content.SubmissionResult, error) {
	result, err := r.Repository.FindSubmission(ctx, key, fingerprint)
	runAutosaveBoundary(&r.afterFind)
	return result, err
}

func (r *editorAuthorityRepository) GetAutosave(ctx context.Context, id int64, owner string) (content.Autosave, error) {
	a, err := r.Repository.GetAutosave(ctx, id, owner)
	runAutosaveBoundary(&r.afterAutosave)
	return a, err
}

func (r *editorAuthorityRepository) ListTerms(ctx context.Context, query content.TermQuery) (content.TermList, error) {
	list, err := r.Repository.ListTerms(ctx, query)
	runAutosaveBoundary(&r.afterTerms)
	return list, err
}

func (r *editorAuthorityRepository) Create(ctx context.Context, input content.PostInput) (content.Post, error) {
	r.mutations++
	return r.Repository.Create(ctx, input)
}

func (r *editorAuthorityRepository) Update(ctx context.Context, id int64, m content.Mutation, input content.PostInput) (content.Post, error) {
	r.mutations++
	return r.Repository.Update(ctx, id, m, input)
}

func (r *editorAuthorityRepository) ChangeStatus(ctx context.Context, id int64, m content.Mutation, status string) (content.Post, error) {
	r.mutations++
	return r.Repository.ChangeStatus(ctx, id, m, status)
}

func (r *editorAuthorityRepository) RestoreRevision(ctx context.Context, id, revision int64, m content.Mutation) (content.Post, error) {
	r.mutations++
	return r.Repository.RestoreRevision(ctx, id, revision, m)
}

func (r *editorAuthorityRepository) Submit(ctx context.Context, submission content.Submission) (content.SubmissionResult, error) {
	r.mutations++
	result, err := r.Repository.Submit(ctx, submission)
	runAutosaveBoundary(&r.afterSubmit)
	return result, err
}

func editorAuthorityForm(p auth.Principal, post content.Post) url.Values {
	return url.Values{"csrf_token": {p.CSRFToken}, "action": {"save"}, "version": {fmt.Sprint(post.Version)},
		"type": {"post"}, "status": {"draft"}, "title": {"Updated editorial content"}, "slug": {post.Slug},
		"content_source": {"<p>Updated editorial content</p>"}, "comment_status": {"open"},
		"published_timezone": {"Asia/Shanghai"}, "published_source": {"canonical"}}
}

func TestEditorAuthorityMutationBoundaries(t *testing.T) {
	for _, operation := range []string{"create-body", "save-body", "save-read", "save-parse", "trash-read", "restore-read", "publish-read", "restore_revision-read", "save-find"} {
		for _, protocol := range []bool{false, true} {
			if operation == "save-find" && !protocol {
				continue
			}
			for _, change := range []string{"logout", "logout-with-fallback", "permission", "suspension", "allowed"} {
				t.Run(fmt.Sprintf("%s/editor=%v/%s", operation, protocol, change), func(t *testing.T) {
					t.Parallel()
					s, handler, manager, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
					user, cookie, actor := createAndLoginTestUser(t, manager, owner, "editor-boundary", "editor-boundary@example.test", "Editor", "editor")
					post, _, _ := autosaveAuthorityFixture(t, s, repository, actor)
					form := editorAuthorityForm(actor, post)
					action, boundary, _ := strings.Cut(operation, "-")
					path := fmt.Sprintf("/admin/content/%d/edit/", post.ID)
					if action == "create" {
						path = "/admin/content/new/"
						form.Set("slug", "new-editorial-content")
						form.Del("version")
					} else {
						form.Set("action", action)
					}
					if action == "restore_revision" {
						revisions, err := repository.ListRevisions(t.Context(), post.ID, 10)
						if err != nil || len(revisions) == 0 {
							t.Fatal("missing fixture revision", err)
						}
						form.Set("action", fmt.Sprintf("restore_revision-%d", revisions[0].ID))
					}
					if boundary == "parse" {
						form.Set("published_source", "autosave")
					}
					changed := false
					after := func() { changed = true; autosaveAuthorityRevoke(t, manager, owner, user, cookie, change) }
					wrapped := &editorAuthorityRepository{Repository: repository}
					switch boundary {
					case "read":
						wrapped.afterPost = after
					case "parse":
						wrapped.afterAutosave = after
					case "find":
						wrapped.afterFind = after
					}
					s.repository = wrapped
					r := httptest.NewRequest(http.MethodPost, "https://example.test"+path, strings.NewReader(form.Encode()))
					r.AddCookie(cookie)
					if change == "logout-with-fallback" {
						r.AddCookie(ownerCookie)
					}
					r.Header.Set("Origin", "https://example.test")
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					if protocol {
						r.Header.Set("X-NewPress-Editor", "1")
						r.Header.Set("Idempotency-Key", "editor-authority-request")
					}
					if boundary == "body" {
						r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
					}
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, r)
					if !changed {
						t.Fatalf("boundary not reached: %d %s", w.Code, w.Body.String())
					}
					current, err := repository.GetByID(t.Context(), post.ID)
					if err != nil {
						t.Fatal(err)
					}
					if change == "allowed" {
						want := 303
						if protocol {
							want = 200
						}
						if w.Code != want || wrapped.mutations != 1 {
							t.Fatalf("allowed mutation failed: %d calls=%d %s", w.Code, wrapped.mutations, w.Body.String())
						}
						return
					}
					want := 303
					if change == "permission" {
						want = 403
					}
					if w.Code != want || wrapped.mutations != 0 || current.Version != post.Version || strings.Contains(w.Body.String(), `"saved":true`) {
						t.Errorf("revoked mutation committed: status=%d want=%d mutations=%d version=%d", w.Code, want, wrapped.mutations, current.Version)
					}
				})
			}
		}
	}
}

func TestEditorAuthorityRecoveryBoundaries(t *testing.T) {
	for _, boundary := range []string{"read", "terms"} {
		for _, change := range []string{"logout", "logout-with-fallback", "permission", "suspension", "discard", "replacement", "canonical", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "editor-recovery", "editor-recovery@example.test", "Editor", "editor")
				post, a, _ := autosaveAuthorityFixture(t, s, repository, actor)
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
						if _, err := repository.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusPrivate); err != nil {
							t.Fatal(err)
						}
					}
				}
				wrapped := &editorAuthorityRepository{Repository: repository}
				if boundary == "read" {
					wrapped.afterAutosave = after
				} else {
					wrapped.afterTerms = after
				}
				s.repository = wrapped
				r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("https://example.test/admin/content/%d/edit/?recovery=autosave", post.ID), nil)
				r.AddCookie(cookie)
				if change == "logout-with-fallback" {
					r.AddCookie(ownerCookie)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if !changed {
					t.Fatal("recovery boundary not reached", w.Code)
				}
				if change == "allowed" {
					if w.Code != 200 || !strings.Contains(w.Body.String(), autosaveAuthorityMarker) {
						t.Fatal("valid recovery lost", w.Code)
					}
					return
				}
				want := 303
				if change == "permission" {
					want = 403
				} else if change == "discard" || change == "replacement" || change == "canonical" {
					want = 410
				}
				if w.Code != want || strings.Contains(w.Body.String(), autosaveAuthorityMarker) {
					t.Errorf("revoked/stale recovery exposed: status=%d want=%d", w.Code, want)
				}
			})
		}
	}
}

func TestEditorAuthorityReplayRechecksAfterLookup(t *testing.T) {
	for _, operation := range []string{"create", "save"} {
		for _, change := range []string{"logout", "permission", "allowed"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "editor-replay", "editor-replay@example.test", "Editor", "editor")
				post, _, _ := autosaveAuthorityFixture(t, s, repository, actor)
				path := fmt.Sprintf("/admin/content/%d/edit/", post.ID)
				form := editorAuthorityForm(actor, post)
				if operation == "create" {
					path = "/admin/content/new/"
					form.Set("slug", "editorial-replay")
					form.Del("version")
				}
				first := editorSubmit(handler, cookie, path, "editorial-replay-request", form, nil)
				if first.Code != 200 {
					t.Fatal(first.Code, first.Body.String())
				}
				changed := false
				wrapped := &editorAuthorityRepository{Repository: repository, afterFind: func() { changed = true; autosaveAuthorityRevoke(t, manager, owner, user, cookie, change) }}
				s.repository = wrapped
				w := editorSubmit(handler, cookie, path, "editorial-replay-request", form, nil)
				want := 200
				if change == "logout" {
					want = 303
				} else if change == "permission" {
					want = 403
				}
				if !changed || w.Code != want || wrapped.mutations != 0 || (change != "allowed" && strings.Contains(w.Body.String(), `"saved":true`)) {
					t.Errorf("revoked receipt returned: %d want=%d %s", w.Code, want, w.Body.String())
				}
			})
		}
	}
}

func TestEditorAuthorityCanonicalAndErrorPages(t *testing.T) {
	for _, page := range []string{"canonical", "new", "validation", "conflict"} {
		for _, boundary := range []string{"terms", "render"} {
			if page == "new" && boundary == "render" {
				continue
			}
			for _, change := range []string{"logout", "permission", "allowed"} {
				t.Run(page+"/"+boundary+"/"+change, func(t *testing.T) {
					t.Parallel()
					s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
					user, cookie, actor := createAndLoginTestUser(t, manager, owner, "editor-page", "editor-page@example.test", "Editor", "editor")
					post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "editor-private", "PRIVATE-EDITOR-CANONICAL")
					changed := false
					after := func() { changed = true; autosaveAuthorityRevoke(t, manager, owner, user, cookie, change) }
					wrapped := &editorAuthorityRepository{Repository: repository}
					if boundary == "terms" {
						wrapped.afterTerms = after
					} else {
						// The next object read occurs inside renderChecked's feature status preparation.
						wrapped.afterTerms = func() { wrapped.afterPost = after }
					}
					s.repository = wrapped
					path := fmt.Sprintf("/admin/content/%d/edit/", post.ID)
					var w *httptest.ResponseRecorder
					want := 200
					if page == "validation" || page == "conflict" {
						form := editorAuthorityForm(actor, post)
						if page == "validation" {
							form.Set("title", "")
							want = 422
						} else {
							form.Set("version", "100")
							want = 409
						}
						w = serveContentForm(handler, cookie, "https://example.test"+path, "https://example.test", form)
					} else {
						if page == "new" {
							path = "/admin/content/new/"
						}
						w = serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, cookie, nil)
					}
					if change == "logout" {
						want = 303
					} else if change == "permission" {
						want = 403
					}
					if !changed || w.Code != want || (change != "allowed" && strings.Contains(w.Body.String(), "PRIVATE-EDITOR-CANONICAL")) {
						t.Errorf("private editor page exposed: %d want=%d changed=%v", w.Code, want, changed)
					}
				})
			}
		}
	}
}

func TestEditorAuthorityCurrentAuthorScope(t *testing.T) {
	for _, operation := range []string{"read", "recovery", "save", "editor-save", "bulk"} {
		for _, own := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/own=%v", operation, own), func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "editor-scope", "editor-scope@example.test", "Editor", "editor")
				author := owner
				if own {
					author = actor
				}
				post := createOwnedTestPost(t, repository, author.UserID, author.Label(), "scope-editor", "PRIVATE-EDITOR-SCOPE")
				ownerKey, _ := autosaveOwnerKey(actor)
				_, err := repository.SaveAutosave(t.Context(), content.Autosave{PostID: post.ID, OwnerKey: ownerKey, SourceVersion: post.Version, Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				changed := false
				after := func() {
					changed = true
					_, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Status: user.Status, RoleSlugs: []string{"author"}})
					if err != nil {
						t.Fatal(err)
					}
				}
				wrapped := &editorAuthorityRepository{Repository: repository}
				path := fmt.Sprintf("/admin/content/%d/edit/", post.ID)
				method := http.MethodPost
				form := editorAuthorityForm(actor, post)
				switch operation {
				case "read", "recovery":
					method = http.MethodGet
					wrapped.afterTerms = after
					if operation == "recovery" {
						path += "?recovery=autosave"
					}
				case "save", "editor-save":
					form.Set("published_source", "autosave")
					wrapped.afterAutosave = after
				case "bulk":
					path = "/admin/content/bulk/"
					form = url.Values{"csrf_token": {actor.CSRFToken}, "action": {"publish"}, "selection": {fmt.Sprintf("%d:%d", post.ID, post.Version)}, "return_type": {""}, "return_status": {""}, "return_q": {""}, "return_page": {"1"}}
				}
				s.repository = wrapped
				r := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(form.Encode()))
				r.AddCookie(cookie)
				r.Header.Set("Origin", "https://example.test")
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if operation == "editor-save" {
					r.Header.Set("X-NewPress-Editor", "1")
					r.Header.Set("Idempotency-Key", "scope-submission")
				}
				if operation == "bulk" {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				want := 403
				if own {
					want = 303
					if method == http.MethodGet || operation == "editor-save" {
						want = 200
					}
				}
				if !changed || w.Code != want {
					t.Errorf("current scope ignored: status=%d want=%d %s", w.Code, want, w.Body.String())
				}
				current, err := repository.GetByID(t.Context(), post.ID)
				if err != nil || (!own && current.Version != post.Version) {
					t.Fatal("foreign post changed", err)
				}
				if !own && strings.Contains(w.Body.String(), "PRIVATE-EDITOR-SCOPE") {
					t.Error("foreign post exposed")
				}
			})
		}
	}
}

func TestEditorAuthorityRevocationDuringStream(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		for _, change := range []string{"logout", "permission", "replacement", "cancel", "allowed"} {
			if change == "replacement" && !recovery {
				continue
			}
			t.Run(fmt.Sprintf("recovery=%v/%s", recovery, change), func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "editor-stream", "editor-stream@example.test", "Editor", "editor")
				post, a, _ := autosaveAuthorityFixture(t, s, repository, actor)
				const tail = "PRIVATE-EDITOR-STREAM-TAIL"
				body := "<p>" + strings.Repeat("x", content.MaxContentBytes-256) + tail + "</p>"
				path := fmt.Sprintf("/admin/content/%d/edit/", post.ID)
				if recovery {
					path += "?recovery=autosave"
					a.Snapshot.ContentSource = body
					if _, err := repository.SaveAutosave(t.Context(), a); err != nil {
						t.Fatal(err)
					}
				} else {
					input := inputFromPost(post)
					input.ContentSource = body
					if _, err := repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, input); err != nil {
						t.Fatal(err)
					}
				}
				var changed atomic.Bool
				local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx, cancel := context.WithCancel(r.Context())
					defer cancel()
					handler.ServeHTTP(&storageAfterWrite{ResponseWriter: w, after: func() {
						autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
						if change == "replacement" {
							if _, err := repository.SaveAutosave(t.Context(), a); err != nil {
								t.Error(err)
							}
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
				if !changed.Load() || response.StatusCode != 200 || !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
					t.Fatal("stream boundary not reached", response.StatusCode, readErr)
				}
				if change == "allowed" {
					if readErr != nil || !strings.Contains(string(got), tail) {
						t.Fatal("valid editor output lost", readErr)
					}
					return
				}
				if !errors.Is(readErr, io.ErrUnexpectedEOF) || len(got) == 0 || len(got) > 1<<20 || strings.Contains(string(got), tail) || strings.Contains(string(got), "internal_server_error") {
					t.Errorf("revoked editor stream completed: bytes=%d err=%v", len(got), readErr)
				}
			})
		}
	}
}

func TestEditorAuthoritySessionOutagePreservesCookie(t *testing.T) {
	for _, boundary := range []string{"body", "canonical", "recovery", "render", "lookup", "submit"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
			users, err := auth.NewFileUserStore("")
			if err != nil {
				t.Fatal(err)
			}
			sessions := &privateReadSessionStore{SessionStore: auth.NewMemorySessionStore()}
			const password = "Editorial outage password 2026"
			hash, err := auth.HashPassword(password)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := auth.NewWithContext(t.Context(), auth.Config{Username: "owner", PasswordHash: hash, Users: users, Sessions: sessions, BootstrapEmail: "owner@example.test"})
			if err != nil {
				t.Fatal(err)
			}
			token, actor, err := manager.AuthenticateContext(t.Context(), "editorial-outage", "owner", password)
			if err != nil {
				t.Fatal(err)
			}
			s.auth = manager
			post, _, _ := autosaveAuthorityFixture(t, s, repository, actor)
			changed := false
			fail := func() { changed = true; sessions.failAt = sessions.lookups.Load() + 1 }
			wrapped := &editorAuthorityRepository{Repository: repository}
			path := fmt.Sprintf("/admin/content/%d/edit/", post.ID)
			method, body := http.MethodGet, ""
			switch boundary {
			case "body", "lookup", "submit":
				method = http.MethodPost
				body = editorAuthorityForm(actor, post).Encode()
			case "recovery":
				path += "?recovery=autosave"
				wrapped.afterAutosave = fail
			case "canonical":
				wrapped.afterPost = fail
			case "render":
				wrapped.afterTerms = func() { wrapped.afterPost = fail }
			}
			if boundary == "lookup" {
				wrapped.afterFind = fail
			}
			if boundary == "submit" {
				wrapped.afterSubmit = fail
			}
			s.repository = wrapped
			r := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body))
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
			r.Header.Set("Origin", "https://example.test")
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if boundary == "lookup" || boundary == "submit" {
				r.Header.Set("X-NewPress-Editor", "1")
				r.Header.Set("Idempotency-Key", "editor-outage")
			}
			if boundary == "body" {
				r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: fail}
			}
			w := httptest.NewRecorder()
			s.routes().ServeHTTP(w, r)
			if !changed || w.Code != 503 || w.Header().Get("Retry-After") != "5" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("WWW-Authenticate") != "" || strings.Contains(w.Body.String(), "PRIVATE-") || strings.Contains(w.Body.String(), `"saved":true`) {
				t.Errorf("outage leaked data or erased session: status=%d headers=%v", w.Code, w.Header())
			}
			current, err := repository.GetByID(t.Context(), post.ID)
			wantVersion := post.Version
			if boundary == "submit" {
				wantVersion++
			}
			if err != nil || current.Version != wantVersion {
				t.Fatal("unexpected mutation during outage", current.Version, err)
			}
			if boundary == "submit" {
				// A committed write is not rolled back. The original key confirms it
				// once storage recovers, without duplicating the content mutation.
				sessions.failAt = 0
				w = editorSubmit(s.routes(), &http.Cookie{Name: auth.CookieName, Value: token}, path, "editor-outage", editorAuthorityForm(actor, post), nil)
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) || wrapped.mutations != 1 {
					t.Fatal("committed outcome could not be recovered", w.Code, wrapped.mutations)
				}
			}
		})
	}
}

func TestEditorAuthorityCancellationBeforeOutputOrMutation(t *testing.T) {
	for _, boundary := range []string{"body", "parse", "read", "terms"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			s, handler, repository, cookie, actor := newAuthenticatedContentServer(t)
			post, _, _ := autosaveAuthorityFixture(t, s, repository, actor)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			changed := false
			after := func() { changed = true; cancel() }
			wrapped := &editorAuthorityRepository{Repository: repository}
			method, body := http.MethodGet, ""
			form := editorAuthorityForm(actor, post)
			if boundary == "parse" {
				form.Set("published_source", "autosave")
				wrapped.afterAutosave = after
			}
			if boundary == "body" || boundary == "parse" {
				method, body = http.MethodPost, form.Encode()
			}
			if boundary == "read" {
				wrapped.afterPost = after
			}
			if boundary == "terms" {
				wrapped.afterTerms = after
			}
			s.repository = wrapped
			r := httptest.NewRequest(method, fmt.Sprintf("https://example.test/admin/content/%d/edit/", post.ID), strings.NewReader(body)).WithContext(ctx)
			r.AddCookie(cookie)
			r.Header.Set("Origin", "https://example.test")
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if boundary == "body" {
				r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
			}
			w := httptest.NewRecorder()
			func() {
				defer func() {
					if value := recover(); value != nil && value != http.ErrAbortHandler {
						panic(value)
					}
				}()
				handler.ServeHTTP(w, r)
			}()
			current, err := repository.GetByID(t.Context(), post.ID)
			if !changed || wrapped.mutations != 0 || err != nil || current.Version != post.Version || w.Body.Len() != 0 {
				t.Errorf("cancelled request completed: calls=%d bytes=%d version=%d err=%v", wrapped.mutations, w.Body.Len(), current.Version, err)
			}
		})
	}
}

func TestEditorAuthoritySameAccountFallbackCannotTakeOver(t *testing.T) {
	s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
	user, cookie, actor := createAndLoginTestUser(t, manager, owner, "editor-fallback", "editor-fallback@example.test", "Editor", "editor")
	token, _, err := manager.AuthenticateContext(t.Context(), "second-editor-session", user.Username, "Account password for editor-fallback 2026")
	if err != nil {
		t.Fatal(err)
	}
	post, _, _ := autosaveAuthorityFixture(t, s, repository, actor)
	wrapped := &editorAuthorityRepository{Repository: repository, afterAutosave: func() { autosaveAuthorityRevoke(t, manager, owner, user, cookie, "logout") }}
	s.repository = wrapped
	form := editorAuthorityForm(actor, post)
	form.Set("published_source", "autosave")
	w := editorSubmit(handler, cookie, fmt.Sprintf("/admin/content/%d/edit/", post.ID), "same-account-fallback", form, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token}) })
	if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/admin/login/") || wrapped.mutations != 0 {
		t.Fatal("second session took over", w.Code, wrapped.mutations)
	}
}

func TestEditorAuthorityLocalReadOnlyCompatibility(t *testing.T) {
	s, _, repository, _, actor := newAuthenticatedContentServer(t)
	post, _, _ := autosaveAuthorityFixture(t, s, repository, actor)
	manager, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s.auth = manager
	for _, path := range []string{"/admin/content/new/", fmt.Sprintf("/admin/content/%d/edit/", post.ID)} {
		r := httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil)
		r.RemoteAddr = "127.0.0.1:41234"
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "本机只读预览") {
			t.Fatal("local read-only editor lost", path, w.Code)
		}
	}
}

func TestEditorAuthorityRejectedRequestRetainsRetryKey(t *testing.T) {
	for _, create := range []bool{false, true} {
		t.Run(fmt.Sprint("create=", create), func(t *testing.T) {
			t.Parallel()
			s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "editor-retry", "editor-retry@example.test", "Editor", "editor")
			post, _, _ := autosaveAuthorityFixture(t, s, repository, actor)
			path := fmt.Sprintf("/admin/content/%d/edit/", post.ID)
			form := editorAuthorityForm(actor, post)
			if create {
				path = "/admin/content/new/"
				form.Set("slug", "editor-retry-created")
				form.Del("version")
			}
			wrapped := &editorAuthorityRepository{Repository: repository, afterFind: func() { autosaveAuthorityRevoke(t, manager, owner, user, cookie, "logout") }}
			s.repository = wrapped
			w := editorSubmit(handler, cookie, path, "editor-retry-key", form, nil)
			if w.Code != 303 || wrapped.mutations != 0 {
				t.Fatal("revoked request changed content", w.Code, wrapped.mutations)
			}
			token, renewed, err := manager.AuthenticateContext(t.Context(), "editor-retry-renewed", user.Username, "Account password for editor-retry 2026")
			if err != nil {
				t.Fatal(err)
			}
			form.Set("csrf_token", renewed.CSRFToken)
			cookie = &http.Cookie{Name: auth.CookieName, Value: token}
			for _, replay := range []bool{false, true} {
				w = editorSubmit(handler, cookie, path, "editor-retry-key", form, nil)
				if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"replayed":%v`, replay)) || wrapped.mutations != 1 {
					t.Fatal("rejected key was consumed or duplicated", w.Code, wrapped.mutations, w.Body.String())
				}
			}
		})
	}
}

func TestEditorAuthorityCreationReceiptSurvivesDeletion(t *testing.T) {
	_, handler, repository, cookie, actor := newAuthenticatedContentServer(t)
	form := editorAuthorityForm(actor, content.Post{Slug: "deleted-editor-creation"})
	form.Del("version")
	w := editorSubmit(handler, cookie, "/admin/content/new/", "deleted-create-receipt", form, nil)
	var result struct{ ID int64 }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal("create failed", w.Code)
	}
	p, err := repository.ChangeStatus(t.Context(), result.ID, content.Mutation{ExpectedVersion: 1}, content.StatusTrash)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.BulkChangeStatus(t.Context(), content.BulkStatusChange{Delete: true, Status: content.StatusTrash, Items: []content.BulkMutation{{ID: p.ID, ExpectedVersion: p.Version}}})
	if err != nil {
		t.Fatal(err)
	}
	w = editorSubmit(handler, cookie, "/admin/content/new/", "deleted-create-receipt", form, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("known creation lost confirmation", w.Code, w.Body.String())
	}
	if _, err := repository.GetByID(t.Context(), p.ID); !errors.Is(err, content.ErrNotFound) {
		t.Fatal("deleted content recreated", err)
	}
}
