package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

type accountAuthorityHeaders struct {
	content.Repository
	afterHeaders     func()
	reads, fullReads int
	transferID       int64
}

func (r *accountAuthorityHeaders) GetPostHeaders(ctx context.Context, ids []int64) (map[int64]content.PostHeader, error) {
	r.reads++
	v, err := content.ReadPostHeaders(ctx, r.Repository, ids)
	runAutosaveBoundary(&r.afterHeaders)
	return v, err
}
func (r *accountAuthorityHeaders) GetByID(ctx context.Context, id int64) (content.Post, error) {
	r.fullReads++
	return r.Repository.GetByID(ctx, id)
}
func (r *accountAuthorityHeaders) GetPostStates(ctx context.Context, ids []int64) (map[int64]content.PostState, error) {
	v, err := content.ReadPostStates(ctx, r.Repository, ids)
	if p, ok := v[r.transferID]; ok {
		p.AuthorID++
		v[p.ID] = p
	}
	return v, err
}

type accountAuthorityStateComments struct {
	*accountAuthorityComments
	afterState func()
	reads      int
}

func (s *accountAuthorityStateComments) GetCommentStates(ctx context.Context, ids []int64) (map[int64]comment.State, error) {
	s.reads++
	v, err := comment.ReadStates(ctx, s.Store, ids)
	runAutosaveBoundary(&s.afterState)
	return v, err
}

func accountAuthorityModule(t *testing.T, s *Server, id string, enabled bool) {
	t.Helper()
	snapshot := s.siteSettings.Snapshot()
	if snapshot.Settings.Modules == nil {
		snapshot.Settings.Modules = map[string]bool{}
	}
	snapshot.Settings.Modules[id] = enabled
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) > 0 {
		t.Error(fields, err)
	}
}

func TestAccountContentAuthorityCommentBoundaries(t *testing.T) {
	for _, change := range []string{"trash", "deleted", "edited", "comments-disabled", "parent-private", "parent-deleted", "flash-disabled", "logout-after-header", "logout-after-state", "cancel", "allowed"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
			_, cookie, actor := createAndLoginTestUser(t, manager, owner, "comment-boundary", "comment-boundary@example.test", "Subscriber", "subscriber")
			postType := content.TypePost
			if change == "flash-disabled" {
				postType = content.TypeFlash
			}
			post, err := repository.Create(t.Context(), content.PostInput{Type: postType, Title: "PRIVATE-PARENT-BOUNDARY", Slug: "private-parent-boundary", ContentSource: "Public parent body", Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour), AuthorID: owner.UserID})
			if err != nil {
				t.Fatal(err)
			}
			c, err := s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, UserID: actor.UserID, AuthorName: actor.Label(), AuthorEmail: "reader@example.test", ContentSource: "PRIVATE-COMMENT-BOUNDARY", Status: comment.StatusApproved})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			baseComments := s.comments
			parents := &accountAuthorityHeaders{Repository: repository}
			comments := &accountAuthorityStateComments{accountAuthorityComments: &accountAuthorityComments{Store: baseComments}}
			changed := false
			after := func() {
				changed = true
				switch change {
				case "trash":
					_, err = baseComments.ChangeStatus(t.Context(), c.ID, comment.Mutation{ExpectedVersion: c.Version}, comment.StatusTrash)
				case "deleted":
					var trashed comment.Comment
					trashed, err = baseComments.ChangeStatus(t.Context(), c.ID, comment.Mutation{ExpectedVersion: c.Version}, comment.StatusTrash)
					if err == nil {
						err = baseComments.Delete(t.Context(), c.ID, comment.Mutation{ExpectedVersion: trashed.Version})
					}
				case "edited":
					_, err = baseComments.Edit(t.Context(), c.ID, comment.Mutation{ExpectedVersion: c.Version, Actor: "editor"}, comment.EditInput{AuthorName: c.AuthorName, AuthorEmail: c.AuthorEmail, ContentSource: "replacement"})
				case "comments-disabled":
					accountAuthorityModule(t, s, "comments", false)
				case "parent-private":
					_, err = repository.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusPrivate)
				case "parent-deleted":
					var trashed content.Post
					trashed, err = repository.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusTrash)
					if err == nil {
						_, err = repository.BulkChangeStatus(t.Context(), content.BulkStatusChange{Delete: true, Status: content.StatusTrash, Items: []content.BulkMutation{{ID: post.ID, ExpectedVersion: trashed.Version}}})
					}
				case "flash-disabled":
					accountAuthorityModule(t, s, "flash", false)
				case "logout-after-header", "logout-after-state":
					err = manager.RevokeSessionContext(t.Context(), cookie.Value)
				case "cancel":
					cancel()
				}
				if err != nil {
					t.Error(err)
				}
			}
			if change == "logout-after-state" || change == "flash-disabled" {
				comments.afterState = after
			} else if strings.HasPrefix(change, "parent-") || change == "logout-after-header" {
				parents.afterHeaders = after
			} else {
				comments.afterList = after
			}
			s.repository, s.comments = parents, comments
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/social/own-content?kind=comments", nil).WithContext(ctx)
			r.AddCookie(cookie)
			func() {
				defer func() {
					if v := recover(); v != nil && v != http.ErrAbortHandler {
						panic(v)
					}
				}()
				handler.ServeHTTP(w, r)
			}()
			want := 409
			if change == "allowed" {
				want = 200
			}
			if change == "comments-disabled" {
				want = 404
			}
			if strings.HasPrefix(change, "logout") {
				want = 401
			}
			if !changed {
				t.Fatal("boundary not reached")
			}
			if change == "cancel" {
				if w.Body.Len() != 0 {
					t.Fatal("cancelled list exposed data")
				}
				return
			}
			if w.Code != want || change != "allowed" && (strings.Contains(w.Body.String(), "PRIVATE-") || strings.Contains(w.Body.String(), "private-parent-boundary")) {
				t.Fatal("comment boundary exposed stale data", change, w.Code, w.Body.String())
			}
		})
	}
}

func TestAccountContentAuthorityBatchReads(t *testing.T) {
	s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
	_, cookie, actor := createAndLoginTestUser(t, manager, owner, "batch-personal", "batch-personal@example.test", "Subscriber", "subscriber")
	post, err := repository.Create(t.Context(), content.PostInput{Title: "Parent", Slug: "batch-parent", Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for range 24 {
		if _, err := s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, UserID: actor.UserID, AuthorName: actor.Label(), AuthorEmail: "batch@example.test", ContentSource: "Own comment", Status: comment.StatusApproved}); err != nil {
			t.Fatal(err)
		}
	}
	parents := &accountAuthorityHeaders{Repository: repository}
	comments := &accountAuthorityStateComments{accountAuthorityComments: &accountAuthorityComments{Store: s.comments}}
	s.repository, s.comments = parents, comments
	w := contentAuthorityRequest(handler, http.MethodGet, "/api/v1/social/own-content?kind=comments", "", cookie, nil, nil)
	if w.Code != 200 || parents.reads != 3 || parents.fullReads != 0 || comments.reads != 2 || strings.Count(w.Body.String(), "batch-parent#comments") != 24 {
		t.Fatal("per-comment full read or lost items", w.Code, parents.reads, parents.fullReads, comments.reads)
	}
}

func TestAccountContentAuthorityPostTransferAndEditLinks(t *testing.T) {
	for _, change := range []string{"transfer", "permission", "allowed"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "personal-editor", "personal-editor@example.test", "Editor", "editor")
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "personal-editor-post", "PRIVATE-OWN-POST")
			parents := &accountAuthorityHeaders{Repository: repository}
			if change == "transfer" {
				parents.transferID = post.ID
			}
			if change == "permission" {
				parents.Repository = &contentAuthorityRepository{editorAuthorityRepository: &editorAuthorityRepository{Repository: repository}, afterList: func() { autosaveAuthorityRevoke(t, manager, owner, user, cookie, "permission") }}
			}
			s.repository = parents
			w := contentAuthorityRequest(handler, http.MethodGet, "/api/v1/social/own-content?kind=posts", "", cookie, nil, nil)
			want := 200
			if change == "transfer" {
				want = 403
			}
			if change == "permission" {
				want = 409
			}
			if w.Code != want || change != "allowed" && strings.Contains(w.Body.String(), "PRIVATE-OWN-POST") {
				t.Fatal("post authority failed", w.Code, w.Body.String())
			}
			if change == "permission" {
				w = contentAuthorityRequest(handler, http.MethodGet, "/api/v1/social/own-content?kind=posts", "", cookie, nil, nil)
				if w.Code != 200 || !strings.Contains(w.Body.String(), "PRIVATE-OWN-POST") || strings.Contains(w.Body.String(), "/admin/content/") {
					t.Fatal("subscriber retry lost own post", w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestAccountContentAuthorityEncoding(t *testing.T) {
	s, _, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	handler := s.sessionState(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := contentAuthorityMarshaler{after: func() {
			if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
				t.Error(err)
			}
		}}
		writePrivateJSON(w, r, 200, value, nil, func() error { _, err := s.ownContentPrincipal(r, owner.UserID); return err }, func(err error) { s.ownContentError(w, r, err) })
	}))
	w := contentAuthorityRequest(handler, http.MethodGet, "/api/v1/social/own-content?kind=posts", "", cookie, nil, nil)
	if w.Code != 401 || strings.Contains(w.Body.String(), "PRIVATE-") {
		t.Fatal(fmt.Sprintf("encoded content leaked: %d %s", w.Code, w.Body.String()))
	}
}
