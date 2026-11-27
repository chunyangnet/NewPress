package database

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestPostgreSQLMediaSnapshots(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	posts := NewPostRepository(pool)
	input := content.PostInput{Type: content.TypePage, Title: "Media history", Slug: fmt.Sprintf("media-history-%d", time.Now().UnixNano()), CoverURL: "/media/777/original.png"}
	post, err := posts.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=$1`, post.ID)
	}()
	input.CoverURL = ""
	for i := 0; i < 101; i++ {
		post, err = posts.Update(ctx, post.ID, content.Mutation{ExpectedVersion: post.Version}, input)
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot := post
	snapshot.CoverURL = "/media/778/autosave.png"
	for _, owner := range []string{"user:700", "user:701"} {
		if _, err := posts.SaveAutosave(ctx, content.Autosave{PostID: post.ID, OwnerKey: owner, SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE np_post_autosaves SET updated_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE post_id=$1 AND owner_key='user:701'`, post.ID); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	oldestFound := false
	if err := posts.WalkMediaSnapshots(ctx, func(snapshot content.MediaSnapshot) error {
		if snapshot.PostID != post.ID {
			return nil
		}
		counts[snapshot.Source]++
		if snapshot.AuthorID != 0 || snapshot.Type != content.TypePage {
			t.Fatalf("snapshot identity = %#v", snapshot)
		}
		if snapshot.Source == content.MediaSourceRevision && snapshot.Version == 1 && snapshot.CoverURL == "/media/777/original.png" {
			oldestFound = true
		}
		if snapshot.Source == content.MediaSourceAutosave && (snapshot.OwnerKey != "user:700" || snapshot.CoverURL != "/media/778/autosave.png") {
			t.Fatalf("autosave = %#v", snapshot)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if counts[content.MediaSourceCurrent] != 1 || counts[content.MediaSourceRevision] != 102 || counts[content.MediaSourceAutosave] != 1 || !oldestFound {
		t.Fatalf("snapshots = %#v found=%v", counts, oldestFound)
	}
}
