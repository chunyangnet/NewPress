package database

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/idempotency"
)

func TestPostgreSQLCommentAtomicSubmission(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := pgx.Identifier{fmt.Sprintf("comment_retry_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, err := admin.Exec(cleanup, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	config := admin.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	otherPool, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	repository, other := NewCommentRepository(pool), NewCommentRepository(otherPool)
	post, err := NewPostRepository(pool).Create(ctx, content.PostInput{Title: "Comment retry", Slug: "comment-retry"})
	if err != nil {
		t.Fatal(err)
	}
	digest := func(v string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(v))) }
	request := comment.Submission{Key: digest("create"), Fingerprint: digest("body"), Input: comment.CreateInput{PostID: post.ID, AuthorName: "Reader", AuthorEmail: "reader@example.test", ContentSource: "Only once", Status: comment.StatusApproved}}
	start := make(chan struct{})
	results := make(chan comment.SubmissionResult, 12)
	var workers sync.WaitGroup
	for i := range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			store := repository
			if i%2 != 0 {
				store = other
			}
			<-start
			result, err := store.CreateOnce(ctx, request)
			if err != nil {
				t.Error(err)
				return
			}
			results <- result
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	id, created := int64(0), 0
	for result := range results {
		if id != 0 && id != result.Receipt.CommentID {
			t.Fatal("multiple IDs")
		}
		id = result.Receipt.CommentID
		if !result.Replayed {
			created++
		}
	}
	if id <= 0 || created != 1 {
		t.Fatal(id, created)
	}
	assertCounts := func(comments, receipts, events int) {
		t.Helper()
		var c, r, e int
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_comments),(SELECT count(*) FROM np_comment_submissions),(SELECT count(*) FROM np_outbox WHERE topic='notification.comment')`).Scan(&c, &r, &e); err != nil || c != comments || r != receipts || e != events {
			t.Fatal(c, r, e, err)
		}
	}
	assertCounts(1, 1, 1)
	changed := request
	changed.Fingerprint = digest("changed")
	if _, err := other.CreateOnce(ctx, changed); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatal(err)
	}
	// Failure after comment and outbox writes must roll all of them back.
	failing := request
	failing.Key = digest("rollback")
	if _, err := pool.Exec(ctx, `ALTER TABLE np_comment_submissions ADD CONSTRAINT fail_receipt CHECK (key_hash <> '`+failing.Key+`')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateOnce(ctx, failing); err == nil {
		t.Fatal("receipt failure committed")
	}
	assertCounts(1, 1, 1)
	if _, err := other.FindSubmission(ctx, failing.Key, failing.Fingerprint); !errors.Is(err, comment.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE np_comment_submissions DROP CONSTRAINT fail_receipt`); err != nil {
		t.Fatal(err)
	}
	second, err := other.CreateOnce(ctx, failing)
	if err != nil || second.Replayed {
		t.Fatal(second, err)
	}
	assertCounts(2, 2, 2)
	// A cancelled wait releases no lease and commits no new receipt.
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-comment-submissions', 0))`); err != nil {
		t.Fatal(err)
	}
	blocked := request
	blocked.Key = digest("cancelled")
	limited, done := context.WithTimeout(ctx, 80*time.Millisecond)
	_, err = other.CreateOnce(limited, blocked)
	done()
	_ = lock.Rollback(ctx)
	if err == nil {
		t.Fatal("cancelled lock wait succeeded")
	}
	assertCounts(2, 2, 2)
	if err := repository.Delete(ctx, id, comment.Mutation{ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM np_posts WHERE id=$1`, post.ID); err != nil {
		t.Fatal(err)
	}
	// Decade-old receipts still prevent recreation after article/comment deletion.
	if _, err := pool.Exec(ctx, `UPDATE np_comment_submissions SET created_at=now()-interval '10 years'`); err != nil {
		t.Fatal(err)
	}
	for _, original := range []comment.Submission{request, failing} {
		result, err := other.CreateOnce(ctx, original)
		if err != nil || !result.Replayed {
			t.Fatal(result, err)
		}
		if _, err := other.Get(ctx, result.Receipt.CommentID); !errors.Is(err, comment.ErrNotFound) {
			t.Fatal("deleted comment resurrected", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO np_comment_submissions(key_hash,fingerprint,comment_id,post_id,created_at) SELECT md5('fixture-'||n)||md5('fixture-'||n),repeat('b',64),1000000+n,1,now() FROM generate_series(1,$1::integer) n`, comment.MaxSubmissionReceipts-2); err != nil {
		t.Fatal(err)
	}
	if _, err := other.CreateOnce(ctx, blocked); !errors.Is(err, idempotency.ErrCapacity) {
		t.Fatal(err)
	}
	if result, err := other.CreateOnce(ctx, request); err != nil || !result.Replayed || result.Receipt.CommentID != id {
		t.Fatal(result, err)
	}
	t.Log("two pools, 12 concurrent requests, comment/outbox/receipt rollback, cancellation, deletion, permanent receipts and capacity passed")
}
