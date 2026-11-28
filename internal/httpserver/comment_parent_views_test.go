package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/site"
)

// Preserved per-comment renderer for behavior comparison with the batch path.
func referenceCommentParentView(s *Server, r *http.Request, c comment.Comment) adminCommentItemView {
	v := adminCommentItemView{Comment: c, PostTitle: "内容 #" + strconv.FormatInt(c.PostID, 10),
		SourceFingerprint: shortCommentFingerprint(c.AuthorIPHash), CanDelete: c.Status == comment.StatusTrash,
		InspectorURL: "/admin/comments/" + strconv.FormatInt(c.ID, 10) + "/edit/"}
	v.StatusLabel, v.StatusTone = adminCommentStatusPresentation(c.Status)
	p, err := s.repository.GetByID(r.Context(), c.PostID)
	if err != nil {
		return v
	}
	v.PostTitle, v.PostType, v.PostStatus, v.postAuthorID = p.Title, p.Type, p.Status, p.AuthorID
	v.PostEditURL = "/admin/content/" + strconv.FormatInt(p.ID, 10) + "/edit/"
	if content.PublicAt(p, time.Now()) {
		v.PostPermalink = s.contentPath(p)
	}
	return v
}

func TestCommentParentViewsMatchReference(t *testing.T) {
	for _, kind := range []string{"memory", "file", "fallback"} {
		t.Run(kind, func(t *testing.T) {
			var repository content.Repository = content.NewMemoryRepository(false)
			if kind == "file" {
				var err error
				repository, err = content.NewFileRepository(filepath.Join(t.TempDir(), "content.json"), false)
				if err != nil {
					t.Fatal(err)
				}
			}
			var posts []content.Post
			for _, typ := range []string{content.TypePost, content.TypePage, content.TypeFlash} {
				for _, status := range []string{content.StatusDraft, content.StatusPublish, content.StatusFuture, content.StatusPrivate, content.StatusTrash, content.StatusPending} {
					published := time.Date(2020, 12, 31, 20, 30, 0, 0, time.UTC)
					if status == content.StatusFuture {
						published = time.Now().AddDate(1, 0, 0)
					}
					p, err := repository.Create(t.Context(), content.PostInput{Type: typ, Status: status, Slug: typ + "-" + status, Title: "Parent " + typ + status, ContentSource: "Parent body", AuthorID: 7, PublishedAt: published})
					if err != nil {
						t.Fatalf("create %s/%s: %v (%v)", typ, status, err, content.FieldErrors(err))
					}
					posts = append(posts, p)
				}
			}
			if kind == "fallback" {
				repository = &commentParentReadRepository{Repository: repository}
			}
			settings := site.DefaultSettings("Parent tests", "")
			settings.Identity.Timezone = "Asia/Shanghai"
			manager, err := site.NewManager("", settings)
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{repository: repository, siteSettings: manager}
			items := []comment.Comment{{ID: 9999, PostID: 9999, Status: comment.StatusPending}}
			for i := range 2 {
				for _, p := range posts {
					items = append(items, comment.Comment{ID: int64(len(items) + 1), PostID: p.ID, AuthorIPHash: strings.Repeat("a", 64), Status: []string{comment.StatusApproved, comment.StatusTrash}[i]})
				}
			}
			r := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/admin/comments", nil)
			for _, style := range []string{"", "id", "month", "date"} {
				snapshot := manager.Snapshot()
				snapshot.Settings.Writing.PostPermalink, snapshot.Settings.Writing.PagePermalink = style, "id"
				if _, fields, err := manager.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) > 0 {
					t.Fatal(fields, err)
				}
				got := s.adminCommentItems(r, items)
				want := make([]adminCommentItemView, len(items))
				for i, c := range items {
					want[i] = referenceCommentParentView(s, r, c)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatal("parent display differs from reference", style)
				}
			}
			// A request-local batch must not cache old parent titles or links.
			p := posts[1]
			_, err = repository.Update(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version}, content.PostInput{Type: p.Type, Status: content.StatusDraft, Title: "Updated parent", Slug: p.Slug, ContentSource: "Updated body"})
			if err != nil {
				t.Fatal(err)
			}
			got := s.adminCommentItem(r, comment.Comment{ID: 1, PostID: p.ID})
			if got.PostTitle != "Updated parent" || got.PostPermalink != "" {
				t.Fatal("parent metadata cached across requests", got)
			}
		})
	}
}

type commentParentFailedHeaders struct {
	*commentParentReadRepository
	calls int
}

func (r *commentParentFailedHeaders) GetPostHeaders(context.Context, []int64) (map[int64]content.PostHeader, error) {
	r.calls++
	return map[int64]content.PostHeader{1: {ID: 1, Title: "PARTIAL-UNTRUSTED-PARENT"}}, errors.New("header read failed")
}

func TestCommentParentViewsDiscardFailedBatch(t *testing.T) {
	repository := &commentParentFailedHeaders{commentParentReadRepository: &commentParentReadRepository{Repository: content.NewMemoryRepository(false)}}
	s := &Server{repository: repository}
	r := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/admin/comments", nil)
	if got := s.adminCommentItems(r, nil); len(got) != 0 || repository.calls != 0 || repository.fullReads != 0 {
		t.Fatal("empty page loaded parents")
	}
	values := s.adminCommentItems(r, []comment.Comment{{ID: 1, PostID: 1}, {ID: 2, PostID: 2}})
	if repository.calls != 1 || repository.fullReads != 0 {
		t.Fatal("failed batch caused per-row retries", repository.calls, repository.fullReads)
	}
	for _, v := range values {
		if v.PostTitle != "内容 #"+strconv.FormatInt(v.Comment.PostID, 10) || v.PostEditURL != "" || v.PostPermalink != "" {
			t.Fatal("failed batch exposed partial metadata", v)
		}
	}
}

func commentParentBenchmarkFixture(b *testing.B, rich bool) *Server {
	b.Helper()
	repository := content.NewMemoryRepository(false)
	var config *resources.Config
	if rich {
		config = &resources.Config{}
		for i := range 8 {
			resource := resources.Resource{ID: fmt.Sprintf("resource-%d", i), Title: "Article resource"}
			for j := range 2 {
				resource.Variants = append(resource.Variants, resources.Variant{ID: fmt.Sprintf("variant-%d", j), Name: "Reading material", Kind: "content", Body: strings.Repeat("<p>Resource content</p>", 30), Groups: []resources.Group{{Name: "Public", Conditions: []resources.Condition{{ID: "public", Kind: "public"}}}}})
			}
			config.Items = append(config.Items, resource)
		}
	}
	for i := range 4 {
		_, err := repository.Create(b.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Title: fmt.Sprintf("Parent %d", i), Slug: fmt.Sprintf("parent-%d", i), AuthorID: 7, ContentSource: strings.Repeat("<p>Article content</p>", 1000), PublishedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Resources: config})
		if err != nil {
			b.Fatal(err)
		}
	}
	settings := site.DefaultSettings("Benchmark", "")
	settings.Writing.PostPermalink = "date"
	manager, err := site.NewManager("", settings)
	if err != nil {
		b.Fatal(err)
	}
	return &Server{repository: repository, siteSettings: manager}
}

func BenchmarkAdminCommentParentViews(b *testing.B) {
	for _, rich := range []bool{false, true} {
		for _, count := range []int{24, 200} {
			for _, fallback := range []bool{false, true} {
				b.Run(fmt.Sprintf("rich=%v/count=%d/fallback=%v", rich, count, fallback), func(b *testing.B) {
					s := commentParentBenchmarkFixture(b, rich)
					if fallback {
						s.repository = &commentParentReadRepository{Repository: s.repository}
					}
					items := make([]comment.Comment, count)
					for i := range items {
						items[i] = comment.Comment{ID: int64(i + 1), PostID: int64(i%4 + 1), Status: comment.StatusPending, ContentSource: "Comment body"}
					}
					r := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/admin/comments", nil)
					b.ReportAllocs()
					for b.Loop() {
						views := s.adminCommentItems(r, items)
						if len(views) != len(items) || views[0].PostPermalink == "" {
							b.Fatal("parent metadata missing")
						}
					}
				})
			}
		}
	}
}
