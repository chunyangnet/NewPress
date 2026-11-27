package engagement

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestGuestMergeAtomicIdempotentAndPreservesCollections(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "engagement.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, Input{UserID: 1, TargetID: 1, Kind: Bookmark, Collection: "自己的收藏夹", Active: true}); err != nil {
		t.Fatal(err)
	}
	inputs := []Input{{UserID: 1, TargetID: 1, Kind: Bookmark, Collection: "本机收藏", Active: true}, {UserID: 1, TargetID: 2, Kind: Like, Active: true}}
	r, err := s.Merge(ctx, 1, inputs, true)
	if err != nil || r.Added != 1 || r.Existing != 1 {
		t.Fatal(r, err)
	}
	likes, _ := s.Entries(ctx, 1, Like)
	if len(likes) != 0 {
		t.Fatal("preview persisted")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.Merge(ctx, 1, inputs, false); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	r, err = s.Merge(ctx, 1, inputs, false)
	if err != nil || r.Added != 0 || r.Existing != 2 {
		t.Fatal(r, err)
	}
	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := reopened.Entries(ctx, 1, Bookmark)
	if len(saved) != 1 || saved[0].Collection != "自己的收藏夹" {
		t.Fatal(saved)
	}
	other, _ := reopened.Entries(ctx, 2, Like)
	if len(other) != 0 {
		t.Fatal("cross-account merge")
	}
	if _, err := s.Merge(ctx, 2, inputs, false); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "block")
	_ = os.WriteFile(blocked, []byte("file"), 0600)
	s.path = filepath.Join(blocked, "engagement.json")
	if _, err := s.Merge(ctx, 1, []Input{{UserID: 1, TargetID: 3, Kind: Like, Active: true}}, false); err == nil {
		t.Fatal("failed save accepted")
	}
	likes, _ = s.Entries(ctx, 1, Like)
	if len(likes) != 1 {
		t.Fatal("failed save partly applied")
	}
	s.path = ""
	for id := int64(2); id <= 1000; id++ {
		s.data.Entries = append(s.data.Entries, Entry{UserID: 1, TargetID: id, Kind: Bookmark})
	}
	_, err = s.Merge(ctx, 1, []Input{{UserID: 1, TargetID: 4, Kind: Like, Active: true}, {UserID: 1, TargetID: 1001, Kind: Bookmark, Active: true}}, false)
	if !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	likes, _ = s.Entries(ctx, 1, Like)
	if len(likes) != 1 {
		t.Fatal("capacity failure partly applied")
	}
}
