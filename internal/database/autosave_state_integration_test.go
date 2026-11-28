package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestPostgreSQLAutosaveAuthorityState(t *testing.T) {
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
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	p, err := r.Create(ctx, content.PostInput{Type: content.TypePage, Title: "Autosave state", Slug: fmt.Sprintf("autosave-state-%d", time.Now().UnixNano())})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=$1`, p.ID)
	}()
	a, err := r.SaveAutosave(ctx, content.Autosave{PostID: p.ID, OwnerKey: "user:1", SourceVersion: p.Version, Snapshot: p, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	state, err := content.ReadAutosaveState(ctx, r, p.ID, a.OwnerKey)
	if err != nil || state.PostID != p.ID || state.AuthorID != p.AuthorID || state.Type != p.Type || state.PostVersion != p.Version || state.SourceVersion != a.SourceVersion || !state.UpdatedAt.Equal(a.UpdatedAt) || !state.ExpiresAt.Equal(a.ExpiresAt) {
		t.Fatalf("authority projection differs: %+v %v", state, err)
	}
	if _, err = r.GetAutosaveState(ctx, p.ID, "user:2"); !errors.Is(err, content.ErrAutosaveNotFound) {
		t.Fatal("cross-owner projection returned", err)
	}
	// Authority reads must not load or normalize snapshot payloads.
	if _, err = pool.Exec(ctx, `UPDATE np_post_autosaves SET snapshot='{}'::jsonb WHERE post_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetAutosaveState(ctx, p.ID, a.OwnerKey); err != nil {
		t.Fatal("projection depended on private snapshot payload", err)
	}
	if _, err = r.GetAutosave(ctx, p.ID, a.OwnerKey); err == nil {
		t.Fatal("corrupt snapshot unexpectedly accepted")
	}
	if _, err = pool.Exec(ctx, `UPDATE np_post_autosaves SET updated_at=now()-interval '1 hour', expires_at=now()-interval '1 second' WHERE post_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetAutosaveState(ctx, p.ID, a.OwnerKey); !errors.Is(err, content.ErrAutosaveNotFound) {
		t.Fatal("expired projection returned", err)
	}
}
