package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/activity"
)

func TestPostgreSQLActivityKeysetPaging(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	schema := fmt.Sprintf("activity_paging_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := pool.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	config := pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	if err := Migrate(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	var events []activity.Event
	for i := int64(120); i > 0; i-- {
		events = append(events, activity.New(activity.ContentUpdated, i, i, 1, 1, "draft", now))
	}
	tx, err := isolated.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := insertActivity(ctx, tx, "notification.post", events); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	source := NewActivitySource(isolated, "notification.post")
	after := ""
	seen := map[string]bool{}
	for range 3 {
		page, err := source.PendingActivityAfter(ctx, now, 50, after)
		if err != nil {
			t.Fatal(err)
		}
		expected := activity.PendingAfter(events, now, 50, after)
		if len(page) != len(expected) {
			t.Fatal("different page size")
		}
		for i, event := range page {
			if event.Key != expected[i].Key || seen[event.Key] {
				t.Fatal("ordering mismatch", event.Key)
			}
			seen[event.Key] = true
			// Leave the first batch pending: the next scan must still advance.
			if after != "" {
				if err := source.AckActivity(ctx, event.Key); err != nil {
					t.Fatal(err)
				}
			}
		}
		after = page[len(page)-1].Key
	}
	if len(seen) != 120 {
		t.Fatal("starved rows", len(seen))
	}
	page, err := source.PendingActivityAfter(ctx, now, 50, "")
	if err != nil || len(page) != 50 {
		t.Fatal("lost retained events", len(page), err)
	}
}
