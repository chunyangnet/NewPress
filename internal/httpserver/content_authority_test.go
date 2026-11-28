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

type contentAuthorityRepository struct {
	*editorAuthorityRepository
	afterList, afterRevision, afterRevisions func()
}

func (r *contentAuthorityRepository) ListAdmin(ctx context.Context, q content.AdminQuery) (content.AdminList, error) {
	result, err := r.Repository.ListAdmin(ctx, q)
	runAutosaveBoundary(&r.afterList)
	return result, err
}
func (r *contentAuthorityRepository) GetRevision(ctx context.Context, id, revision int64) (content.Revision, error) {
	result, err := r.Repository.GetRevision(ctx, id, revision)
	runAutosaveBoundary(&r.afterRevision)
	return result, err
}
func (r *contentAuthorityRepository) ListRevisions(ctx context.Context, id int64, limit int) ([]content.Revision, error) {
	result, err := r.Repository.ListRevisions(ctx, id, limit)
	runAutosaveBoundary(&r.afterRevisions)
	return result, err
}

func contentAuthorityRequest(handler http.Handler, method, path, body string, cookie *http.Cookie, headers map[string]string, configure func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	if configure != nil {
		configure(r)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestContentAuthorityReadBoundaries(t *testing.T) {
	for _, operation := range []string{"list-page", "list-api", "get", "get-304", "revisions", "revisions-304", "compare"} {
		for _, change := range []string{"logout", "logout-with-fallback", "permission", "scope", "disabled", "allowed"} {
			if change == "disabled" && (operation == "list-page" || operation == "compare") {
				continue
			}
			t.Run(operation+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "content-reader", "content-reader@example.test", "Reader", "editor")
				post := createOwnedTestPost(t, repository, owner.UserID, owner.Label(), "private-content-target", "PRIVATE-CONTENT-AUTHORITY")
				revisions, err := repository.ListRevisions(t.Context(), post.ID, 10)
				if err != nil {
					t.Fatal(err)
				}
				changed := false
				after := func() {
					changed = true
					autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
					if change == "scope" {
						_, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Status: user.Status, RoleSlugs: []string{"author"}})
						if err != nil {
							t.Fatal(err)
						}
					}
					if change == "disabled" {
						settings := s.siteSettings.Snapshot()
						settings.Settings.API.AdminContentEnabled = false
						if _, _, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil {
							t.Fatal(err)
						}
					}
				}
				wrapped := &contentAuthorityRepository{editorAuthorityRepository: &editorAuthorityRepository{Repository: repository}}
				path := fmt.Sprintf("/api/v1/admin/contents/%d", post.ID)
				headers := map[string]string{}
				switch operation {
				case "list-page":
					path = "/admin/content/"
					wrapped.afterList = after
				case "list-api":
					path = "/api/v1/admin/contents"
					wrapped.afterList = after
				case "compare":
					path = fmt.Sprintf("/admin/content/%d/revisions/compare/?from=%d&to=current", post.ID, revisions[0].ID)
					wrapped.afterRevision = after
				case "revisions":
					path += "/revisions"
					wrapped.afterRevisions = after
				case "revisions-304":
					path += "/revisions"
					headers["If-None-Match"] = contentRevisionsAPIETag(post)
					wrapped.afterPost = after
				case "get", "get-304":
					wrapped.afterPost = after
					if operation == "get-304" {
						headers["If-None-Match"] = contentAPIETag(post)
					}
				}
				s.repository = wrapped
				w := contentAuthorityRequest(handler, http.MethodGet, path, "", cookie, headers, func(r *http.Request) {
					if change == "logout-with-fallback" {
						r.AddCookie(ownerCookie)
					}
				})
				want := 200
				if change == "allowed" && strings.HasSuffix(operation, "304") {
					want = 304
				}
				if change == "logout" || change == "logout-with-fallback" {
					want = 401
					if strings.HasPrefix(path, "/admin/") {
						want = 303
					}
				}
				if change == "permission" || change == "scope" {
					want = 403
				}
				if change == "disabled" {
					want = 404
				}
				if !changed || w.Code != want || (change != "allowed" && (strings.Contains(w.Body.String(), "PRIVATE-CONTENT-AUTHORITY") || w.Header().Get("ETag") != "" || w.Header().Get("Last-Modified") != "")) {
					t.Errorf("revoked read exposed data/validators: status=%d want=%d changed=%v etag=%q", w.Code, want, changed, w.Header().Get("ETag"))
				}
				if change == "allowed" && want == 200 && !strings.Contains(w.Body.String(), "PRIVATE-CONTENT-AUTHORITY") {
					t.Error("allowed read lost content")
				}
				_ = actor
			})
		}
	}
}

func TestContentAuthorityAPIMutationBoundaries(t *testing.T) {
	for _, operation := range []string{"create-body", "create-key-body", "create-key-find", "patch-body", "patch-read", "delete-read", "restore-read", "replay-find", "replay-read"} {
		for _, change := range []string{"logout", "permission", "disabled", "allowed"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "content-writer", "content-writer@example.test", "Writer", "editor")
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "api-write-target", "Original")
				revisions, err := repository.ListRevisions(t.Context(), post.ID, 10)
				if err != nil {
					t.Fatal(err)
				}
				path, method, body := fmt.Sprintf("/api/v1/admin/contents/%d", post.ID), http.MethodPatch, `{"title":"Updated"}`
				headers := authenticatedAPIHeaders(actor)
				headers["If-Match"] = contentAPIETag(post)
				if strings.HasPrefix(operation, "create") || strings.HasPrefix(operation, "replay") {
					path, method, body = "/api/v1/admin/contents", http.MethodPost, `{"title":"Created","slug":"api-created"}`
					delete(headers, "If-Match")
				}
				if strings.Contains(operation, "key") || strings.HasPrefix(operation, "replay") {
					headers["Idempotency-Key"] = "content-authority-api-key"
				}
				if strings.HasPrefix(operation, "delete") {
					method, body = http.MethodDelete, ""
				}
				if strings.HasPrefix(operation, "restore") {
					method, body = http.MethodPost, ""
					path += fmt.Sprintf("/revisions/%d/restore", revisions[0].ID)
				}
				if strings.HasPrefix(operation, "replay") {
					first := contentAuthorityRequest(handler, method, path, body, cookie, headers, nil)
					if first.Code != 201 {
						t.Fatal(first.Code, first.Body.String())
					}
				}
				before, err := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 100})
				if err != nil {
					t.Fatal(err)
				}
				changed := false
				after := func() {
					changed = true
					autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
					if change == "disabled" {
						settings := s.siteSettings.Snapshot()
						settings.Settings.API.AdminContentEnabled = false
						if _, _, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil {
							t.Fatal(err)
						}
					}
				}
				wrapped := &editorAuthorityRepository{Repository: repository}
				if strings.HasSuffix(operation, "read") {
					wrapped.afterPost = after
				}
				if strings.HasSuffix(operation, "find") {
					wrapped.afterFind = after
				}
				s.repository = wrapped
				w := contentAuthorityRequest(handler, method, path, body, cookie, headers, func(r *http.Request) {
					if strings.HasSuffix(operation, "body") {
						r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
					}
				})
				current, err := repository.GetByID(t.Context(), post.ID)
				if err != nil {
					t.Fatal(err)
				}
				afterList, err := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 100})
				if err != nil {
					t.Fatal(err)
				}
				want := 200
				if method == http.MethodPost && path == "/api/v1/admin/contents" {
					want = 201
				}
				if change == "logout" {
					want = 401
				}
				if change == "permission" {
					want = 403
				}
				if change == "disabled" {
					want = 404
				}
				if !changed || w.Code != want {
					t.Errorf("mutation authority ignored: status=%d want=%d changed=%v", w.Code, want, changed)
				}
				if change != "allowed" && (wrapped.mutations != 0 || current.Version != post.Version || afterList.Total != before.Total || w.Header().Get("ETag") != "" || w.Header().Get("Idempotency-Replayed") != "") {
					t.Errorf("revoked mutation committed or returned result: calls=%d version=%d total=%d", wrapped.mutations, current.Version, afterList.Total)
				}
			})
		}
	}
}
