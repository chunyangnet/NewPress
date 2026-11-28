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

type autosaveFallbackRepository struct{ Repository }

type autosaveObservedContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (ctx *autosaveObservedContext) Err() error {
	err := ctx.Context.Err()
	ctx.once.Do(func() { close(ctx.entered) })
	return err
}

func TestAutosaveStateTracksRecoveryWithoutPayload(t *testing.T) {
	for _, backend := range []string{"memory", "file", "fallback"} {
		t.Run(backend, func(t *testing.T) {
			var repository Repository = NewMemoryRepository(false)
			var path string
			if backend == "file" {
				path = filepath.Join(t.TempDir(), "content.json")
				file, err := NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
				repository = file
			}
			post, err := repository.Create(t.Context(), PostInput{Type: TypePost, Title: "State fixture", Slug: "state-fixture", AuthorID: 42, Actor: "editor"})
			if err != nil {
				t.Fatal(err)
			}
			input := Autosave{PostID: post.ID, OwnerKey: "user:42", SourceVersion: post.Version, Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)}
			first, err := repository.SaveAutosave(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			if backend == "file" {
				file, err := NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
				repository = file
			} else if backend == "fallback" {
				repository = autosaveFallbackRepository{repository}
			}
			check := func(expected Autosave) {
				t.Helper()
				state, err := ReadAutosaveState(t.Context(), repository, post.ID, input.OwnerKey)
				want := autosaveState(post, expected)
				if err != nil || state.PostID != want.PostID || state.AuthorID != want.AuthorID || state.Type != want.Type || state.PostVersion != want.PostVersion || state.SourceVersion != want.SourceVersion || !state.UpdatedAt.Equal(want.UpdatedAt) || !state.ExpiresAt.Equal(want.ExpiresAt) {
					t.Fatalf("state mismatch: %+v %v", state, err)
				}
			}
			check(first)
			if _, err = ReadAutosaveState(t.Context(), repository, post.ID, "user:43"); !errors.Is(err, ErrAutosaveNotFound) {
				t.Fatal("cross-owner state exposed", err)
			}
			input.Snapshot.Title = "Replacement"
			second, err := repository.SaveAutosave(t.Context(), input)
			if err != nil || !second.UpdatedAt.After(first.UpdatedAt) {
				t.Fatal("replacement identity did not advance", err)
			}
			check(second)
			if err = repository.DeleteAutosave(t.Context(), post.ID, input.OwnerKey); err != nil {
				t.Fatal(err)
			}
			if _, err = ReadAutosaveState(t.Context(), repository, post.ID, input.OwnerKey); !errors.Is(err, ErrAutosaveNotFound) {
				t.Fatal("deleted state remained", err)
			}
			if _, err = repository.SaveAutosave(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			if _, err = repository.ChangeStatus(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version, Actor: "editor"}, StatusPrivate); err != nil {
				t.Fatal(err)
			}
			if _, err = ReadAutosaveState(t.Context(), repository, post.ID, input.OwnerKey); !errors.Is(err, ErrAutosaveNotFound) {
				t.Fatal("canonical change retained stale recovery", err)
			}
		})
	}
}

func TestMemoryAutosaveCancellationAfterLockPreservesRecovery(t *testing.T) {
	for _, operation := range []string{"save", "get", "delete", "state"} {
		t.Run(operation, func(t *testing.T) {
			r := NewMemoryRepository(false)
			post, err := r.Create(t.Context(), PostInput{Title: "Cancel fixture", Slug: "cancel-fixture", Actor: "editor"})
			if err != nil {
				t.Fatal(err)
			}
			a, err := r.SaveAutosave(t.Context(), Autosave{PostID: post.ID, OwnerKey: "user:1", SourceVersion: post.Version, Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := &autosaveObservedContext{Context: parent, entered: make(chan struct{})}
			r.mu.Lock()
			done := make(chan error, 1)
			go func() {
				var err error
				switch operation {
				case "save":
					next := a
					next.Snapshot.Title = "Cancelled replacement"
					_, err = r.SaveAutosave(ctx, next)
				case "get":
					_, err = r.GetAutosave(ctx, post.ID, a.OwnerKey)
				case "delete":
					err = r.DeleteAutosave(ctx, post.ID, a.OwnerKey)
				case "state":
					_, err = r.GetAutosaveState(ctx, post.ID, a.OwnerKey)
				}
				done <- err
			}()
			select {
			case <-ctx.entered:
			case <-time.After(time.Second):
				// Older implementations never read the context. Release their
				// lock too so the regression reports a failure instead of hanging.
			}
			cancel()
			r.mu.Unlock()
			if err = <-done; !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled operation completed", err)
			}
			current, err := r.GetAutosave(t.Context(), post.ID, a.OwnerKey)
			if err != nil || !reflect.DeepEqual(current, a) {
				t.Fatal("cancelled operation changed recovery", err)
			}
		})
	}
}

func TestMemoryAutosaveStateExpiryAndConcurrentReplacement(t *testing.T) {
	r := NewMemoryRepository(false)
	post, err := r.Create(t.Context(), PostInput{Title: "State concurrency", Slug: "state-concurrency", Actor: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	input := Autosave{PostID: post.ID, OwnerKey: "user:1", SourceVersion: post.Version, Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)}
	if _, err = r.SaveAutosave(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				state, err := r.GetAutosaveState(t.Context(), post.ID, input.OwnerKey)
				if err != nil || state.SourceVersion != post.Version || state.UpdatedAt.IsZero() {
					t.Errorf("inconsistent state: %+v %v", state, err)
					return
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		input.Snapshot.Title = fmt.Sprintf("Recovery %d", i)
		if _, err = r.SaveAutosave(t.Context(), input); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	r.now = func() time.Time { return input.ExpiresAt }
	if _, err = r.GetAutosaveState(t.Context(), post.ID, input.OwnerKey); !errors.Is(err, ErrAutosaveNotFound) {
		t.Fatal("expired state returned", err)
	}
}

func BenchmarkAutosaveAuthorityRead(b *testing.B) {
	for _, size := range []int{0, 2048} {
		r := NewMemoryRepository(false)
		post, err := r.Create(b.Context(), PostInput{Title: "Read benchmark", Slug: "read-benchmark", Actor: "editor"})
		if err != nil {
			b.Fatal(err)
		}
		rows := make([]any, size)
		for i := range rows {
			rows[i] = map[string]any{"name": "fixture", "value": i}
		}
		snapshot := post
		snapshot.Meta = map[string]any{"rows": rows}
		if _, err = r.SaveAutosave(b.Context(), Autosave{PostID: post.ID, OwnerKey: "user:1", SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			b.Fatal(err)
		}
		for _, mode := range []string{"full-fallback", "state"} {
			var reader Repository = r
			if mode == "full-fallback" {
				reader = autosaveFallbackRepository{r}
			}
			b.Run(fmt.Sprintf("rows-%d/%s", size, mode), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					state, err := ReadAutosaveState(b.Context(), reader, post.ID, "user:1")
					if err != nil || state.PostVersion != post.Version {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
