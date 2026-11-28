package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

func TestPostgreSQLPersonalContentProjections(t *testing.T) {
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
	posts := NewPostRepository(pool)
	comments := NewCommentRepository(pool)
	p, err := posts.Create(ctx, content.PostInput{Title: "Projection title", Slug: fmt.Sprintf("personal-projection-%d", time.Now().UnixNano())})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=$1`, p.ID)
	}()
	c, err := comments.Create(ctx, comment.CreateInput{PostID: p.ID, AuthorName: "Projection author", AuthorEmail: "projection@example.test", ContentSource: "Private projection text", Status: comment.StatusPending})
	if err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		full, err := posts.GetByID(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		headers, err := content.ReadPostHeaders(ctx, posts, []int64{p.ID, p.ID, 0})
		if err != nil || len(headers) != 1 {
			t.Fatal("headers", headers, err)
		}
		h := headers[p.ID]
		if h.ID != full.ID || h.AuthorID != full.AuthorID || h.Version != full.Version || h.Type != full.Type || h.Status != full.Status || h.Title != full.Title || h.Slug != full.Slug || !h.PublishedAt.Equal(full.PublishedAt) || !h.UpdatedAt.Equal(full.UpdatedAt) {
			t.Fatal("header differs", h, full)
		}
		states, err := comment.ReadStates(ctx, comments, []int64{c.ID, c.ID})
		if err != nil || len(states) != 1 || states[c.ID].UserID != c.UserID || states[c.ID].Version != c.Version || states[c.ID].Status != c.Status || states[c.ID].PostID != p.ID {
			t.Fatal("comment state differs", states, err)
		}
	}
	check()
	p, err = posts.ChangeStatus(ctx, p.ID, content.Mutation{ExpectedVersion: p.Version}, content.StatusPrivate)
	if err != nil {
		t.Fatal(err)
	}
	c, err = comments.ChangeStatus(ctx, c.ID, comment.Mutation{ExpectedVersion: c.Version}, comment.StatusSpam)
	if err != nil {
		t.Fatal(err)
	}
	check()
	if _, err = pool.Exec(ctx, `UPDATE np_posts SET author_id=NULL WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	check()
	if values, err := content.ReadPostHeaders(ctx, posts, nil); err != nil || len(values) != 0 {
		t.Fatal("empty headers", err)
	}
	if values, err := comment.ReadStates(ctx, comments, nil); err != nil || len(values) != 0 {
		t.Fatal("empty states", err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM np_comments WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if values, err := comment.ReadStates(ctx, comments, []int64{c.ID}); !errors.Is(err, comment.ErrNotFound) || values != nil {
		t.Fatal("missing state", values, err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM np_posts WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if values, err := content.ReadPostHeaders(ctx, posts, []int64{p.ID}); err != nil || len(values) != 0 {
		t.Fatal("missing header", values, err)
	}
	cancel()
	if values, err := content.ReadPostHeaders(ctx, posts, []int64{p.ID}); !errors.Is(err, context.Canceled) || values != nil {
		t.Fatal("cancelled header", values, err)
	}
	if values, err := comment.ReadStates(ctx, comments, []int64{c.ID}); !errors.Is(err, context.Canceled) || values != nil {
		t.Fatal("cancelled states", values, err)
	}
}
