package content

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestPostStatesTracksCurrentObjects(t *testing.T) {
	for _, backend := range []string{"memory", "file", "fallback", "state-fallback"} {
		t.Run(backend, func(t *testing.T) {
			var r Repository = NewMemoryRepository(false)
			path := filepath.Join(t.TempDir(), "content.json")
			if backend == "file" {
				var err error
				r, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			var posts []Post
			for i := range 3 {
				p, err := r.Create(t.Context(), PostInput{Title: fmt.Sprintf("Batch %d", i), Slug: fmt.Sprintf("batch-%d", i), AuthorID: int64(i + 1)})
				if err != nil {
					t.Fatal(err)
				}
				posts = append(posts, p)
			}
			if backend == "file" {
				var err error
				r, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			if backend == "fallback" {
				r = autosaveFallbackRepository{r}
			}
			if backend == "state-fallback" {
				r = postStatesSingleFallback{r}
			}
			ids := []int64{posts[2].ID, posts[0].ID, posts[2].ID}
			check := func() {
				t.Helper()
				states, err := ReadPostStates(t.Context(), r, ids)
				if err != nil || len(states) != 2 {
					t.Fatal("batch projection", states, err)
				}
				for _, id := range ids {
					p, err := r.GetByID(t.Context(), id)
					if err != nil || states[id] != postState(p) {
						t.Fatal("state mismatch", states[id], p, err)
					}
				}
				delete(states, ids[0])
				again, err := ReadPostStates(t.Context(), r, ids)
				if err != nil || len(again) != 2 {
					t.Fatal("state map aliases repository", err)
				}
			}
			check()
			p := posts[0]
			var err error
			p, err = r.Update(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, PostInput{Title: p.Title, Slug: p.Slug, AuthorID: 12, Type: TypePage})
			if err != nil {
				t.Fatal(err)
			}
			check()
			for _, missing := range []int64{0, -1, 9999} {
				states, err := ReadPostStates(t.Context(), r, append(append([]int64(nil), ids...), missing))
				if !errors.Is(err, ErrNotFound) || states != nil {
					t.Fatal("partial batch returned", states, err)
				}
			}
			states, err := ReadPostStates(t.Context(), r, nil)
			if err != nil || len(states) != 0 {
				t.Fatal("empty batch", states, err)
			}
			p, err = r.ChangeStatus(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, StatusTrash)
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.BulkChangeStatus(t.Context(), BulkStatusChange{Delete: true, Status: StatusTrash, Items: []BulkMutation{{ID: p.ID, ExpectedVersion: p.Version}}})
			if err != nil {
				t.Fatal(err)
			}
			states, err = ReadPostStates(t.Context(), r, ids)
			if !errors.Is(err, ErrNotFound) || states != nil {
				t.Fatal("deleted state authorized", states, err)
			}
		})
	}
}

type postStatesSingleFallback struct{ Repository }

func (r postStatesSingleFallback) GetPostState(ctx context.Context, id int64) (PostState, error) {
	return ReadPostState(ctx, r.Repository, id)
}

type postStatesBoundaryReader struct {
	Repository
	after  func()
	result map[int64]PostState
	err    error
}

func (r postStatesBoundaryReader) GetPostStates(context.Context, []int64) (map[int64]PostState, error) {
	if r.after != nil {
		r.after()
	}
	return r.result, r.err
}

func TestPostStatesStorageBoundary(t *testing.T) {
	for _, kind := range []string{"cancel-batch", "cancel-fallback", "incomplete", "wrong-id", "error"} {
		t.Run(kind, func(t *testing.T) {
			r := adminListFixture(2, 1)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader := postStatesBoundaryReader{Repository: r, result: map[int64]PostState{1: postState(r.posts[0])}}
			var repo Repository = reader
			want := ErrNotFound
			switch kind {
			case "cancel-batch":
				reader.after = cancel
				repo = reader
				want = context.Canceled
			case "cancel-fallback":
				repo = postStateCancelFallback{r, cancel}
				want = context.Canceled
			case "wrong-id":
				reader.result[2] = PostState{ID: 3}
			case "error":
				reader.err = errors.New("storage unavailable")
				repo = reader
				want = reader.err
			}
			states, err := ReadPostStates(ctx, repo, []int64{1, 2})
			if !errors.Is(err, want) || states != nil {
				t.Fatal("invalid batch exposed", states, err)
			}
		})
	}
}

func TestPostStatesCancellationAfterLock(t *testing.T) {
	r := adminListFixture(100, 1)
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &autosaveObservedContext{Context: parent, entered: make(chan struct{})}
	r.mu.Lock()
	done := make(chan error, 1)
	go func() { _, err := r.GetPostStates(ctx, []int64{100, 1}); done <- err }()
	<-ctx.entered
	cancel()
	r.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled lock waiter returned", err)
	}
}

func TestPostStatesConcurrentMutation(t *testing.T) {
	r := adminListFixture(100, 1)
	ids := []int64{1, 2, 99}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				states, err := ReadPostStates(t.Context(), r, ids)
				if err != nil || len(states) != 3 || states[1].ID != 1 || states[1].Version < 1 {
					t.Error("inconsistent states", err)
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

func TestPostStatesTracksAuthorTransfer(t *testing.T) {
	r := adminListFixture(3, 1)
	before, err := ReadPostStates(t.Context(), r, []int64{1, 3})
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.posts[0].AuthorID = 42
	r.posts[0].Version++
	r.mu.Unlock()
	after, err := ReadPostStates(t.Context(), r, []int64{1, 3})
	if err != nil || before[1].AuthorID == 42 || after[1].AuthorID != 42 || after[1].Version != before[1].Version+1 || after[3] != before[3] {
		t.Fatal("stale author projection", before, after, err)
	}
}

func BenchmarkPostStatesPage(b *testing.B) {
	r := adminListFixture(5000, 4)
	ids := make([]int64, 100)
	for i := range ids {
		ids[i] = int64(4901 + i)
	}
	want, _ := ReadPostStates(b.Context(), r, ids)
	for _, mode := range []string{"single-reads", "batch"} {
		var repo Repository = r
		if mode == "single-reads" {
			repo = postStatesSingleFallback{r}
		}
		got, err := ReadPostStates(b.Context(), repo, ids)
		if err != nil || !reflect.DeepEqual(got, want) {
			b.Fatal("batch mismatch", err)
		}
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ReadPostStates(b.Context(), repo, ids); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
