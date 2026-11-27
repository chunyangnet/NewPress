package database

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func TestPostgreSQLContentImport(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	prefix := fmt.Sprintf("import-%d", time.Now().UnixNano())
	users := NewUserRepository(pool)
	if err = users.EnsureRoles(ctx, auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("Local import integration password")
	if err != nil {
		t.Fatal(err)
	}
	user, err := users.CreateUser(ctx, auth.UserMutation{Actor: "integration"}, auth.UserInput{Username: prefix, Email: prefix + "@example.test", DisplayName: "迁入作者", Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"author"}}, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool.Exec(cleanup, `DELETE FROM np_outbox WHERE topic='notification.post' AND (payload->>'post_id')::bigint IN(SELECT id FROM np_posts WHERE slug LIKE $1)`, prefix+"%")
		pool.Exec(cleanup, `DELETE FROM np_posts WHERE slug LIKE $1`, prefix+"%")
		pool.Exec(cleanup, `DELETE FROM np_users WHERE id=$1`, user.ID)
	}()
	batch := content.ImportBatch{Source: prefix, Format: "markdown", Items: []content.ImportEntry{{Key: "a.md", Digest: strings.Repeat("a", 64), Input: content.PostInput{Title: "Imported A", Slug: prefix + "-a"}, Categories: []string{"Imported category"}, OldPath: "/old/" + prefix + "/"}, {Key: "b.md", Digest: strings.Repeat("b", 64), Input: content.PostInput{Title: "Imported B", Slug: prefix + "-b"}}}}
	batch.Items[0].Input.AuthorID = user.ID
	batch.Items[0].Input.Status = content.StatusPublish
	batch.Items[0].Input.PublishedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	plan, err := r.PreviewImport(ctx, batch)
	if err != nil || plan.New != 2 {
		t.Fatal(plan, err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_content_imports WHERE source=$1`, prefix).Scan(&count); err != nil || count != 0 {
		t.Fatal("preview wrote mappings", count, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := r.ApplyImport(ctx, batch, plan.Revision); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	r = NewPostRepository(pool)
	again, err := r.PreviewImport(ctx, batch)
	if err != nil || again.Existing != 2 || again.New != 0 {
		t.Fatal(again, err)
	}
	p, err := r.GetByID(ctx, again.Items[0].PostID)
	if err != nil || len(p.Categories) != 1 {
		t.Fatal(p, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_outbox WHERE topic='notification.post' AND (payload->>'post_id')::bigint=$1`, p.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("historical import emitted notification", count, err)
	}
	p, err = r.Update(ctx, p.ID, content.Mutation{ExpectedVersion: p.Version}, content.PostInput{Title: "Edited", Slug: p.Slug, Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ApplyImport(ctx, batch, plan.Revision); err != nil {
		t.Fatal(err)
	}
	current, _ := r.GetByID(ctx, p.ID)
	if current.Title != "Edited" {
		t.Fatal("edited target overwritten")
	}
	// WeChat mappings persist the additional source metadata fingerprint. A
	// changed excerpt/cover is reported without overwriting the local target.
	metaSource := prefix + "-wechat"
	meta := content.ImportBatch{Source: metaSource, Format: "wechat", Items: []content.ImportEntry{{Key: "metadata", Digest: strings.Repeat("e", 64), MetadataDigest: strings.Repeat("f", 64), Input: content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "Metadata article", Slug: prefix + "-metadata", Excerpt: "source excerpt", CoverURL: "/media/source-cover.jpg", AuthorID: user.ID}}}}
	metaPlan, err := r.PreviewImport(ctx, meta)
	if err != nil || metaPlan.New != 1 {
		t.Fatal(metaPlan, err)
	}
	if _, err = r.ApplyImport(ctx, meta, metaPlan.Revision); err != nil {
		t.Fatal(err)
	}
	meta.Items[0].MetadataDigest = strings.Repeat("0", 64)
	metaPlan, err = r.PreviewImport(ctx, meta)
	if err != nil || metaPlan.Items[0].Action != "changed" || !strings.Contains(metaPlan.Items[0].Message, "摘要或封面") {
		t.Fatal(metaPlan, err)
	}
	if _, err = r.ApplyImport(ctx, meta, metaPlan.Revision); err != nil {
		t.Fatal(err)
	}
	// A WeChat import must supply its source metadata fingerprint.
	legacy := content.ImportBatch{Source: prefix + "-legacy", Format: "wechat", Items: []content.ImportEntry{{Key: "legacy", Digest: strings.Repeat("1", 64), Input: content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "Legacy metadata", Slug: prefix + "-legacy", AuthorID: user.ID}}}}
	legacyPlan, err := r.PreviewImport(ctx, legacy)
	if len(content.FieldErrors(err)) == 0 {
		t.Fatal("accepted missing metadata fingerprint", legacyPlan, err)
	}
	if _, err = r.ApplyImport(ctx, legacy, ""); len(content.FieldErrors(err)) == 0 {
		t.Fatal("applied missing metadata fingerprint", err)
	}
	legacy.Items[0].MetadataDigest = strings.Repeat("2", 64)
	legacyPlan, err = r.PreviewImport(ctx, legacy)
	if err != nil || legacyPlan.New != 1 {
		t.Fatal(legacyPlan, err)
	}
	if _, err = r.ApplyImport(ctx, legacy, legacyPlan.Revision); err != nil {
		t.Fatal(err)
	}
	target, err := r.ResolveRedirect(ctx, batch.Items[0].OldPath)
	if err != nil || target != content.PublicPath(p) {
		t.Fatal(target, err)
	}
	// The second row has an absent author. The first post, terms, revision and
	// mapping must roll back even though its own insert was valid.
	failed := content.ImportBatch{Source: prefix + "-failed", Format: "markdown", Items: []content.ImportEntry{{Key: "1", Digest: strings.Repeat("c", 64), Input: content.PostInput{Title: "Rollback first", Slug: prefix + "-rollback"}}, {Key: "2", Digest: strings.Repeat("d", 64), Input: content.PostInput{Title: "Bad author", Slug: prefix + "-bad", AuthorID: 9223372036854775806}}}}
	preview, err := r.PreviewImport(ctx, failed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ApplyImport(ctx, failed, preview.Revision); err == nil {
		t.Fatal("invalid author succeeded")
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_posts WHERE slug=$1`, prefix+"-rollback").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial batch persisted", count, err)
	}
}
