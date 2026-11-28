package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

func TestPostgreSQLApprovedCounts(t *testing.T) {
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
	posts := NewPostRepository(pool)
	comments := NewCommentRepository(pool)
	p, err := posts.Create(ctx, content.PostInput{Title: "Comment counts", Slug: fmt.Sprintf("comment-counts-%d", time.Now().UnixNano())})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, "DELETE FROM np_posts WHERE id=$1", p.ID)
	}()
	for _, status := range []string{comment.StatusApproved, comment.StatusApproved, comment.StatusPending, comment.StatusSpam, comment.StatusTrash} {
		if _, err := comments.Create(ctx, comment.CreateInput{PostID: p.ID, AuthorName: "Counts", AuthorEmail: "counts@example.test", ContentSource: "Comment body", Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := comment.ReadApprovedCounts(ctx, comments, []int64{p.ID, p.ID, 1 << 60})
	if err != nil || !reflect.DeepEqual(got, map[int64]int{p.ID: 2, 1 << 60: 0}) {
		t.Fatal("wrong grouped count", got, err)
	}
	_, total, err := comments.ListApproved(ctx, p.ID, 1, 0)
	if err != nil || total != got[p.ID] {
		t.Fatal("list mismatch", total, err)
	}
	if v, e := comments.CountApprovedByPost(ctx, nil); e != nil || len(v) != 0 {
		t.Fatal(v, e)
	}
	if _, e := comments.CountApprovedByPost(ctx, []int64{0}); !errors.Is(e, comment.ErrInvalidInput) {
		t.Fatal(e)
	}
	cancel()
	if v, e := comments.CountApprovedByPost(ctx, []int64{p.ID}); !errors.Is(e, context.Canceled) || v != nil {
		t.Fatal(v, e)
	}
}
