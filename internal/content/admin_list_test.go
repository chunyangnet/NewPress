package content

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sync"
	"testing"
	"time"
)

func adminListFixture(count, rows int) *MemoryRepository {
	r := NewMemoryRepository(false)
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	for i := range count {
		meta := make([]any, rows)
		for j := range meta {
			meta[j] = map[string]any{"text": "nested value", "row": j}
		}
		p := Post{ID: int64(i + 1), AuthorID: int64(i % 4), Version: 1,
			Type: []string{TypePost, TypePage, TypeFlash}[i%3], Status: []string{StatusDraft, StatusPublish, StatusPrivate, StatusTrash, StatusFuture, StatusPending}[i%6],
			Title: fmt.Sprintf("Title %d 中文", i), Slug: fmt.Sprintf("slug-%d", i), Excerpt: "Excerpt", AuthorName: fmt.Sprintf("Author %d", i%3),
			Categories: []Term{{ID: 1, Name: "Alpha category"}}, Tags: []Term{{ID: 2, Name: "Beta tag"}},
			PublishedAt: now.Add(time.Duration(i%9) * time.Hour), UpdatedAt: now.Add(time.Duration(i%7) * time.Hour),
			Meta: map[string]any{"rows": meta}}
		if i%11 == 0 {
			p.PublishedAt = time.Time{}
		}
		r.posts = append(r.posts, p)
	}
	r.nextID = int64(count + 1)
	return r
}

func TestAdminListMatchesReference(t *testing.T) {
	r := adminListFixture(173, 2)
	random := rand.New(rand.NewPCG(17, 91))
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	for iteration := range 800 {
		q := AdminQuery{AuthorID: int64(random.IntN(5)),
			Type:   []string{"", TypePost, TypePage, TypeFlash}[random.IntN(4)],
			Status: []string{"", "all", StatusDraft, StatusPublish, StatusPrivate, StatusTrash, StatusFuture}[random.IntN(7)],
			Search: []string{"", "TITLE", "中文", "slug-1", "beta", "Alpha category", "excerpt", "Author 2", "absent"}[random.IntN(9)],
			Limit:  []int{0, 1, 20, 100, 200}[random.IntN(5)], Offset: []int{-1, 0, 1, 20, 100, 200, int(^uint(0) >> 1)}[random.IntN(7)], PublicationAscending: random.IntN(2) == 0}
		if random.IntN(3) == 0 {
			q.PublishedAfter = now
		}
		if random.IntN(3) == 0 {
			q.PublishedBefore = now.Add(5 * time.Hour)
		}
		want, wantErr := referenceAdminPage(r, q)
		got, err := r.ListAdmin(t.Context(), q)
		if err != nil || wantErr != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("query %d differs: %+v; got=%+v want=%+v err=%v/%v", iteration, q, got, want, err, wantErr)
		}
	}
	for _, q := range []AdminQuery{{Type: "unknown"}, {Status: "unknown"}, {AuthorID: -1}, {Search: "bad\x00query"}} {
		if _, err := r.ListAdmin(t.Context(), q); err == nil {
			t.Fatal("invalid query accepted", q)
		}
	}
}

func TestAdminListReturnsIndependentValues(t *testing.T) {
	r := adminListFixture(40, 1)
	q := AdminQuery{Limit: 2, Offset: 4}
	want, _ := referenceAdminPage(r, q)
	got, err := r.ListAdmin(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	got.Items[0].Categories[0].Name = "changed"
	got.Items[0].Tags[0].Name = "changed"
	got.Items[0].Meta["rows"].([]any)[0].(map[string]any)["text"] = "changed"
	got.StatusCounts[StatusDraft] = 1000
	again, err := r.ListAdmin(t.Context(), q)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatal("returned page aliases storage", err)
	}
}

func TestAdminListCancellation(t *testing.T) {
	for _, boundary := range []string{"entry", "lock", "scan"} {
		t.Run(boundary, func(t *testing.T) {
			r := adminListFixture(1000, 1)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var queryCtx context.Context = ctx
			if boundary == "entry" {
				cancel()
			}
			if boundary == "lock" {
				observed := &autosaveObservedContext{Context: ctx, entered: make(chan struct{})}
				queryCtx = observed
				r.mu.Lock()
				go func() { <-observed.entered; cancel(); r.mu.Unlock() }()
			}
			if boundary == "scan" {
				paused := newPausedSearchContext(ctx)
				queryCtx = paused
				go func() { <-paused.entered; cancel(); paused.release() }()
			}
			got, err := r.ListAdmin(queryCtx, AdminQuery{Limit: 20})
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, AdminList{}) {
				t.Fatal("cancelled page exposed", err)
			}
		})
	}
}

func TestAdminListConcurrentMutation(t *testing.T) {
	r := adminListFixture(100, 2)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 40 {
				list, err := r.ListAdmin(t.Context(), AdminQuery{Limit: 20})
				if err != nil || len(list.Items) != 20 || list.Total != 100 {
					t.Error("invalid concurrent page", err)
					return
				}
				list.Items[0].Meta["rows"].([]any)[0].(map[string]any)["text"] = "outside"
			}
		})
	}
	for range 20 {
		p, err := r.GetByID(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = r.ChangeStatus(t.Context(), 1, Mutation{ExpectedVersion: p.Version}, StatusDraft); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func BenchmarkAdminListPage(b *testing.B) {
	for _, size := range []int{100, 2000} {
		r := adminListFixture(size, 24)
		for _, offset := range []int{0, size / 2, size + 1} {
			b.Run(fmt.Sprintf("posts-%d/offset-%d", size, offset), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := r.ListAdmin(b.Context(), AdminQuery{Limit: 20, Offset: offset}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
