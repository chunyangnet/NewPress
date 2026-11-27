package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/mediausage"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/theme"
)

// Media remains local in PostgreSQL deployments. Exercise the actual guarded
// PostgreSQL repository, including revisions, rather than a second image store.
func TestPostgreSQLImageDerivativeReferences(t *testing.T) {
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
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	library, err := media.NewManager(filepath.Join(root, "media"))
	if err != nil {
		t.Fatal(err)
	}
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 32, 16))); err != nil {
		t.Fatal(err)
	}
	item, err := library.Upload(ctx, &imageBytes, media.UploadInput{Filename: "postgres.png"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	item, err = library.ProcessImage(ctx, item.ID, item.Version, media.ImageInput{FocalX: 75, FocalY: 25})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := site.NewManager("", site.DefaultSettings("Images", "Integration"))
	if err != nil {
		t.Fatal(err)
	}
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel")
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	menus, err := navigation.NewFileStore("", false)
	if err != nil {
		t.Fatal(err)
	}
	posts := NewPostRepository(pool)
	usage := mediausage.New(library, posts, menus, settings, themes, "https://example.test")
	input := content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "派生图引用集成", Slug: fmt.Sprintf("image-derivative-%d", time.Now().UnixNano()), ContentSource: `<img src="` + item.VariantURL("small", "") + `">`}
	post, err := usage.Content().Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		pool.Exec(cleanup, `DELETE FROM np_outbox WHERE topic='notification.post' AND (payload->>'post_id')::bigint=$1`, post.ID)
		pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=$1`, post.ID)
	}()
	if err := usage.Trash(ctx, item.ID, item.Version); !errors.Is(err, mediausage.ErrInUse) {
		t.Fatalf("current derivative reference = %v", err)
	}
	input.ContentSource = "<p>图片已从当前正文移除。</p>"
	if _, err := usage.Content().Update(ctx, post.ID, content.Mutation{ExpectedVersion: post.Version}, input); err != nil {
		t.Fatal(err)
	}
	if err := usage.Trash(ctx, item.ID, item.Version); !errors.Is(err, mediausage.ErrInUse) {
		t.Fatalf("revision derivative reference = %v", err)
	}
	reloaded, err := media.NewManager(filepath.Join(root, "media"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reloaded.Get(item.ID)
	if err != nil || loaded.Image != item.Image {
		t.Fatal("local image recipe restart", err)
	}
}
