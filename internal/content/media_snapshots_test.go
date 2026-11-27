package content

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestMediaSnapshotsIncludeAllHistoryAndOnlyUnexpiredAutosaves(t *testing.T) {
	repository := NewMemoryRepository(false)
	now := time.Now().UTC()
	repository.now = func() time.Time { return now }
	input := PostInput{AuthorID: 17, Title: "Historical image", Slug: "history", CoverURL: "/media/1/history.png"}
	post, err := repository.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.CoverURL = ""
	for i := 0; i < 105; i++ {
		post, err = repository.Update(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version}, input)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range []struct {
		owner string
		ttl   time.Duration
	}{{"user:17", time.Hour}, {"user:18", time.Minute}} {
		if _, err := repository.SaveAutosave(t.Context(), Autosave{PostID: post.ID, OwnerKey: fixture.owner, SourceVersion: post.Version, Snapshot: post, ExpiresAt: now.Add(fixture.ttl)}); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(2 * time.Minute)
	counts := map[string]int{}
	historicalFound := false
	err = repository.WalkMediaSnapshots(t.Context(), func(snapshot MediaSnapshot) error {
		counts[snapshot.Source]++
		if snapshot.AuthorID != 17 || snapshot.PostID != post.ID {
			t.Fatalf("canonical identity = %#v", snapshot)
		}
		if snapshot.Source == MediaSourceRevision && snapshot.Version == 1 && snapshot.CoverURL == "/media/1/history.png" {
			historicalFound = true
		}
		if snapshot.Source == MediaSourceAutosave && snapshot.OwnerKey != "user:17" {
			t.Fatalf("expired snapshot = %#v", snapshot)
		}
		return nil
	})
	if err != nil || counts[MediaSourceRevision] != 106 || counts[MediaSourceAutosave] != 1 || counts[MediaSourceCurrent] != 1 || !historicalFound {
		t.Fatalf("scan = %#v found=%v err=%v", counts, historicalFound, err)
	}
	stop := errors.New("stop visit")
	if err := repository.WalkMediaSnapshots(t.Context(), func(MediaSnapshot) error { return stop }); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := repository.WalkMediaSnapshots(ctx, func(MediaSnapshot) error { t.Fatal("visited canceled scan"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestFileMediaSnapshotsSurviveRestart(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "content.json")
	repository, err := NewFileRepository(filename, false)
	if err != nil {
		t.Fatal(err)
	}
	post, err := repository.Create(t.Context(), PostInput{AuthorID: 7, Type: TypePage, Title: "Page", Slug: "page", CoverURL: "/media/1/page.png"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := post
	snapshot.ContentSource = `<img src="/media/2/autosave.png">`
	if _, err := repository.SaveAutosave(t.Context(), Autosave{PostID: post.ID, OwnerKey: "user:7", SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileRepository(filename, false)
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []MediaSnapshot
	if err := reopened.WalkMediaSnapshots(t.Context(), func(snapshot MediaSnapshot) error { snapshots = append(snapshots, snapshot); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 3 || snapshots[0].Source != MediaSourceCurrent || snapshots[1].Source != MediaSourceRevision || snapshots[2].OwnerKey != "user:7" || snapshots[2].ContentSource != snapshot.ContentSource {
		t.Fatalf("reopened snapshots = %#v", snapshots)
	}
}
