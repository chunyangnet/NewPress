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

func TestPostgreSQLStructuredContent(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repo := NewPostRepository(pool)
	source := `<aside data-callout="tip"><p>结构化正文</p></aside><pre><code class="language-go">a &lt; b</code></pre><table><thead><tr><th scope="col">项目</th></tr></thead><tbody><tr><td>持久</td></tr></tbody></table>`
	input := content.PostInput{Title: "结构化草稿", Slug: fmt.Sprintf("blocks-%d", time.Now().UnixNano()), ContentSource: source}
	post, err := repo.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=$1`, post.ID)
	}()
	revisions, err := repo.ListRevisions(ctx, post.ID, 10)
	if err != nil || len(revisions) != 1 {
		t.Fatal("missing revision", err)
	}
	snapshot := post
	snapshot.Title = "未保存的内容块"
	_, err = repo.SaveAutosave(ctx, content.Autosave{PostID: post.ID, OwnerKey: "user:1", SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	reloaded := NewPostRepository(pool)
	recovered, err := reloaded.GetAutosave(ctx, post.ID, "user:1")
	if err != nil || recovered.Snapshot.ContentSource != source || recovered.Snapshot.ContentHTML != post.ContentHTML {
		t.Fatal("autosave changed", err)
	}
	loaded, err := reloaded.GetByID(ctx, post.ID)
	if err != nil || loaded.ContentSource != source || !strings.Contains(loaded.ContentHTML, `data-callout="tip"`) || !strings.Contains(loaded.ContentHTML, `class="language-go"`) {
		t.Fatal("stored block content changed", err)
	}
	input.ContentSource = "<p>替换正文</p>"
	updated, err := reloaded.Update(ctx, post.ID, content.Mutation{ExpectedVersion: post.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reloaded.RestoreRevision(ctx, post.ID, revisions[0].ID, content.Mutation{ExpectedVersion: updated.Version})
	if err != nil || restored.ContentSource != source || restored.ContentHTML != post.ContentHTML {
		t.Fatal("revision changed", err)
	}
}
