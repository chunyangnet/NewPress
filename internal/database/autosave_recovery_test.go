package database

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestPostgreSQLAutosaveRecoveryProjection(t *testing.T) {
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
	r := NewPostRepository(pool)
	post, err := r.Create(ctx, content.PostInput{Type: content.TypePage, Title: "原稿", Slug: fmt.Sprintf("autosave-projection-%d", time.Now().UnixNano())})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=$1`, post.ID)
	}()
	snapshot := post
	snapshot.Title, snapshot.Excerpt = "", ""
	snapshot.ContentSource = "  <p>待恢复正文</p><script>bad()</script>  "
	snapshot.ContentHTML = `<script>injected()</script>`
	saved, err := r.SaveAutosave(ctx, content.Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Snapshot.ContentSource != snapshot.ContentSource || saved.Snapshot.ContentHTML != "<p>待恢复正文</p>" || saved.Snapshot.Title != "" || saved.Snapshot.Excerpt != "" {
		t.Errorf("save trusted HTML or changed recovery fields: %#v", saved.Snapshot)
	}
	// Check older stored projections as well as new writes.
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE np_post_autosaves SET snapshot=$1 WHERE post_id=$2 AND owner_key='user:1'`, payload, post.ID); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetAutosave(ctx, post.ID, "user:1")
	if err != nil || got.Snapshot.ContentHTML != "<p>待恢复正文</p>" || got.Snapshot.ContentSource != snapshot.ContentSource || got.Snapshot.Title != "" || got.Snapshot.Excerpt != "" || !got.UpdatedAt.Equal(saved.UpdatedAt) {
		t.Errorf("load trusted HTML or changed recovery: %#v, %v", got, err)
	}
	current, err := r.GetByID(ctx, post.ID)
	revisions, revErr := r.ListRevisions(ctx, post.ID, 10)
	if err != nil || revErr != nil || current.Title != post.Title || current.Version != post.Version || len(revisions) != 1 {
		t.Error("autosave changed canonical content or revision history")
	}
}
