package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
)

type commentAuthorityStore struct {
	comment.Store
	afterGet, afterList, afterRevisions, afterMutation func()
	mutations                                          int
}

func (s *commentAuthorityStore) Get(ctx context.Context, id int64) (comment.Comment, error) {
	v, err := s.Store.Get(ctx, id)
	runAutosaveBoundary(&s.afterGet)
	return v, err
}
func (s *commentAuthorityStore) ListAdmin(ctx context.Context, q comment.Query) (comment.List, error) {
	v, err := s.Store.ListAdmin(ctx, q)
	runAutosaveBoundary(&s.afterList)
	return v, err
}
func (s *commentAuthorityStore) ListRevisions(ctx context.Context, id int64, limit int) ([]comment.Revision, error) {
	v, err := s.Store.ListRevisions(ctx, id, limit)
	runAutosaveBoundary(&s.afterRevisions)
	return v, err
}
func (s *commentAuthorityStore) ChangeStatus(ctx context.Context, id int64, m comment.Mutation, status string) (comment.Comment, error) {
	s.mutations++
	v, err := s.Store.ChangeStatus(ctx, id, m, status)
	runAutosaveBoundary(&s.afterMutation)
	return v, err
}
func (s *commentAuthorityStore) Delete(ctx context.Context, id int64, m comment.Mutation) error {
	s.mutations++
	err := s.Store.Delete(ctx, id, m)
	runAutosaveBoundary(&s.afterMutation)
	return err
}
func (s *commentAuthorityStore) BulkChangeStatus(ctx context.Context, change comment.BulkStatusChange) ([]comment.Comment, error) {
	s.mutations++
	v, err := s.Store.BulkChangeStatus(ctx, change)
	runAutosaveBoundary(&s.afterMutation)
	return v, err
}
func (s *commentAuthorityStore) Edit(ctx context.Context, id int64, m comment.Mutation, input comment.EditInput) (comment.Comment, error) {
	s.mutations++
	v, err := s.Store.Edit(ctx, id, m, input)
	runAutosaveBoundary(&s.afterMutation)
	return v, err
}
func (s *commentAuthorityStore) Reply(ctx context.Context, input comment.ReplyInput) (comment.Comment, error) {
	s.mutations++
	v, err := s.Store.Reply(ctx, input)
	runAutosaveBoundary(&s.afterMutation)
	return v, err
}
func (s *commentAuthorityStore) RestoreRevision(ctx context.Context, id, version int64, m comment.Mutation) (comment.Comment, error) {
	s.mutations++
	v, err := s.Store.RestoreRevision(ctx, id, version, m)
	runAutosaveBoundary(&s.afterMutation)
	return v, err
}

func commentAuthorityFixture(t *testing.T, s *Server, postID int64) comment.Comment {
	t.Helper()
	c, err := s.comments.Create(t.Context(), comment.CreateInput{PostID: postID, AuthorName: "Private commenter", AuthorEmail: "private-comment-authority@example.test", Status: comment.StatusApproved, ContentSource: "PRIVATE-COMMENT-ORIGINAL"})
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.comments.Edit(t.Context(), c.ID, comment.Mutation{ExpectedVersion: c.Version, Actor: "owner"}, comment.EditInput{AuthorName: c.AuthorName, AuthorEmail: c.AuthorEmail, ContentSource: "PRIVATE-COMMENT-CURRENT"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func commentAuthorityMutationInput(operation string, c comment.Comment, p auth.Principal) (string, string, string, map[string]string) {
	method, path, body := http.MethodPost, "/admin/comments/", ""
	headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/x-www-form-urlencoded"}
	base := "/admin/comments/" + strconv.FormatInt(c.ID, 10)
	var form url.Values
	switch operation {
	case "form-status", "form-delete":
		action := comment.StatusApproved
		if operation == "form-delete" {
			action = "delete"
		}
		form = adminCommentFormValues(p, c, action)
	case "form-bulk":
		path, form = "/admin/comments/bulk/", bulkCommentValues(p.CSRFToken, comment.StatusApproved, c)
	case "form-edit":
		path, form = base+"/edit/", commentEditorEditValues(p.CSRFToken, c.Version, c.AuthorName, c.AuthorEmail, "Updated comment", "approved")
	case "form-reply":
		path, form = base+"/reply/", commentEditorReplyValues(p.CSRFToken, c.Version, "Staff reply", "approved")
	case "form-restore":
		path, form = base+"/revisions/1/restore/", commentEditorRestoreValues(p.CSRFToken, c.Version, "approved")
	default:
		path = "/api/v1/admin/comments/" + strconv.FormatInt(c.ID, 10)
		headers = authenticatedAPIHeaders(p)
		headers["If-Match"] = commentETag(c)
		switch operation {
		case "api-status":
			method, body = http.MethodPatch, fmt.Sprintf(`{"version":%d,"status":"spam"}`, c.Version)
		case "api-edit":
			method, body = http.MethodPut, fmt.Sprintf(`{"version":%d,"author_name":"Edited","author_email":"edited@example.test","content_source":"Updated comment"}`, c.Version)
		case "api-reply":
			path += "/replies"
			body = fmt.Sprintf(`{"parent_version":%d,"content_source":"Staff reply"}`, c.Version)
		case "api-restore":
			path += "/revisions/1/restore"
		case "api-delete":
			method = http.MethodDelete
		}
	}
	if form != nil {
		body = form.Encode()
	}
	return method, path, body, headers
}

func commentAuthorityServe(handler http.Handler, r *http.Request) (*httptest.ResponseRecorder, bool) {
	w, aborted := httptest.NewRecorder(), false
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
	return w, aborted
}

func TestCommentAuthorityMutationBoundaries(t *testing.T) {
	operations := []string{"form-status", "form-delete", "form-bulk", "form-edit", "form-reply", "form-restore", "api-status", "api-delete", "api-edit", "api-reply", "api-restore"}
	for _, operation := range operations {
		boundaries := []string{"body", "committed"}
		if operation == "form-delete" || operation == "api-delete" || operation == "api-restore" {
			boundaries = append(boundaries, "read")
		}
		for _, boundary := range boundaries {
			for _, change := range []string{"logout", "logout-with-fallback", "permission", "module", "cancel", "allowed"} {
				t.Run(operation+"/"+boundary+"/"+change, func(t *testing.T) {
					t.Parallel()
					s, handler, manager, repository, fallback, owner := newPersistentAdminTestServer(t)
					user, cookie, actor := createAndLoginTestUser(t, manager, owner, "comment-authority", "comment-authority@example.test", "Editor", "editor")
					post := createOwnedTestPost(t, repository, owner.UserID, owner.Label(), "comment-authority", "PRIVATE-COMMENT-PARENT")
					c := commentAuthorityFixture(t, s, post.ID)
					if strings.HasSuffix(operation, "delete") {
						var err error
						c, err = s.comments.ChangeStatus(t.Context(), c.ID, comment.Mutation{ExpectedVersion: c.Version}, comment.StatusTrash)
						if err != nil {
							t.Fatal(err)
						}
					}
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					changed := false
					after := func() {
						changed = true
						if change == "cancel" {
							cancel()
						} else if change == "module" {
							accountAuthorityModule(t, s, "comments", false)
						} else {
							autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
						}
					}
					store := &commentAuthorityStore{Store: s.comments}
					if boundary == "committed" {
						store.afterMutation = after
					}
					if boundary == "read" {
						store.afterGet = after
					}
					s.comments = store
					method, path, body, headers := commentAuthorityMutationInput(operation, c, actor)
					r := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body)).WithContext(ctx)
					r.AddCookie(cookie)
					if change == "logout-with-fallback" {
						r.AddCookie(fallback)
					}
					for key, value := range headers {
						r.Header.Set(key, value)
					}
					if boundary == "body" {
						r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
					}
					w, aborted := commentAuthorityServe(handler, r)
					if !changed {
						t.Fatal("boundary not reached", w.Code, w.Body.String())
					}
					wantCalls := 0
					if change == "allowed" || boundary == "committed" {
						wantCalls = 1
					}
					if store.mutations != wantCalls {
						t.Fatal("revoked write reached store", store.mutations, wantCalls)
					}
					if change == "cancel" {
						if !aborted || w.Body.Len() != 0 {
							t.Fatal("cancelled write returned data", aborted, w.Code)
						}
						return
					}
					want := 403
					if change == "module" {
						want = 404
					}
					if strings.HasPrefix(change, "logout") {
						want = 401
						if strings.HasPrefix(operation, "form-") {
							want = 303
						}
					}
					if change == "allowed" {
						want = 200
						if strings.HasPrefix(operation, "form-") {
							want = 303
						}
						if operation == "api-delete" {
							want = 204
						}
						if operation == "api-reply" {
							want = 201
						}
					}
					if aborted || w.Code != want {
						t.Fatal("write authority response", aborted, w.Code, want, w.Body.String())
					}
					if change != "allowed" && (strings.Contains(w.Body.String(), "PRIVATE-COMMENT") || strings.Contains(w.Body.String(), "private-comment-authority@") || w.Header().Get("ETag") != "" || strings.Contains(w.Header().Get("Location"), "notice=")) {
						t.Fatal("revoked write leaked private output", w.Header())
					}
					if boundary == "committed" {
						list, err := store.Store.ListAdmin(t.Context(), comment.Query{Limit: 20})
						if err != nil || strings.HasSuffix(operation, "reply") && list.Total != 2 || strings.HasSuffix(operation, "delete") && list.Total != 0 {
							t.Fatal("committed mutation lost", list.Total, err)
						}
					}
				})
			}
		}
	}
}

func TestCommentAuthorityReadBoundaries(t *testing.T) {
	for _, endpoint := range []string{"list-page", "editor-page", "list-api", "item-api", "revisions-api", "render-page"} {
		for _, change := range []string{"logout", "logout-with-fallback", "permission", "module", "cancel", "allowed"} {
			t.Run(endpoint+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, fallback, owner := newPersistentAdminTestServer(t)
				user, cookie, _ := createAndLoginTestUser(t, manager, owner, "comment-reader", "comment-reader@example.test", "Editor", "editor")
				post := createOwnedTestPost(t, repository, owner.UserID, owner.Label(), "comment-reader", "PRIVATE-COMMENT-PARENT")
				c := commentAuthorityFixture(t, s, post.ID)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				changed := false
				after := func() {
					changed = true
					if change == "cancel" {
						cancel()
					} else if change == "module" {
						accountAuthorityModule(t, s, "comments", false)
					} else {
						autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
					}
				}
				store := &commentAuthorityStore{Store: s.comments}
				path := "/api/v1/admin/comments/" + strconv.FormatInt(c.ID, 10)
				switch endpoint {
				case "list-page":
					path, store.afterList = "/admin/comments/?status=all", after
				case "editor-page":
					path, store.afterRevisions = "/admin/comments/"+strconv.FormatInt(c.ID, 10)+"/edit/", after
				case "list-api":
					path, store.afterList = "/api/v1/admin/comments?status=all", after
				case "item-api":
					store.afterGet = after
				case "revisions-api":
					path, store.afterRevisions = path+"/revisions", after
				case "render-page":
					path = "/admin/comments/?status=all"
					s.repository = &editorAuthorityRepository{Repository: repository, afterTerms: after}
				}
				s.comments = store
				r := httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil).WithContext(ctx)
				r.AddCookie(cookie)
				if change == "logout-with-fallback" {
					r.AddCookie(fallback)
				}
				w, aborted := commentAuthorityServe(handler, r)
				if !changed {
					t.Fatal("read boundary not reached", w.Code)
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancelled read returned data", aborted, w.Code)
					}
					return
				}
				want := 403
				if change == "module" {
					want = 404
				}
				if strings.HasPrefix(change, "logout") {
					want = 401
					if strings.Contains(endpoint, "page") {
						want = 303
					}
				}
				if change == "allowed" {
					want = 200
				}
				if aborted || w.Code != want || change != "allowed" && (strings.Contains(w.Body.String(), "PRIVATE-COMMENT") || strings.Contains(w.Body.String(), "private-comment-authority@") || w.Header().Get("ETag") != "") {
					t.Fatal("read authority response", aborted, w.Code, want)
				}
				if change == "allowed" && !strings.Contains(w.Body.String(), "PRIVATE-COMMENT") {
					t.Fatal("allowed comment content missing")
				}
			})
		}
	}
}
