package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

type commentParentReadRepository struct {
	content.Repository
	fullReads int
}

func (r *commentParentReadRepository) GetByID(ctx context.Context, id int64) (content.Post, error) {
	r.fullReads++
	return r.Repository.GetByID(ctx, id)
}

func (r *commentParentReadRepository) GetPostStates(ctx context.Context, ids []int64) (map[int64]content.PostState, error) {
	return content.ReadPostStates(ctx, r.Repository, ids)
}

type commentParentHeaderRepository struct {
	*commentParentReadRepository
	headerReads int
	ids         []int64
	after       func()
}

func (r *commentParentHeaderRepository) GetPostHeaders(ctx context.Context, ids []int64) (map[int64]content.PostHeader, error) {
	r.headerReads++
	r.ids = append([]int64(nil), ids...)
	result, err := content.ReadPostHeaders(ctx, r.Repository, ids)
	runAutosaveBoundary(&r.after)
	return result, err
}

func TestCommentParentReadsBatchAcrossHandlers(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, endpoint := range []string{"list-api", "list-page", "item-api", "editor"} {
			mode := "fallback"
			if native {
				mode = "headers"
			}
			t.Run(mode+"/"+endpoint, func(t *testing.T) {
				t.Parallel()
				s, handler, _, repository, cookie, actor := newPersistentAdminTestServer(t)
				posts := []content.Post{
					createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "comment-parent-a", "PRIVATE-PARENT-A"),
					createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "comment-parent-b", "PRIVATE-PARENT-B"),
				}
				var parent comment.Comment
				for i := range 8 {
					c, err := s.comments.Create(t.Context(), comment.CreateInput{PostID: posts[i%2].ID, Status: comment.StatusPending, AuthorName: "Reader", AuthorEmail: "reader@example.test", ContentSource: "PRIVATE-PARENT-COMMENT"})
					if err != nil {
						t.Fatal(err)
					}
					if i == 0 {
						parent = c
					}
				}
				reply, err := s.comments.Reply(t.Context(), comment.ReplyInput{ParentID: parent.ID, ExpectedParentVersion: parent.Version, UserID: actor.UserID, AuthorName: actor.Label(), AuthorEmail: actor.Email, ContentSource: "PRIVATE-PARENT-REPLY", Actor: actor.Username})
				if err != nil {
					t.Fatal(err)
				}
				full := &commentParentReadRepository{Repository: repository}
				headers := &commentParentHeaderRepository{commentParentReadRepository: full}
				s.repository = full
				if native {
					s.repository = headers
				}
				path, unique := "/api/v1/admin/comments?status=all&per_page=200", 2
				switch endpoint {
				case "list-page":
					path = "/admin/comments/?status=all"
				case "item-api":
					path, unique = "/api/v1/admin/comments/"+strconv.FormatInt(reply.ID, 10), 1
				case "editor":
					path, unique = "/admin/comments/"+strconv.FormatInt(reply.ID, 10)+"/edit/", 1
				}
				w := contentAuthorityRequest(handler, http.MethodGet, path, "", cookie, nil, nil)
				parentMarker := "PRIVATE-PARENT-A"
				if endpoint == "editor" {
					parentMarker = "/admin/content/" + strconv.FormatInt(posts[0].ID, 10) + "/edit/"
				}
				if w.Code != 200 || !strings.Contains(w.Body.String(), parentMarker) || !strings.Contains(w.Body.String(), "PRIVATE-PARENT-REPLY") {
					t.Fatal("comment display changed", endpoint, w.Code)
				}
				if native {
					if full.fullReads != 0 || headers.headerReads != 1 || len(headers.ids) != unique {
						t.Fatal("parent reads not batched", full.fullReads, headers.headerReads, headers.ids)
					}
				} else if full.fullReads != unique {
					t.Fatal("fallback repeated parent read", full.fullReads, unique)
				}
			})
		}
	}
}

func TestCommentParentReadsStillRefreshAuthority(t *testing.T) {
	for _, endpoint := range []string{"list-api", "editor"} {
		for _, change := range []string{"logout", "cancel"} {
			t.Run(endpoint+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, handler, manager, repository, cookie, actor := newPersistentAdminTestServer(t)
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "comment-parent-revoke", "PRIVATE-PARENT-REVOKE")
				c := commentAuthorityFixture(t, s, post.ID)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				changed := false
				s.repository = &commentParentHeaderRepository{commentParentReadRepository: &commentParentReadRepository{Repository: repository}, after: func() {
					changed = true
					if change == "cancel" {
						cancel()
					} else if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Fatal(err)
					}
				}}
				path, want := "/api/v1/admin/comments?status=all", 401
				if endpoint == "editor" {
					path, want = "/admin/comments/"+strconv.FormatInt(c.ID, 10)+"/edit/", 303
				}
				r := httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil).WithContext(ctx)
				r.AddCookie(cookie)
				w, aborted := commentAuthorityServe(handler, r)
				if !changed || strings.Contains(w.Body.String(), "PRIVATE-") || w.Header().Get("ETag") != "" {
					t.Fatal("parent projection skipped authority check", changed, w.Code)
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancelled parent read produced response", aborted, w.Code)
					}
				} else if aborted || w.Code != want {
					t.Fatal("revoked parent read response", aborted, w.Code, want)
				}
			})
		}
	}
}

type dashboardCountComments struct {
	comment.Store
	lists, gets, snapshots int
	after                  func()
	fail                   bool
}

func (s *dashboardCountComments) ListAdmin(ctx context.Context, q comment.Query) (comment.List, error) {
	s.lists++
	return s.Store.ListAdmin(ctx, q)
}

func (s *dashboardCountComments) Get(ctx context.Context, id int64) (comment.Comment, error) {
	s.gets++
	return s.Store.Get(ctx, id)
}

func (s *dashboardCountComments) Snapshot(ctx context.Context) (comment.Snapshot, error) {
	s.snapshots++
	v, err := s.Store.Snapshot(ctx)
	// The request's pending-count read follows adminView's general summary.
	if s.snapshots == 2 {
		runAutosaveBoundary(&s.after)
		if s.fail {
			return comment.Snapshot{}, errors.New("pending count unavailable")
		}
	}
	return v, err
}

func TestDashboardCommentCountsAvoidUnusedRecords(t *testing.T) {
	for _, change := range []string{"allowed", "error", "logout", "cancel", "module"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, handler, manager, repository, cookie, actor := newPersistentAdminTestServer(t)
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "dashboard-comment-count", "Count parent")
			for i := range 8 {
				status := comment.StatusApproved
				if i < 3 {
					status = comment.StatusPending
				}
				if _, err := s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, AuthorName: "Reader", AuthorEmail: "reader@example.test", ContentSource: "PRIVATE-COUNT-COMMENT", Status: status}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			changed := false
			counts := &dashboardCountComments{Store: s.comments, fail: change == "error", after: func() {
				changed = true
				switch change {
				case "logout":
					if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Fatal(err)
					}
				case "cancel":
					cancel()
				case "module":
					accountAuthorityModule(t, s, "comments", false)
				}
			}}
			parents := &commentParentHeaderRepository{commentParentReadRepository: &commentParentReadRepository{Repository: repository}}
			s.repository, s.comments = parents, counts
			r := httptest.NewRequest(http.MethodGet, "https://example.test/admin/", nil).WithContext(ctx)
			r.AddCookie(cookie)
			w, aborted := commentAuthorityServe(handler, r)
			if !changed || counts.snapshots != 2 || counts.lists != 0 || counts.gets != 0 || parents.fullReads != 0 || parents.headerReads != 0 || strings.Contains(w.Body.String(), "PRIVATE-COUNT-COMMENT") {
				t.Fatal("dashboard loaded unused comment data", changed, counts.snapshots, counts.lists, counts.gets, parents.fullReads, parents.headerReads)
			}
			if change == "cancel" {
				if !aborted || w.Body.Len() != 0 {
					t.Fatal("cancelled count rendered", aborted, w.Code)
				}
				return
			}
			want := 200
			if change == "logout" {
				want = 303
			} else if change == "module" {
				want = 403
			}
			if aborted || w.Code != want {
				t.Fatal("count authority response", aborted, w.Code, want)
			}
			if change == "allowed" && !strings.Contains(w.Body.String(), "待审核评论 <strong>3</strong>") || change == "error" && !strings.Contains(w.Body.String(), "待审核评论 <strong>读取失败</strong>") {
				t.Fatal("pending count or error display changed")
			}
		})
	}
}
