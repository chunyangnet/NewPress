package httpserver

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/database"
)

func TestPostgreSQLFlashPublicationTime(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	admin, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("np_flash_time_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	s.repository = database.NewPostRepository(pool)
	setPublicationTestTimezone(t, s, "America/New_York")
	post, err := s.repository.Create(ctx, content.PostInput{Type: content.TypeFlash, Title: "Database flash", ContentSource: "Database body",
		Slug: "pg-flash-time", Status: content.StatusPublish, PublishedAt: publicationTestInstant(t, "2026-11-01T06:30:43.987654Z")})
	if err != nil {
		t.Fatal(err)
	}
	original := post.PublishedAt
	body := flashPublicationBody(owner, post, "save", "2026-11-01T01:30")
	body.PublishedTimezone = "America/New_York"
	body.Title = "Edited database flash"
	response := flashPublicationRequest(t, handler, cookie, owner, "pg-flash-save-time", body)
	if response.Code != 200 {
		t.Fatalf("database save: %d %s", response.Code, response.Body.String())
	}
	saved, err := s.repository.GetByID(ctx, post.ID)
	if err != nil || !saved.PublishedAt.Equal(original) || saved.Version != post.Version+1 || saved.Title != body.Title {
		t.Fatalf("database save changed the instant: %#v %v", saved, err)
	}
	setPublicationTestTimezone(t, s, "Asia/Shanghai")
	response = flashPublicationRequest(t, handler, cookie, owner, "pg-flash-save-time", body)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"replayed":true`) {
		t.Fatalf("database receipt after timezone change: %d %s", response.Code, response.Body.String())
	}
	body.Version = saved.Version
	response = flashPublicationRequest(t, handler, cookie, owner, "pg-flash-stale-timezone", body)
	if response.Code != 422 {
		t.Fatalf("stale timezone accepted: %d %s", response.Code, response.Body.String())
	}
	for _, action := range []string{"unpublish", "trash", "restore"} {
		body = flashPublicationBody(owner, saved, action, "unfinished")
		response = flashPublicationRequest(t, handler, cookie, owner, "pg-flash-state-"+action, body)
		if response.Code != 200 {
			t.Fatalf("database state change: %d %s", response.Code, response.Body.String())
		}
		saved, err = s.repository.GetByID(ctx, post.ID)
		if err != nil || !saved.PublishedAt.Equal(original) {
			t.Fatalf("database state change lost time: %#v %v", saved, err)
		}
	}
	before := time.Now().Truncate(time.Microsecond)
	body = flashPublicationBody(owner, saved, "publish", "")
	response = flashPublicationRequest(t, handler, cookie, owner, "pg-flash-publish-now", body)
	if response.Code != 200 {
		t.Fatalf("database publish now: %d %s", response.Code, response.Body.String())
	}
	saved, err = s.repository.GetByID(ctx, post.ID)
	if err != nil || saved.Status != content.StatusPublish || saved.PublishedAt.Before(before) || saved.PublishedAt.After(time.Now()) {
		t.Fatalf("database publish now retained the old time: %#v %v", saved, err)
	}
}
