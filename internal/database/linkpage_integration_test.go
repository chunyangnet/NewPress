package database

import (
	"context"
	"fmt"
	"newpress.local/newpress/internal/linkpage"
	"os"
	"sync"
	"testing"
	"time"
)

func TestPostgreSQLLinkPages(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	pool, e := Open(t.Context(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = Migrate(t.Context(), pool); e != nil {
		t.Fatal(e)
	}
	slug := fmt.Sprintf("links-%d", time.Now().UnixNano())
	defer pool.Exec(context.Background(), `DELETE FROM np_link_pages WHERE slug=$1`, slug)
	s := NewLinkPageRepository(pool)
	a := linkpage.Actor{ID: 1, Admin: true}
	cfg := linkpage.Defaults()
	now := time.Now().UTC()
	if _, e = s.Apply(t.Context(), slug, a, linkpage.Command{Action: "create", Key: "create-link-page", ExpectedUserID: 1, Settings: &cfg}, now); e != nil {
		t.Fatal(e)
	}
	c := linkpage.Command{Action: "save_item", Key: "add-public-site", Version: 1, ExpectedUserID: 1, Item: &linkpage.Item{Name: "Example", URL: "https://example.com/", Enabled: true}}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() { _, e := NewLinkPageRepository(pool).Apply(t.Context(), slug, a, c, now); errs <- e })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	b, e := s.Get(t.Context(), slug)
	if e != nil || len(b.Items) != 1 || b.Version != 2 {
		t.Fatal(b, e)
	}
	pages, e := s.List(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range pages {
		if len(b.Items) > 0 || len(b.Receipts) > 0 {
			t.Fatal("list exposes body")
		}
	}
}
