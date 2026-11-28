package content

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostStateTracksCurrentObject(t *testing.T) {
	for _, backend := range []string{"memory", "file", "fallback"} {
		t.Run(backend, func(t *testing.T) {
			var repository Repository = NewMemoryRepository(false)
			path := filepath.Join(t.TempDir(), "content.json")
			if backend == "file" {
				r, err := NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
				repository = r
			}
			post, err := repository.Create(t.Context(), PostInput{Title: "Projection fixture", Slug: "projection-fixture", AuthorID: 42, Type: TypePost})
			if err != nil {
				t.Fatal(err)
			}
			if backend == "file" {
				r, err := NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
				repository = r
			}
			if backend == "fallback" {
				repository = autosaveFallbackRepository{repository}
			}
			check := func() {
				t.Helper()
				state, err := ReadPostState(t.Context(), repository, post.ID)
				if err != nil || state != postState(post) {
					t.Fatalf("projection mismatch: %+v %v", state, err)
				}
			}
			check()
			input := PostInput{Title: post.Title, Slug: post.Slug, Type: TypePage, AuthorID: post.AuthorID, Status: StatusDraft}
			post, err = repository.Update(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version}, input)
			if err != nil {
				t.Fatal(err)
			}
			check()
			for _, id := range []int64{0, -1, post.ID + 100} {
				if _, err := ReadPostState(t.Context(), repository, id); !errors.Is(err, ErrNotFound) {
					t.Fatal("missing projection returned", id, err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := ReadPostState(ctx, repository, post.ID); !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled projection returned", err)
			}
			post, err = repository.ChangeStatus(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version}, StatusTrash)
			if err != nil {
				t.Fatal(err)
			}
			_, err = repository.BulkChangeStatus(t.Context(), BulkStatusChange{Delete: true, Status: StatusTrash, Items: []BulkMutation{{ID: post.ID, ExpectedVersion: post.Version}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ReadPostState(t.Context(), repository, post.ID); !errors.Is(err, ErrNotFound) {
				t.Fatal("deleted projection returned", err)
			}
		})
	}
}

func TestPostStateCancellationAfterLock(t *testing.T) {
	r := NewMemoryRepository(false)
	post, err := r.Create(t.Context(), PostInput{Title: "Cancellation projection", Slug: "cancel-projection"})
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &autosaveObservedContext{Context: parent, entered: make(chan struct{})}
	r.mu.Lock()
	done := make(chan error, 1)
	go func() { _, err := r.GetPostState(ctx, post.ID); done <- err }()
	<-ctx.entered
	cancel()
	r.mu.Unlock()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled lock waiter read object", err)
	}
}

type postStateCancelReader struct {
	Repository
	cancel context.CancelFunc
}

func (r postStateCancelReader) GetPostState(ctx context.Context, id int64) (PostState, error) {
	state, err := ReadPostState(ctx, r.Repository, id)
	r.cancel()
	return state, err
}

type postStateCancelFallback struct {
	Repository
	cancel context.CancelFunc
}

func (r postStateCancelFallback) GetByID(ctx context.Context, id int64) (Post, error) {
	p, err := r.Repository.GetByID(ctx, id)
	r.cancel()
	return p, err
}

func TestPostStateCancellationAfterStorage(t *testing.T) {
	for _, fast := range []bool{false, true} {
		t.Run(fmt.Sprint(fast), func(t *testing.T) {
			r := NewMemoryRepository(false)
			p, err := r.Create(t.Context(), PostInput{Title: "Storage boundary", Slug: "storage-boundary"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var reader Repository = postStateCancelFallback{r, cancel}
			if fast {
				reader = postStateCancelReader{r, cancel}
			}
			state, err := ReadPostState(ctx, reader, p.ID)
			if !errors.Is(err, context.Canceled) || state != (PostState{}) {
				t.Fatal("cancelled storage result exposed", state, err)
			}
		})
	}
}

func TestPostStateConcurrentMutation(t *testing.T) {
	r := NewMemoryRepository(false)
	p, err := r.Create(t.Context(), PostInput{Title: "Concurrent authority", Slug: "concurrent-authority", AuthorID: 42})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			for range 100 {
				state, err := r.GetPostState(t.Context(), id)
				if err != nil || state.ID != id || state.AuthorID != 42 || state.Type != TypePost || state.Version <= 0 {
					t.Error("inconsistent projection", state, err)
					return
				}
			}
		}(p.ID)
	}
	for range 20 {
		p, err = r.ChangeStatus(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, StatusDraft)
		if err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func BenchmarkPostAuthorityRead(b *testing.B) {
	for _, size := range []int{0, 2048} {
		r := NewMemoryRepository(false)
		p, err := r.Create(b.Context(), PostInput{Title: "Authority benchmark", Slug: "authority-benchmark"})
		if err != nil {
			b.Fatal(err)
		}
		rows := make([]any, size)
		for i := range rows {
			rows[i] = map[string]any{"name": "fixture", "value": i}
		}
		r.posts[0].Meta["rows"] = rows
		for _, mode := range []string{"full-fallback", "state"} {
			var reader Repository = r
			if mode == "full-fallback" {
				reader = autosaveFallbackRepository{r}
			}
			b.Run(fmt.Sprintf("rows-%d/%s", size, mode), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					state, err := ReadPostState(b.Context(), reader, p.ID)
					if err != nil || state.Version != p.Version {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
