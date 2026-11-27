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

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/idempotency"
)

func TestPostgreSQLEditorialSubmissions(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
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
	repo := NewPostRepository(pool)
	stamp := fmt.Sprintf("submission-%d", time.Now().UnixNano())
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(stamp+value))) }
	request := content.Submission{Key: digest("create"), Fingerprint: digest("body"), Action: "create", Input: content.PostInput{Title: stamp, Slug: stamp}}
	start := make(chan struct{})
	results := make(chan content.SubmissionResult, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := repo.Submit(ctx, request)
			if err != nil {
				t.Error(err)
				return
			}
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var postID int64
	created := 0
	for result := range results {
		if !result.Replayed {
			created++
		}
		if postID != 0 && postID != result.Receipt.PostID {
			t.Fatal("different identities")
		}
		postID = result.Receipt.PostID
	}
	if created != 1 || postID <= 0 {
		t.Fatal(created, postID)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		pool.Exec(cleanup, `DELETE FROM np_content_submissions WHERE post_id=$1`, postID)
		pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=$1`, postID)
	}()
	// A separate pool models a restarted process, independent of in-memory state.
	otherPool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	other := NewPostRepository(otherPool)
	if result, err := other.Submit(ctx, request); err != nil || !result.Replayed || result.Receipt.PostID != postID {
		t.Fatal(result, err)
	}
	changed := request
	changed.Fingerprint = digest("different")
	if _, err := other.Submit(ctx, changed); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatal(err)
	}
	update := content.Submission{Key: digest("update"), Fingerprint: digest("updated"), Action: "save", PostID: postID, Version: 1, Input: content.PostInput{Title: "updated", Slug: stamp}}
	// A receipt constraint failure occurs after the content/revision writes inside Submit.
	constraint := "submission_failure_" + digest("constraint")[:12]
	if _, err := pool.Exec(ctx, `ALTER TABLE np_content_submissions ADD CONSTRAINT `+constraint+` CHECK (key_hash <> '`+update.Key+`')`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `ALTER TABLE np_content_submissions DROP CONSTRAINT IF EXISTS `+constraint)
	if _, err := repo.Submit(ctx, update); err == nil {
		t.Fatal("injected receipt failure committed")
	}
	post, _ := repo.GetByID(ctx, postID)
	revisions, _ := repo.ListRevisions(ctx, postID, 20)
	if post.Version != 1 || len(revisions) != 1 || post.Title != stamp {
		t.Fatal(post, len(revisions))
	}
	if _, err := repo.FindSubmission(ctx, update.Key, update.Fingerprint); !errors.Is(err, content.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE np_content_submissions DROP CONSTRAINT `+constraint); err != nil {
		t.Fatal(err)
	}
	if result, err := repo.Submit(ctx, update); err != nil || result.Replayed || result.Receipt.Version != 2 {
		t.Fatal(result, err)
	}
	firstRevisions, _ := repo.ListRevisions(ctx, postID, 20)
	for i, action := range []string{"publish", "trash", "restore", "restore_revision"} {
		command := content.Submission{Key: digest(action), Fingerprint: digest("body" + action), Action: action, PostID: postID, Version: int64(i + 2), RevisionID: firstRevisions[len(firstRevisions)-1].ID}
		for attempt := range 2 {
			result, err := repo.Submit(ctx, command)
			if err != nil || result.Replayed != (attempt == 1) || result.Receipt.Version != int64(i+3) {
				t.Fatal(result, err)
			}
		}
	}
	if replay, err := other.Submit(ctx, update); err != nil || !replay.Replayed || replay.Receipt.Version != 2 {
		t.Fatal(replay, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE np_content_submissions SET created_at=$1 WHERE post_id=$2`, time.Now().Add(-content.SubmissionReplayWindow-time.Hour), postID); err != nil {
		t.Fatal(err)
	}
	if replay, err := other.Submit(ctx, request); err != nil || !replay.Replayed {
		t.Fatal(replay, err)
	}
	if _, err := other.Submit(ctx, update); !errors.Is(err, content.ErrVersionConflict) {
		t.Fatal(err)
	}
	revisions, _ = repo.ListRevisions(ctx, postID, 20)
	if len(revisions) != 6 {
		t.Fatal("extra revisions", len(revisions))
	}
}
