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

type dashboardAuthorityRepository struct {
	*contentAuthorityRepository
	afterCreate func()
	conflict    bool
	creates     int
}

func (r *dashboardAuthorityRepository) Create(ctx context.Context, input content.PostInput) (content.Post, error) {
	r.creates++
	if r.conflict && r.creates == 1 {
		runAutosaveBoundary(&r.afterCreate)
		return content.Post{}, content.ErrSlugConflict
	}
	p, err := r.Repository.Create(ctx, input)
	runAutosaveBoundary(&r.afterCreate)
	return p, err
}

type accountAuthorityComments struct {
	comment.Store
	afterList, afterSnapshot func()
	skipSnapshots            int
}

func (s *accountAuthorityComments) ListAdmin(ctx context.Context, q comment.Query) (comment.List, error) {
	v, err := s.Store.ListAdmin(ctx, q)
	runAutosaveBoundary(&s.afterList)
	return v, err
}

func (s *accountAuthorityComments) Snapshot(ctx context.Context) (comment.Snapshot, error) {
	v, err := s.Store.Snapshot(ctx)
	if s.skipSnapshots > 0 {
		s.skipSnapshots--
	} else {
		runAutosaveBoundary(&s.afterSnapshot)
	}
	return v, err
}

func TestDashboardAuthorityQuickDraft(t *testing.T) {
	for _, boundary := range []string{"body", "invalid-body", "conflict", "committed"} {
		for _, change := range []string{"logout", "logout-with-fallback", "permission", "cancel", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "quick-authority", "quick-authority@example.test", "Editor", "editor")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				changed := false
				after := func() {
					changed = true
					autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
					if change == "cancel" {
						cancel()
					}
				}
				wrapped := &dashboardAuthorityRepository{contentAuthorityRepository: &contentAuthorityRepository{editorAuthorityRepository: &editorAuthorityRepository{Repository: repository}}, conflict: boundary == "conflict"}
				if boundary == "conflict" || boundary == "committed" {
					wrapped.afterCreate = after
				}
				s.repository = wrapped
				form := quickDraftValues(actor.CSRFToken, "Draft authority", "", "PRIVATE-QUICK-DRAFT")
				if boundary == "invalid-body" {
					form.Set("title", "")
				}
				r := httptest.NewRequest(http.MethodPost, "https://example.test/admin/quick-draft/", strings.NewReader(form.Encode())).WithContext(ctx)
				r.AddCookie(cookie)
				if change == "logout-with-fallback" {
					r.AddCookie(ownerCookie)
				}
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("Origin", "https://example.test")
				if strings.HasSuffix(boundary, "body") {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
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
				list, err := repository.ListAdmin(t.Context(), content.AdminQuery{})
				wantTotal := 0
				if boundary == "committed" || change == "allowed" && boundary != "invalid-body" {
					wantTotal = 1
				}
				wantCalls := wantTotal
				if boundary == "conflict" {
					wantCalls++
				}
				if !changed || err != nil || list.Total != wantTotal || wrapped.creates != wantCalls {
					t.Errorf("quick draft crossed boundary: changed=%v calls=%d/%d total=%d/%d err=%v", changed, wrapped.creates, wantCalls, list.Total, wantTotal, err)
				}
				if change == "cancel" {
					if w.Body.Len() != 0 || w.Header().Get("Location") != "" {
						t.Fatal("cancelled request returned result")
					}
					return
				}
				wantStatus := 303
				if change == "permission" {
					wantStatus = 403
				}
				if change == "allowed" && boundary == "invalid-body" {
					wantStatus = 422
				}
				if w.Code != wantStatus {
					t.Fatal("wrong authority response", w.Code, wantStatus, w.Body.String())
				}
				if change != "allowed" && (strings.Contains(w.Body.String(), "PRIVATE-QUICK-DRAFT") || strings.Contains(w.Header().Get("Location"), "quick-draft-created")) {
					t.Fatal("revoked request exposed outcome", w.Code)
				}
			})
		}
	}
}

func TestDashboardAuthorityRead(t *testing.T) {
	for _, boundary := range []string{"content", "comments", "render"} {
		for _, change := range []string{"logout", "logout-with-fallback", "permission", "scope", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
				user, cookie, _ := createAndLoginTestUser(t, manager, owner, "dashboard-reader", "dashboard-reader@example.test", "Editor", "editor")
				post := createOwnedTestPost(t, repository, owner.UserID, owner.Label(), "dashboard-private", "PRIVATE-DASHBOARD-CONTENT")
				if _, err := s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, AuthorName: "Private author", AuthorEmail: "private@example.test", ContentSource: "PRIVATE-DASHBOARD-COMMENT", Status: comment.StatusPending}); err != nil {
					t.Fatal(err)
				}
				changed := false
				after := func() {
					changed = true
					autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
					if change == "scope" {
						contentAuthorityNarrow(t, manager, owner, user)
					}
				}
				wrapped := &contentAuthorityRepository{editorAuthorityRepository: &editorAuthorityRepository{Repository: repository}}
				if boundary == "content" {
					wrapped.afterList = after
				}
				if boundary == "render" {
					wrapped.afterTerms = after
				}
				if boundary == "comments" {
					// adminView reads a general summary before the request's pending count.
					s.comments = &accountAuthorityComments{Store: s.comments, afterSnapshot: after, skipSnapshots: 1}
				}
				s.repository = wrapped
				w := contentAuthorityRequest(handler, http.MethodGet, "/admin/", "", cookie, nil, func(r *http.Request) {
					if change == "logout-with-fallback" {
						r.AddCookie(ownerCookie)
					}
				})
				want := 200
				if strings.HasPrefix(change, "logout") {
					want = 303
				}
				if change == "permission" || change == "scope" {
					want = 403
				}
				if !changed || w.Code != want || change != "allowed" && strings.Contains(w.Body.String(), "PRIVATE-DASHBOARD") {
					t.Fatal("revoked dashboard returned", boundary, change, w.Code, changed)
				}
			})
		}
	}
}

func TestAccountContentAuthorityRead(t *testing.T) {
	for _, kind := range []string{"posts", "comments"} {
		for _, change := range []string{"logout", "logout-with-fallback", "suspension", "allowed"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "personal-reader", "personal-reader@example.test", "Reader", "subscriber")
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "personal-private", "PRIVATE-PERSONAL-CONTENT")
				if _, err := s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, UserID: actor.UserID, AuthorName: actor.Label(), AuthorEmail: "personal@example.test", ContentSource: "PRIVATE-PERSONAL-COMMENT", Status: comment.StatusPending}); err != nil {
					t.Fatal(err)
				}
				changed := false
				after := func() { changed = true; autosaveAuthorityRevoke(t, manager, owner, user, cookie, change) }
				if kind == "posts" {
					s.repository = &contentAuthorityRepository{editorAuthorityRepository: &editorAuthorityRepository{Repository: repository}, afterList: after}
				} else {
					s.comments = &accountAuthorityComments{Store: s.comments, afterList: after}
				}
				w := contentAuthorityRequest(handler, http.MethodGet, "/api/v1/social/own-content?kind="+kind, "", cookie, nil, func(r *http.Request) {
					if change == "logout-with-fallback" {
						r.AddCookie(ownerCookie)
					}
				})
				want := 401
				if change == "allowed" {
					want = 200
				}
				if !changed || w.Code != want || change != "allowed" && strings.Contains(w.Body.String(), "PRIVATE-PERSONAL") {
					t.Fatal("revoked personal list exposed", w.Code, changed, w.Body.String())
				}
				if change == "allowed" && !strings.Contains(w.Body.String(), "PRIVATE-PERSONAL") {
					t.Fatal("subscriber lost own content")
				}
			})
		}
	}
}

func TestAccountContentAuthorityWithdrawnParent(t *testing.T) {
	s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
	_, cookie, actor := createAndLoginTestUser(t, manager, owner, "parent-reader", "parent-reader@example.test", "Reader", "subscriber")
	post, err := repository.Create(t.Context(), content.PostInput{Title: "WITHDRAWN-PARENT-PRIVATE-TITLE", Slug: "withdrawn-parent-private-slug", Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour), AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, UserID: actor.UserID, AuthorName: actor.Label(), AuthorEmail: "personal@example.test", ContentSource: "Own text", Status: comment.StatusApproved}); err != nil {
		t.Fatal(err)
	}
	changed := false
	s.repository = &editorAuthorityRepository{Repository: repository, afterPost: func() {
		changed = true
		if _, err := repository.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusPrivate); err != nil {
			t.Error(err)
		}
	}}
	w := contentAuthorityRequest(handler, http.MethodGet, "/api/v1/social/own-content?kind=comments", "", cookie, nil, nil)
	if !changed || strings.Contains(w.Body.String(), post.Title) || strings.Contains(w.Body.String(), post.Slug) {
		t.Fatal("withdrawn parent details exposed", w.Code, w.Body.String())
	}
	if w.Code != 200 && w.Code != 409 {
		t.Fatal(fmt.Sprint("unexpected response ", w.Code))
	}
}
