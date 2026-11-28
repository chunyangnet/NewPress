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

func TestPostgreSQLPostAuthorityState(t *testing.T) {
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
	p, err := r.Create(ctx, content.PostInput{Type: content.TypePage, Title: "Post authority state", Slug: fmt.Sprintf("post-state-%d", time.Now().UnixNano())})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=$1`, p.ID)
	}()
	check := func() {
		t.Helper()
		post, err := r.GetByID(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		state, err := content.ReadPostState(ctx, r, p.ID)
		if err != nil || state.ID != post.ID || state.AuthorID != post.AuthorID || state.Type != post.Type || state.Version != post.Version {
			t.Fatal("authority differs from canonical object", state, err)
		}
		states, err := content.ReadPostStates(ctx, r, []int64{p.ID, p.ID})
		if err != nil || len(states) != 1 || states[p.ID] != state {
			t.Fatal("batch authority differs from canonical object", states, err)
		}
		if states, err = content.ReadPostStates(ctx, r, nil); err != nil || len(states) != 0 {
			t.Fatal("empty batch authority", states, err)
		}
	}
	check() // Includes the system-author-to-zero mapping of canonical reads.
	if _, err = pool.Exec(ctx, `UPDATE np_posts SET author_id=NULL WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	check()
	p, err = r.ChangeStatus(ctx, p.ID, content.Mutation{ExpectedVersion: p.Version}, content.StatusPrivate)
	if err != nil {
		t.Fatal(err)
	}
	check()
	if _, err = pool.Exec(ctx, `DELETE FROM np_posts WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = content.ReadPostState(ctx, r, p.ID); !errors.Is(err, content.ErrNotFound) {
		t.Fatal("deleted authority returned", err)
	}
	if states, err := content.ReadPostStates(ctx, r, []int64{p.ID}); !errors.Is(err, content.ErrNotFound) || states != nil {
		t.Fatal("deleted batch authority returned", states, err)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if states, err := content.ReadPostStates(cancelled, r, []int64{p.ID}); !errors.Is(err, context.Canceled) || states != nil {
		t.Fatal("cancelled batch authority returned", states, err)
	}
}
