package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

func TestPostgreSQLWeChatStateAndImports(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repo := NewWeChatRepository(pool)
	original, err := repo.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Update(context.Background(), func(v *wechat.State) error { *v = original; return nil }); err != nil {
			t.Error(err)
		}
	}()
	if err = repo.Update(ctx, func(v *wechat.State) error { *v = wechat.NewState(); return nil }); err != nil {
		t.Fatal(err)
	}
	svc := &wechat.Service{Store: repo}
	cfg := wechat.Defaults()
	cfg.AppID = "wx0123456789abcdef"
	cfg.Secret = strings.Repeat("p", 32)
	cfg.Enabled = true
	cfg.AutoPublish = true
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := svc.SaveConfig(ctx, cfg, false); results <- e }()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for e := range results {
		if e == nil {
			successes++
		} else if errors.Is(e, wechat.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
	saved, err := repo.View(ctx)
	if err != nil || saved.Config.Secret != cfg.Secret {
		t.Fatal(err)
	}
	fail := errors.New("rollback")
	if err = repo.Update(ctx, func(v *wechat.State) error { v.Config.Author = "not committed"; return fail }); !errors.Is(err, fail) {
		t.Fatal(err)
	}
	saved, _ = repo.View(ctx)
	if saved.Config.Author != "" {
		t.Fatal("rollback lost")
	}
	posts := NewPostRepository(pool)
	prefix := fmt.Sprintf("wechat-it-%d", time.Now().UnixNano())
	defer pool.Exec(context.Background(), `DELETE FROM np_posts WHERE slug=$1`, prefix)
	batch := content.ImportBatch{Source: prefix, Format: "wechat", Items: []content.ImportEntry{{Key: "wechat-source", Digest: strings.Repeat("a", 64), MetadataDigest: strings.Repeat("b", 64), Input: content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "公众号导入", Slug: prefix, ContentSource: "<p>正文</p>"}}}}
	plan, err := posts.PreviewImport(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = posts.ApplyImport(ctx, batch, plan.Revision)
	if err != nil || plan.New != 1 {
		t.Fatal(plan, err)
	}
	postID := plan.Items[0].PostID
	plan, err = posts.PreviewImport(ctx, batch)
	if err != nil || plan.New != 0 || plan.Existing != 1 {
		t.Fatal("identity not retained", plan, err)
	}
	svc.Content = posts
	j, err := svc.Queue(ctx, postID, plan.Items[0].Version, "integration")
	if err != nil {
		t.Fatal(err)
	}
	saved, err = NewWeChatRepository(pool).View(ctx)
	if err != nil || len(saved.Jobs) != 1 || saved.Jobs[0].ID != j.ID {
		t.Fatal("job not persistent", err)
	}
	if err = svc.Action(ctx, j.ID, "cancel", j.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	saved, err = NewWeChatRepository(pool).View(ctx)
	if err != nil || saved.Jobs[0].Status != "skipped" {
		t.Fatal(saved, err)
	}
	j = saved.Jobs[0]
	if err = svc.Action(ctx, j.ID, "draft_only", j.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	saved, err = repo.View(ctx)
	if err != nil || saved.Jobs[0].AutoPublish {
		t.Fatal(saved, err)
	}
	j = saved.Jobs[0]
	if err = svc.Action(ctx, j.ID, "resume", j.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	saved, err = NewWeChatRepository(pool).View(ctx)
	if err != nil || saved.Jobs[0].Status != "queued" || saved.Jobs[0].AutoPublish {
		t.Fatal("recovery not persistent", saved, err)
	}
}
