package content

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestPostHeadersTracksCurrentObjects(t *testing.T) {
	for _, kind := range []string{"memory", "file", "fallback"} {
		t.Run(kind, func(t *testing.T) {
			var r Repository = NewMemoryRepository(false)
			path := filepath.Join(t.TempDir(), "content.json")
			if kind == "file" {
				var err error
				r, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			var posts []Post
			for i := range 3 {
				p, err := r.Create(t.Context(), PostInput{Title: fmt.Sprintf("Header %d", i), Slug: fmt.Sprintf("header-%d", i), AuthorID: 42, ContentSource: "<p>Private body</p>"})
				if err != nil {
					t.Fatal(err)
				}
				posts = append(posts, p)
			}
			if kind == "file" {
				var err error
				r, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "fallback" {
				r = autosaveFallbackRepository{r}
			}
			ids := []int64{posts[2].ID, posts[0].ID, posts[2].ID, 9999, 0, -1}
			check := func() {
				t.Helper()
				values, err := ReadPostHeaders(t.Context(), r, ids)
				if err != nil || len(values) != 2 {
					t.Fatal("header read failed", values, err)
				}
				for _, p := range []Post{posts[0], posts[2]} {
					got := values[p.ID]
					want := postHeader(p)
					if !got.PublishedAt.Equal(want.PublishedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
						t.Fatal("header timestamps differ", got, want)
					}
					got.PublishedAt, got.UpdatedAt = want.PublishedAt, want.UpdatedAt
					if got != want {
						t.Fatal("stale header", got, p)
					}
					projection := got.Post()
					if projection.Meta != nil || projection.Categories != nil || projection.ContentSource != "" || projection.ContentHTML != "" {
						t.Fatal("heavy fields projected")
					}
				}
				delete(values, posts[0].ID)
				again, _ := ReadPostHeaders(t.Context(), r, ids)
				if len(again) != 2 {
					t.Fatal("returned map aliases storage")
				}
			}
			check()
			p := posts[0]
			var err error
			posts[0], err = r.Update(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, PostInput{Title: "Updated title", Slug: "new-header-slug", Type: TypePage, Status: StatusPublish, PublishedAt: time.Now().Add(-time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			check()
			p = posts[0]
			p, err = r.ChangeStatus(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, StatusTrash)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.BulkChangeStatus(t.Context(), BulkStatusChange{Delete: true, Status: StatusTrash, Items: []BulkMutation{{ID: p.ID, ExpectedVersion: p.Version}}}); err != nil {
				t.Fatal(err)
			}
			values, err := ReadPostHeaders(t.Context(), r, ids)
			if err != nil || len(values) != 1 || values[posts[2].ID].ID != posts[2].ID {
				t.Fatal("deleted header retained", values, err)
			}
			values, err = ReadPostHeaders(t.Context(), r, nil)
			if err != nil || len(values) != 0 {
				t.Fatal("empty headers", err)
			}
		})
	}
}

type postHeadersBoundaryReader struct {
	Repository
	after  func()
	result map[int64]PostHeader
	err    error
}

func (r postHeadersBoundaryReader) GetPostHeaders(context.Context, []int64) (map[int64]PostHeader, error) {
	if r.after != nil {
		r.after()
	}
	return r.result, r.err
}

func TestPostHeadersCancellationAndErrors(t *testing.T) {
	for _, kind := range []string{"entry", "lock", "batch-storage", "fallback-storage", "storage-error", "wrong-id"} {
		t.Run(kind, func(t *testing.T) {
			r := adminListFixture(3, 1)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var readCtx context.Context = ctx
			var repo Repository = r
			want := context.Canceled
			switch kind {
			case "entry":
				cancel()
			case "lock":
				observed := &autosaveObservedContext{Context: ctx, entered: make(chan struct{})}
				readCtx = observed
				r.mu.Lock()
				go func() { <-observed.entered; cancel(); r.mu.Unlock() }()
			case "batch-storage":
				repo = postHeadersBoundaryReader{Repository: r, after: cancel, result: map[int64]PostHeader{1: postHeader(r.posts[0])}}
			case "fallback-storage":
				repo = postStateCancelFallback{r, cancel}
			case "storage-error":
				want = errors.New("unavailable")
				repo = postHeadersBoundaryReader{Repository: r, err: want}
			case "wrong-id":
				want = ErrNotFound
				repo = postHeadersBoundaryReader{Repository: r, result: map[int64]PostHeader{1: {ID: 2}}}
			}
			values, err := ReadPostHeaders(readCtx, repo, []int64{1, 2})
			if !errors.Is(err, want) || values != nil {
				t.Fatal("invalid read exposed headers", values, err)
			}
		})
	}
}

func TestPostHeadersConcurrentMutation(t *testing.T) {
	r := adminListFixture(100, 3)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				values, err := ReadPostHeaders(t.Context(), r, []int64{1, 99})
				if err != nil || len(values) != 2 || values[1].Version < 1 {
					t.Error("inconsistent header", values, err)
					return
				}
			}
		})
	}
	for range 20 {
		p, _ := r.GetByID(t.Context(), 1)
		if _, err := r.ChangeStatus(t.Context(), 1, Mutation{ExpectedVersion: p.Version}, StatusDraft); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func BenchmarkCommentParentHeaders(b *testing.B) {
	r := adminListFixture(24, 512)
	ids := make([]int64, 24)
	for i := range ids {
		ids[i] = int64(i%4 + 1)
	}
	want, _ := ReadPostHeaders(b.Context(), r, ids)
	for _, mode := range []string{"full-per-comment", "full-deduplicated", "batch-headers"} {
		b.Run(mode, func(b *testing.B) {
			var repo Repository = r
			if mode == "full-deduplicated" {
				repo = autosaveFallbackRepository{r}
			}
			check, err := ReadPostHeaders(b.Context(), repo, ids)
			if err != nil || !reflect.DeepEqual(check, want) {
				b.Fatal("header mismatch", err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if mode == "full-per-comment" {
					for _, id := range ids {
						if _, err := r.GetByID(b.Context(), id); err != nil {
							b.Fatal(err)
						}
					}
				} else {
					if _, err := ReadPostHeaders(b.Context(), repo, ids); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
