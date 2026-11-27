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

func TestPostgreSQLContentMetadataNumbersAndIsolation(t *testing.T) {
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
	input := content.PostInput{Type: content.TypePage, Title: "精确元数据", Slug: fmt.Sprintf("metadata-exact-%d", time.Now().UnixNano())}
	post, err := r.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=$1`, post.ID)
	}()
	const number = "9007199254740993"
	const fraction = "0.1234567890123456789"
	if _, err := pool.Exec(ctx, `UPDATE np_posts SET metadata=metadata || '{"integer":9007199254740993,"decimal":0.1234567890123456789}'::jsonb WHERE id=$1`, post.ID); err != nil {
		t.Fatal(err)
	}
	check := func(meta map[string]any) {
		t.Helper()
		if meta["integer"] != json.Number(number) || meta["decimal"] != json.Number(fraction) {
			t.Errorf("database metadata lost precision: %#v", meta)
		}
	}
	post, err = r.GetByID(ctx, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	check(post.Meta)
	input.Title = "第二版"
	post, err = r.Update(ctx, post.ID, content.Mutation{ExpectedVersion: post.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	check(post.Meta)
	revisions, err := r.ListRevisions(ctx, post.ID, 10)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("revisions: %d %v", len(revisions), err)
	}
	check(revisions[0].Snapshot.Meta)
	revision, err := r.GetRevision(ctx, post.ID, revisions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	check(revision.Snapshot.Meta)
	post, err = r.RestoreRevision(ctx, post.ID, revision.ID, content.Mutation{ExpectedVersion: post.Version})
	if err != nil {
		t.Fatal(err)
	}
	check(post.Meta)
	post.Meta["nested"] = []any{map[string]any{"name": "one"}}
	saved, err := r.SaveAutosave(ctx, content.Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	check(saved.Snapshot.Meta)
	post.Meta["nested"].([]any)[0].(map[string]any)["name"] = "input changed"
	saved.Snapshot.Meta["nested"].([]any)[0].(map[string]any)["name"] = "result changed"
	got, err := r.GetAutosave(ctx, post.ID, "user:1")
	if err != nil {
		t.Fatal(err)
	}
	check(got.Snapshot.Meta)
	if got.Snapshot.Meta["nested"].([]any)[0].(map[string]any)["name"] != "one" {
		t.Error("caller changed the database recovery metadata")
	}
}
