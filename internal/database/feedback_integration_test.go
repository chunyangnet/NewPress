package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/feedback"
)

func TestPostgreSQLFeedback(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	pool, e := Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	slug := fmt.Sprintf("feedback-%d", time.Now().UnixNano())
	defer pool.Exec(context.Background(), `DELETE FROM np_feedback_boards WHERE slug=$1`, slug)
	s := NewFeedbackRepository(pool)
	a := feedback.Actor{ID: 99001, Admin: true}
	reader := feedback.Actor{ID: 99002, Roles: []string{"subscriber"}}
	settings := feedback.Defaults()
	now := time.Now().UTC()
	_, e = s.Apply(ctx, slug, a, feedback.Command{Action: "create", Key: "feedback-create-key", Settings: &settings}, now)
	if e != nil {
		t.Fatal(e)
	}
	cmd := feedback.Command{Action: "submit", Key: "feedback-submit-key", Version: 1, Title: "持久化与并发投票", Body: "同一账号只能投一票，重复提交不会创建重复反馈。", Kind: "suggestion"}
	first, e := s.Apply(ctx, slug, reader, cmd, now)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	yes := true
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := NewFeedbackRepository(pool).Apply(ctx, slug, reader, feedback.Command{Action: "vote", Key: fmt.Sprintf("feedback-vote-%08d", i), Version: 1, ItemID: first.ItemID, Enabled: &yes}, now)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	b, e := s.Get(ctx, slug)
	if e != nil || len(b.Items) != 1 || len(b.Items[0].Votes) != 1 || b.Version != 14 {
		t.Fatal("concurrent desired-state votes", b.Version, e)
	}
	result, e := NewFeedbackRepository(pool).Apply(ctx, slug, reader, cmd, now)
	if e != nil || !result.Replay || result.ItemID != first.ItemID {
		t.Fatal("durable receipt", e, result)
	}
	_, e = s.Apply(ctx, slug, a, feedback.Command{Action: "configure", Key: "feedback-stale-key", Version: 1, Settings: &settings}, now)
	if !errors.Is(e, feedback.ErrConflict) {
		t.Fatal("stale configuration", e)
	}
	headers, e := s.List(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, h := range headers {
		if h.Slug == slug && (len(h.Items) != 0 || len(h.Receipts) != 0) {
			t.Fatal("directory exposes facts")
		}
	}
}
