package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func contentAuthorityDisable(t *testing.T, s *Server, enabled bool) {
	t.Helper()
	settings := s.siteSettings.Snapshot()
	settings.Settings.API.AdminContentEnabled = enabled
	if _, _, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil {
		t.Error(err)
	}
}

func contentAuthorityNarrow(t *testing.T, manager *auth.Manager, owner auth.Principal, user auth.User) {
	t.Helper()
	if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Status: user.Status, RoleSlugs: []string{"author"}}); err != nil {
		t.Error(err)
	}
}

func TestContentAuthorityRevocationDuringStream(t *testing.T) {
	for _, change := range []string{"logout", "scope", "disabled", "cancel", "allowed"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
			user, cookie, _ := createAndLoginTestUser(t, manager, owner, "api-stream", "api-stream@example.test", "Reader", "editor")
			post := createOwnedTestPost(t, repository, owner.UserID, owner.Label(), "api-stream", "Private stream")
			const tail = "PRIVATE-CONTENT-STREAM-TAIL"
			input := inputFromPost(post)
			input.ContentSource = "<p>" + strings.Repeat("x", content.MaxContentBytes-256) + tail + "</p>"
			if _, err := repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, input); err != nil {
				t.Fatal(err)
			}
			var changed atomic.Bool
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				handler.ServeHTTP(&storageAfterWrite{ResponseWriter: w, after: func() {
					autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
					if change == "scope" {
						contentAuthorityNarrow(t, manager, owner, user)
					}
					if change == "disabled" {
						contentAuthorityDisable(t, s, false)
					}
					if change == "cancel" {
						cancel()
					}
					changed.Store(true)
				}}, r.WithContext(ctx))
			}))
			defer local.Close()
			r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("%s/api/v1/admin/contents/%d", local.URL, post.ID), nil)
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
				t.Fatal("stream boundary not reached", response.StatusCode, readErr)
			}
			if change == "allowed" {
				if readErr != nil || !strings.Contains(string(got), tail) || !json.Valid(got) {
					t.Fatal("allowed JSON lost", readErr)
				}
				return
			}
			if !errors.Is(readErr, io.ErrUnexpectedEOF) || len(got) == 0 || len(got) > 1<<20 || strings.Contains(string(got), tail) || strings.Contains(string(got), "internal_server_error") {
				t.Fatalf("revoked stream completed: bytes=%d err=%v", len(got), readErr)
			}
		})
	}
}

type contentAuthorityMarshaler struct{ after func() }

func (v contentAuthorityMarshaler) MarshalJSON() ([]byte, error) {
	v.after()
	return []byte(`{"private":"PRIVATE-CONTENT-ENCODE"}`), nil
}

func TestContentAuthorityEncodingBoundary(t *testing.T) {
	for _, change := range []string{"logout", "disabled", "allowed"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, _, repository, cookie, actor := newAuthenticatedContentServer(t)
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "api-encoding", "Private")
			handler := s.sessionState(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				value := contentAuthorityMarshaler{after: func() {
					if change == "logout" {
						if err := s.auth.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Error(err)
						}
					}
					if change == "disabled" {
						contentAuthorityDisable(t, s, false)
					}
				}}
				w.Header().Set("Idempotency-Replayed", "true")
				s.writeContentJSON(w, r, 200, value, func() { s.setContentAPIHeaders(w, post) }, func() error { return s.contentObjectAccess(r, post.ID, false, true) })
			}))
			w := contentAuthorityRequest(handler, http.MethodGet, "/api/v1/admin/contents/1", "", cookie, nil, nil)
			want := 200
			if change == "logout" {
				want = 401
			}
			if change == "disabled" {
				want = 404
			}
			if w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
			if change != "allowed" && (strings.Contains(w.Body.String(), "PRIVATE-") || w.Header().Get("ETag") != "" || w.Header().Get("Idempotency-Replayed") != "") {
				t.Fatal("encoding boundary leaked content", w.Header())
			}
		})
	}
}

func TestContentAuthorityListScopeIncludesCounts(t *testing.T) {
	for _, path := range []string{"/admin/content/", "/api/v1/admin/contents"} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/empty=%v", path, empty), func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "count-scope", "count-scope@example.test", "Reader", "editor")
				createOwnedTestPost(t, repository, owner.UserID, owner.Label(), "hidden-count", "Foreign")
				createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "own-count", "Own")
				// The prepared page contains only this author, or no rows; its counts
				// still describe both authors and must not survive scope narrowing.
				wrapped := &contentAuthorityRepository{editorAuthorityRepository: &editorAuthorityRepository{Repository: repository}, afterList: func() { contentAuthorityNarrow(t, manager, owner, user) }}
				s.repository = wrapped
				query := "?page=1&per_page=1"
				if path == "/admin/content/" {
					query = "?q=Own"
				}
				if empty {
					query = "?page=9999"
				}
				w := contentAuthorityRequest(handler, http.MethodGet, path+query, "", cookie, nil, nil)
				if w.Code != 403 || strings.Contains(w.Body.String(), `"total":`) || strings.Contains(w.Body.String(), `status_counts`) {
					t.Fatal("narrowed list exposed counts", w.Code, w.Body.String())
				}
			})
		}
	}
}

type contentAuthorityBatchRepository struct {
	*contentAuthorityRepository
	afterBatch         func()
	batches, fullReads int
	transferredID      int64
	newAuthor          int64
}

func (r *contentAuthorityBatchRepository) GetPostStates(ctx context.Context, ids []int64) (map[int64]content.PostState, error) {
	r.batches++
	states, err := content.ReadPostStates(ctx, r.Repository, ids)
	if state, ok := states[r.transferredID]; ok {
		state.AuthorID = r.newAuthor
		states[r.transferredID] = state
	}
	runAutosaveBoundary(&r.afterBatch)
	return states, err
}
func (r *contentAuthorityBatchRepository) GetByID(ctx context.Context, id int64) (content.Post, error) {
	r.fullReads++
	return r.editorAuthorityRepository.GetByID(ctx, id)
}

func TestContentAuthorityListBatchBoundary(t *testing.T) {
	for _, change := range []string{"transfer", "logout", "allowed"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "batch-author", "batch-author@example.test", "Author", "author")
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "batch-author-one", "PRIVATE-BATCH-CONTENT")
			createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "batch-author-two", "PRIVATE-BATCH-CONTENT")
			wrapped := &contentAuthorityBatchRepository{contentAuthorityRepository: &contentAuthorityRepository{editorAuthorityRepository: &editorAuthorityRepository{Repository: repository}}}
			if change == "logout" {
				wrapped.afterBatch = func() { autosaveAuthorityRevoke(t, manager, owner, user, cookie, "logout") }
			}
			if change == "transfer" {
				wrapped.afterList = func() {
					// Simulate an external ownership change in the projection; the
					// ordinary editor Update intentionally preserves AuthorID.
					wrapped.transferredID, wrapped.newAuthor = post.ID, owner.UserID
				}
			}
			s.repository = wrapped
			w := contentAuthorityRequest(handler, http.MethodGet, "/api/v1/admin/contents", "", cookie, nil, nil)
			want := 200
			if change == "transfer" {
				want = 403
			}
			if change == "logout" {
				want = 401
			}
			if w.Code != want || wrapped.batches < 1 || wrapped.batches > 2 || wrapped.fullReads != 0 {
				t.Fatal("batch authority failed", w.Code, wrapped.batches, wrapped.fullReads)
			}
			if change != "allowed" && strings.Contains(w.Body.String(), "PRIVATE-BATCH-CONTENT") {
				t.Fatal("batch scope leaked content")
			}
		})
	}
}

func TestContentAuthorityPreconditionBoundaries(t *testing.T) {
	for _, match := range []string{"", `"stale"`} {
		for _, change := range []string{"logout", "disabled", "allowed"} {
			t.Run(match+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, handler, repository, cookie, actor := newAuthenticatedContentServer(t)
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "precondition", "Private")
				wrapped := &editorAuthorityRepository{Repository: repository, afterPost: func() {
					if change == "logout" {
						if err := s.auth.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Error(err)
						}
					}
					if change == "disabled" {
						contentAuthorityDisable(t, s, false)
					}
				}}
				s.repository = wrapped
				headers := authenticatedAPIHeaders(actor)
				headers["If-Match"] = match
				w := contentAuthorityRequest(handler, http.MethodPatch, fmt.Sprintf("/api/v1/admin/contents/%d", post.ID), `{"title":"Changed"}`, cookie, headers, nil)
				want := 412
				if match == "" {
					want = 428
				}
				if change == "logout" {
					want = 401
				}
				if change == "disabled" {
					want = 404
				}
				if w.Code != want || wrapped.mutations != 0 {
					t.Fatal("precondition authority failed", w.Code, wrapped.mutations)
				}
				if change != "allowed" && (w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "current_etag") || strings.Contains(w.Body.String(), "current_version")) {
					t.Fatal("revoked precondition leaked version", w.Body.String())
				}
			})
		}
	}
}

func TestContentAuthorityRejectedKeyCanRetry(t *testing.T) {
	s, handler, repository, cookie, actor := newAuthenticatedContentServer(t)
	wrapped := &editorAuthorityRepository{Repository: repository, afterFind: func() { contentAuthorityDisable(t, s, false) }}
	s.repository = wrapped
	headers := authenticatedAPIHeaders(actor)
	headers["Idempotency-Key"] = "authority-retry-key"
	body := `{"title":"Retry content","slug":"retry-content"}`
	w := contentAuthorityRequest(handler, http.MethodPost, "/api/v1/admin/contents", body, cookie, headers, nil)
	if w.Code != 404 || wrapped.mutations != 0 {
		t.Fatal("disabled API consumed key", w.Code, wrapped.mutations)
	}
	contentAuthorityDisable(t, s, true)
	for _, replay := range []bool{false, true} {
		w = contentAuthorityRequest(handler, http.MethodPost, "/api/v1/admin/contents", body, cookie, headers, nil)
		if w.Code != 201 || wrapped.mutations != 1 || (w.Header().Get("Idempotency-Replayed") == "true") != replay {
			t.Fatal("retry failed or duplicated write", w.Code, wrapped.mutations, w.Body.String())
		}
	}
}

func TestContentAuthorityLocalReadOnlyCompatibility(t *testing.T) {
	s, _, repository, _, actor := newAuthenticatedContentServer(t)
	post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "local-content", "Local private content")
	manager, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s.auth = manager
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/api/v1/admin/contents"
		if method == http.MethodGet {
			path += fmt.Sprintf("/%d", post.ID)
		}
		w := contentAuthorityRequest(s.routes(), method, path, `{"title":"Created"}`, nil, nil, func(r *http.Request) { r.RemoteAddr = "127.0.0.1:32145" })
		want := 200
		if method == http.MethodPost {
			want = 403
		}
		if w.Code != want {
			t.Fatal("local read-only changed", method, w.Code, w.Body.String())
		}
	}
}
