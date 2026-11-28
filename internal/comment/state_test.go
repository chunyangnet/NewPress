package comment

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

type stateFallback struct{ Store }
type stateBoundaryStore struct {
	Store
	after func()
}

func (s stateBoundaryStore) GetCommentStates(ctx context.Context, ids []int64) (map[int64]State, error) {
	v, err := ReadStates(ctx, s.Store, ids)
	s.after()
	return v, err
}

type stateBoundaryFallback struct {
	Store
	after func()
}

func (s stateBoundaryFallback) Get(ctx context.Context, id int64) (Comment, error) {
	c, err := s.Store.Get(ctx, id)
	s.after()
	return c, err
}

type stateObservedContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *stateObservedContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.entered) })
	return err
}

func stateFixture(t *testing.T, path string) (*FileStore, Comment) {
	t.Helper()
	s, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Create(t.Context(), CreateInput{PostID: 42, UserID: 7, AuthorName: "Author", AuthorEmail: "author@example.test", ContentSource: "Private comment", Status: StatusPending})
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func TestCommentStatesTrackCurrentObjects(t *testing.T) {
	for _, kind := range []string{"memory", "file", "fallback"} {
		t.Run(kind, func(t *testing.T) {
			path := ""
			if kind == "file" {
				path = filepath.Join(t.TempDir(), "comments.json")
			}
			file, c := stateFixture(t, path)
			var s Store = file
			if kind == "file" {
				var err error
				s, err = NewFileStore(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "fallback" {
				s = stateFallback{s}
			}
			check := func() {
				t.Helper()
				values, err := ReadStates(t.Context(), s, []int64{c.ID, c.ID})
				if err != nil || len(values) != 1 || values[c.ID] != state(c) {
					t.Fatal("state mismatch", values, c, err)
				}
				delete(values, c.ID)
				again, _ := ReadStates(t.Context(), s, []int64{c.ID})
				if len(again) != 1 {
					t.Fatal("state aliases storage")
				}
			}
			check()
			var err error
			c, err = s.Edit(t.Context(), c.ID, Mutation{ExpectedVersion: c.Version, Actor: "editor"}, EditInput{AuthorName: "New name", AuthorEmail: c.AuthorEmail, ContentSource: "Updated text"})
			if err != nil {
				t.Fatal(err)
			}
			check()
			c, err = s.ChangeStatus(t.Context(), c.ID, Mutation{ExpectedVersion: c.Version}, StatusSpam)
			if err != nil {
				t.Fatal(err)
			}
			check()
			for _, id := range []int64{0, -1, 9999} {
				values, err := ReadStates(t.Context(), s, []int64{c.ID, id})
				if !errors.Is(err, ErrNotFound) || values != nil {
					t.Fatal("partial comment state exposed", values, err)
				}
			}
			values, err := ReadStates(t.Context(), s, nil)
			if err != nil || len(values) != 0 {
				t.Fatal("empty state read", err)
			}
			c, err = s.ChangeStatus(t.Context(), c.ID, Mutation{ExpectedVersion: c.Version}, StatusTrash)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Delete(t.Context(), c.ID, Mutation{ExpectedVersion: c.Version}); err != nil {
				t.Fatal(err)
			}
			values, err = ReadStates(t.Context(), s, []int64{c.ID})
			if !errors.Is(err, ErrNotFound) || values != nil {
				t.Fatal("deleted comment state exposed", values, err)
			}
		})
	}
}

func TestCommentStatesCancellation(t *testing.T) {
	for _, boundary := range []string{"entry", "lock", "batch", "fallback"} {
		t.Run(boundary, func(t *testing.T) {
			s, c := stateFixture(t, "")
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			var ctx context.Context = parent
			var store Store = s
			switch boundary {
			case "entry":
				cancel()
			case "lock":
				observed := &stateObservedContext{Context: parent, entered: make(chan struct{})}
				ctx = observed
				s.mu.Lock()
				go func() { <-observed.entered; cancel(); s.mu.Unlock() }()
			case "batch":
				store = stateBoundaryStore{s, cancel}
			case "fallback":
				store = stateBoundaryFallback{s, cancel}
			}
			values, err := ReadStates(ctx, store, []int64{c.ID})
			if !errors.Is(err, context.Canceled) || values != nil {
				t.Fatal("cancelled state returned", values, err)
			}
		})
	}
}

func TestCommentStatesConcurrentMutation(t *testing.T) {
	s, c := stateFixture(t, "")
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				v, err := ReadStates(t.Context(), s, []int64{1})
				if err != nil || v[1].ID != 1 || v[1].UserID != 7 || v[1].Version < 1 {
					t.Error("inconsistent comment state", v, err)
					return
				}
			}
		})
	}
	for range 20 {
		var err error
		c, err = s.ChangeStatus(t.Context(), c.ID, Mutation{ExpectedVersion: c.Version}, StatusApproved)
		if err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
