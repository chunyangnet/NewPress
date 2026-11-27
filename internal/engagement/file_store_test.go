package engagement

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAccountIsolationPersistenceAndIdempotentCounts(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "engagement.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []int64{1, 2} {
		for _, kind := range []string{Bookmark, Like, Follow} {
			input := Input{UserID: user, TargetID: 99, Kind: kind, Active: true}
			if kind == Bookmark {
				input.Collection = "开发笔记"
			}
			for i := 0; i < 3; i++ {
				if err := s.Set(ctx, input); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := s.Set(ctx, Input{UserID: 1, TargetID: 99, Kind: Bookmark, Active: true, Collection: "稍后阅读"}); err != nil {
		t.Fatal(err)
	}
	s, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{Bookmark, Like, Follow} {
		states, err := s.States(ctx, 1, kind, []int64{99, 100})
		if err != nil || states[0].Count != 2 || !states[0].Active || states[1].Count != 0 {
			t.Fatalf("states %v %v", states, err)
		}
		if err := s.Set(ctx, Input{UserID: 1, TargetID: 99, Kind: kind, Active: false}); err != nil {
			t.Fatal(err)
		}
		states, err = s.States(ctx, 1, kind, []int64{99})
		if err != nil || states[0].Count != 1 || states[0].Active {
			t.Fatalf("removed state %v %v", states, err)
		}
		other, _ := s.Entries(ctx, 2, kind)
		if len(other) != 1 {
			t.Fatal("other account changed")
		}
		other[0].TargetID = 888
		fresh, _ := s.Entries(ctx, 2, kind)
		if fresh[0].TargetID != 99 {
			t.Fatal("aliased stored entry")
		}
	}
	if err := s.Set(ctx, Input{UserID: 2, TargetID: 777, Kind: Like, Active: false}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentEngagementAndCapacity(t *testing.T) {
	s, _ := NewFileStore("")
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Go(func() {
			if err := s.Set(t.Context(), Input{UserID: 1, TargetID: 9, Kind: Like, Active: true}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	states, _ := s.States(t.Context(), 1, Like, []int64{9})
	if states[0].Count != 1 {
		t.Fatal("concurrent retry increased count")
	}
	for id := int64(2); id <= 501; id++ {
		if err := s.Set(t.Context(), Input{UserID: 1, TargetID: id, Kind: Follow, Active: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Set(t.Context(), Input{UserID: 1, TargetID: 502, Kind: Follow, Active: true}); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if err := s.Set(t.Context(), Input{UserID: 1, TargetID: 2, Kind: Follow, Active: true}); err != nil {
		t.Fatal("retry at capacity", err)
	}
	if err := s.Set(t.Context(), Input{UserID: 2, TargetID: 502, Kind: Follow, Active: true}); err != nil {
		t.Fatal("capacity leaked across accounts", err)
	}
}

func TestFailedWriteAndCancellationKeepPreviousState(t *testing.T) {
	s, _ := NewFileStore("")
	input := Input{UserID: 1, TargetID: 9, Kind: Bookmark, Active: true, Collection: "原收藏夹"}
	if err := s.Set(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(file, "engagement.json")
	input.Collection = "不能生效"
	if err := s.Set(t.Context(), input); err == nil {
		t.Fatal("expected failed persistence")
	}
	entries, _ := s.Entries(t.Context(), 1, Bookmark)
	if entries[0].Collection != "原收藏夹" {
		t.Fatal("failed write changed memory")
	}
	s.path = ""
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Set(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, _ = s.Entries(t.Context(), 1, Bookmark)
	if entries[0].Collection != "原收藏夹" {
		t.Fatal("canceled write changed memory")
	}
}

func TestInvalidEngagementDocumentsAndInput(t *testing.T) {
	for _, body := range []string{`{"version":1,"version":1,"entries":[]}`, `{"version":2,"entries":[]}`, `{"version":1,"entries":null}`, `{"version":1,"entries":[],"extra":0}`, `{"version":1,"entries":[]} {}`, `{"version":1,"entries":[{"user_id":1,"target_id":1,"kind":"follow","created_at":"2026-01-01T00:00:00Z"}]}`} {
		path := filepath.Join(t.TempDir(), "engagement.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileStore(path); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, input := range []Input{{UserID: 0, TargetID: 2, Kind: Like}, {UserID: 1, TargetID: 1, Kind: Follow}, {UserID: 1, TargetID: 2, Kind: "admin"}, {UserID: 1, TargetID: 2, Kind: Like, Collection: "x"}, {UserID: 1, TargetID: 2, Kind: Bookmark, Collection: "a\x00b"}} {
		if _, err := Normalize(input); err == nil {
			t.Fatalf("accepted %#v", input)
		}
	}
}
