package writingtemplate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type templateFaultStorage struct {
	MemoryStorage
	readErr  error
	writeErr error
	reads    int
}

func (s *templateFaultStorage) Read(ctx context.Context) ([]byte, error) {
	s.reads++
	if s.readErr != nil {
		return nil, s.readErr
	}
	return s.MemoryStorage.Read(ctx)
}

func (s *templateFaultStorage) Write(ctx context.Context, expected [sha256.Size]byte, raw []byte) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	return s.MemoryStorage.Write(ctx, expected, raw)
}

func TestWarmReadsDoNotHideStorageChangesOrFailures(t *testing.T) {
	store := &templateFaultStorage{}
	left, err := New(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	first, create := newTemplate(t, left, 1)
	original, _ := store.MemoryStorage.Read(t.Context())
	right, err := New(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		run  func(context.Context) error
	}{
		{"catalog", func(ctx context.Context) error { _, err := right.Catalog(ctx); return err }},
		{"list", func(ctx context.Context) error { _, err := right.List(ctx, true, 10, 0); return err }},
		{"get", func(ctx context.Context) error { _, err := right.Get(ctx, first.ID); return err }},
		{"resolve", func(ctx context.Context) error { _, err := right.Resolve(ctx, first.ID, 1); return err }},
		{"export", func(ctx context.Context) error { _, err := right.Export(ctx, first.ID, 1); return err }},
		{"replay", func(ctx context.Context) error { _, err := right.Apply(ctx, create); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			before := store.reads
			if err := check.run(t.Context()); err != nil || store.reads != before+1 {
				t.Fatal("warm request skipped storage read", err, store.reads-before)
			}
			unavailable := errors.New("storage unavailable")
			store.readErr = unavailable
			if err := check.run(t.Context()); !errors.Is(err, unavailable) {
				t.Fatal("read error used stale snapshot", err)
			}
			store.readErr = nil
			store.raw = bytes.Replace(original, []byte(`"schema":1`), []byte(`"schema":2`), 1)
			if err := check.run(t.Context()); !errors.Is(err, ErrCorrupt) {
				t.Fatal("changed same-sized bytes used stale snapshot", err)
			}
			store.raw = bytes.Clone(original)
			if err := check.run(t.Context()); err != nil {
				t.Fatal("restored bytes remain unavailable", err)
			}
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			if err := check.run(cancelled); !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled warm read succeeded", err)
			}
		})
	}
	if _, err = left.Apply(t.Context(), templateCommand("archive", first.ID, 1, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err = right.Resolve(t.Context(), first.ID, 1); !errors.Is(err, ErrArchived) {
		t.Fatal("another service's archive hidden", err)
	}
	if catalog, err := right.Catalog(t.Context()); err != nil || len(catalog) != 0 {
		t.Fatal("archived item remained in catalog", err)
	}
	// A legitimate restore can move backwards; equality with newly fetched
	// bytes, not monotonic in-process versions, controls the reusable snapshot.
	store.raw = bytes.Clone(original)
	if _, err = right.Resolve(t.Context(), first.ID, 1); err != nil {
		t.Fatal("restored old version hidden", err)
	}
	store.raw = nil
	if catalog, err := right.Catalog(t.Context()); err != nil || len(catalog) != 0 {
		t.Fatal("absent storage served old records", err)
	}
}

func TestReturnedTemplatesAndFailedWritesCannotChangeWarmSnapshot(t *testing.T) {
	store := &templateFaultStorage{}
	s, err := New(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	first, create := newTemplate(t, s, 1)
	original, _ := store.MemoryStorage.Read(t.Context())
	item, _ := s.Get(t.Context(), first.ID)
	item.Revisions[0].Definition.Title = "caller mutation"
	item.Revisions[0].Source.Kind = "invalid"
	item.Revisions[0].Version = 99
	items, _ := s.Catalog(t.Context())
	items[0].Instruction = "caller mutation"
	page, _ := s.List(t.Context(), true, 10, 0)
	page.Items[0].Title = "caller mutation"
	if got, err := s.Get(t.Context(), first.ID); err != nil || got.Revisions[0].Definition != *create.Definition || got.Revisions[0].Version != 1 || got.Revisions[0].Source.Kind != "manual" {
		t.Fatal("returned values modified validated history", got, err)
	}
	command := templateCommand("archive", first.ID, 1, 2)
	for _, failure := range []error{errors.New("injected storage write error"), ErrConflict} {
		store.writeErr = failure
		if _, err = s.Apply(t.Context(), command); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		if got, err := s.Get(t.Context(), first.ID); err != nil || len(got.Revisions) != 1 || got.Revisions[0].Archived {
			t.Fatal("failed write leaked speculative history", got, err)
		}
		if _, err = s.Resolve(t.Context(), first.ID, 1); err != nil {
			t.Fatal("failed archive hid active template", err)
		}
	}
	after, _ := store.MemoryStorage.Read(t.Context())
	if !bytes.Equal(original, after) {
		t.Fatal("failure changed storage")
	}
	store.writeErr = nil
	if result, err := s.Apply(t.Context(), command); err != nil || result.Replayed || result.Version != 2 {
		t.Fatal("failed attempt installed a receipt", result, err)
	}
	if result, err := s.Apply(t.Context(), command); err != nil || !result.Replayed || result.Version != 2 {
		t.Fatal("committed replay changed", result, err)
	}
}

func TestWarmFileReadChecksBytesAndPrimaryPresence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writing-templates.json")
	store, err := NewFileStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := newTemplate(t, s, 1)
	if _, err = s.Resolve(t.Context(), first.ID, 1); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	bad := bytes.Replace(raw, []byte(`"schema":1`), []byte(`"schema":2`), 1)
	if err = os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(path, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve(t.Context(), first.ID, 1); !errors.Is(err, ErrCorrupt) {
		t.Fatal("same size and mtime hid corrupt bytes", err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve(t.Context(), first.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Catalog(t.Context()); !errors.Is(err, ErrCorrupt) {
		t.Fatal("missing primary used cache", err)
	}
}

func TestConcurrentReadCopiesAndHistoryWrites(t *testing.T) {
	s, err := New(t.Context(), &MemoryStorage{})
	if err != nil {
		t.Fatal(err)
	}
	first, create := newTemplate(t, s, 1)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 50; i++ {
				item, err := s.Get(t.Context(), first.ID)
				if err != nil || item.Revisions[0].Definition != *create.Definition {
					t.Error("concurrent reader saw modified original history", err)
					return
				}
				item.Revisions[0].Definition.Title = "independent reader"
				item.Revisions[0].Version = 999
			}
		}()
	}
	close(start)
	for n := 2; n <= 30; n++ {
		definition := *create.Definition
		definition.Title = fmt.Sprintf("version %d", n)
		command := templateCommand("save", first.ID, int64(n-1), n)
		command.Definition = &definition
		if result, err := s.Apply(t.Context(), command); err != nil || result.Version != int64(n) {
			t.Error("concurrent read damaged write", result, err)
			break
		}
	}
	wg.Wait()
	item, err := s.Get(t.Context(), first.ID)
	if err != nil || len(item.Revisions) != 30 || item.Revisions[0].Definition != *create.Definition {
		t.Fatal("history damaged", err)
	}
}
