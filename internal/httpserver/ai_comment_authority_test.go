package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

const aiCommentAuthorityDraft = "PRIVATE-AI-COMMENT-DRAFT"

func aiCommentAuthorityRequest(s *Server, c comment.Comment, cookie *http.Cookie, p auth.Principal, ctx context.Context, before func(*http.Request)) (*httptest.ResponseRecorder, bool) {
	r := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/admin/comments/"+strconv.FormatInt(c.ID, 10)+"/reply-draft", strings.NewReader("{}")).WithContext(ctx)
	r.AddCookie(cookie)
	for k, v := range authenticatedAPIHeaders(p) {
		r.Header.Set(k, v)
	}
	if before != nil {
		before(r)
	}
	return commentAuthorityServe(s.routes(), r)
}

func TestAICommentAuthoritySessionBoundaries(t *testing.T) {
	for _, boundary := range []string{"body", "comment", "post", "upstream"} {
		for _, change := range []string{"logout", "logout-with-fallback", "permission", "module", "cancel", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, manager, repository, fallback, owner := newPersistentAdminTestServer(t)
				enableWritingFixture(t, s)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "ai-comment-editor", "ai-comment-editor@example.test", "Editor", "editor")
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "ai-comment-authority", "PRIVATE-AI-PARENT")
				c := commentAuthorityFixture(t, s, post.ID)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				changed := false
				after := func() {
					changed = true
					switch change {
					case "module":
						accountAuthorityModule(t, s, "comments", false)
					case "cancel":
						cancel()
					default:
						autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
					}
				}
				wrapped := &commentAuthorityStore{Store: s.comments}
				s.comments = wrapped
				if boundary == "comment" {
					wrapped.afterGet = after
				}
				if boundary == "post" {
					s.repository = &editorAuthorityRepository{Repository: repository, afterPost: after}
				}
				calls := 0
				setAIResponse(s, 200, "text/event-stream", aiFrame(aiCommentAuthorityDraft, "stop")+"data: [DONE]\n\n", func(*http.Request) {
					calls++
					if boundary == "upstream" {
						after()
					}
				})
				w, aborted := aiCommentAuthorityRequest(s, c, cookie, actor, ctx, func(r *http.Request) {
					if change == "logout-with-fallback" {
						r.AddCookie(fallback)
					}
					if boundary == "body" {
						r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
					}
				})
				wantCalls := 0
				if boundary == "upstream" || change == "allowed" {
					wantCalls = 1
				}
				if !changed || calls != wantCalls || wrapped.mutations != 0 {
					t.Errorf("revoked context reached model or draft wrote comment: changed=%v calls=%d want=%d mutations=%d", changed, calls, wantCalls, wrapped.mutations)
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancelled draft returned output", aborted, w.Code)
					}
					return
				}
				want := 403
				if strings.HasPrefix(change, "logout") {
					want = 401
				} else if change == "module" {
					want = 404
				} else if change == "allowed" {
					want = 200
				}
				if aborted || w.Code != want || strings.Contains(w.Body.String(), aiCommentAuthorityDraft) != (change == "allowed") || strings.Contains(w.Body.String(), "fixture-only-key") {
					t.Fatal("draft authority response", aborted, w.Code, want)
				}
				if !s.aiState.acquire(actor.UserID) {
					t.Fatal("draft leaked generation slot")
				}
				s.aiState.release(actor.UserID)
			})
		}
	}
}

func TestAICommentAuthorityContextChanges(t *testing.T) {
	for _, boundary := range []string{"post", "upstream"} {
		for _, change := range []string{"comment-edit", "comment-trash", "comment-delete", "post-edit", "post-private", "post-delete", "ownership"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
				enableWritingFixture(t, s)
				_, _, _, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilityCommentsModerate, auth.CapabilityContentWriteOwn})
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "ai-context-changes", "PRIVATE-AI-PARENT")
				if change == "post-private" {
					var err error
					post, err = repository.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusPublish)
					if err != nil {
						t.Fatal(err)
					}
				}
				c := commentAuthorityFixture(t, s, post.ID)
				changed := false
				transferred := false
				after := func() {
					changed = true
					var err error
					switch change {
					case "comment-edit":
						_, err = s.comments.Edit(t.Context(), c.ID, comment.Mutation{ExpectedVersion: c.Version, Actor: actor.Username}, comment.EditInput{AuthorName: c.AuthorName, AuthorEmail: c.AuthorEmail, ContentSource: "Replacement comment"})
					case "comment-trash", "comment-delete":
						var current comment.Comment
						current, err = s.comments.ChangeStatus(t.Context(), c.ID, comment.Mutation{ExpectedVersion: c.Version}, comment.StatusTrash)
						if err == nil && change == "comment-delete" {
							err = s.comments.Delete(t.Context(), c.ID, comment.Mutation{ExpectedVersion: current.Version})
						}
					case "post-private":
						_, err = repository.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusPrivate)
					case "post-delete":
						var current content.Post
						current, err = repository.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusTrash)
						if err == nil {
							_, err = repository.BulkChangeStatus(t.Context(), content.BulkStatusChange{Delete: true, Status: content.StatusTrash, Items: []content.BulkMutation{{ID: post.ID, ExpectedVersion: current.Version}}})
						}
					case "ownership":
						transferred = true
					default:
						input := content.PostInput{Type: post.Type, Status: post.Status, Title: post.Title, Slug: post.Slug, ContentSource: post.ContentSource, AuthorID: post.AuthorID, AuthorName: post.AuthorName}
						input.ContentSource = "Replacement article"
						_, err = repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, input)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if boundary == "post" {
					s.repository = &editorAuthorityRepository{Repository: repository, afterPost: after}
				}
				if change == "ownership" {
					wrapped := &aiCommentAuthorityPostScope{Repository: repository, transferred: &transferred}
					if boundary == "post" {
						wrapped.afterPost = after
					}
					s.repository = wrapped
				}
				calls := 0
				setAIResponse(s, 200, "text/event-stream", aiFrame(aiCommentAuthorityDraft, "stop")+"data: [DONE]\n\n", func(*http.Request) {
					calls++
					if boundary == "upstream" {
						after()
					}
				})
				w, aborted := aiCommentAuthorityRequest(s, c, cookie, actor, t.Context(), nil)
				wantCalls, want := 0, 409
				if boundary == "upstream" {
					wantCalls = 1
				}
				if change == "ownership" {
					want = 403
				}
				if !changed || aborted || calls != wantCalls || w.Code != want || strings.Contains(w.Body.String(), aiCommentAuthorityDraft) {
					t.Fatal("stale context used", changed, aborted, calls, wantCalls, w.Code, want)
				}
			})
		}
	}
}

// Ordinary edits deliberately preserve authorship. An adapter models an
// ownership change independently of version updates and editor permissions.
type aiCommentAuthorityPostScope struct {
	content.Repository
	transferred *bool
	afterPost   func()
}

func (r *aiCommentAuthorityPostScope) GetByID(ctx context.Context, id int64) (content.Post, error) {
	p, err := r.Repository.GetByID(ctx, id)
	runAutosaveBoundary(&r.afterPost)
	return p, err
}

func (r *aiCommentAuthorityPostScope) GetPostHeaders(ctx context.Context, ids []int64) (map[int64]content.PostHeader, error) {
	headers, err := content.ReadPostHeaders(ctx, r.Repository, ids)
	if *r.transferred {
		for id, h := range headers {
			h.AuthorID++
			headers[id] = h
		}
	}
	return headers, err
}
