package commentorigin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
)

func TestUnreliableSourceNeverStoresOrQueriesProxyIP(t *testing.T) {
	s, _, _ := originFixture(t)
	s.lookup = func(context.Context, Settings, string) (Location, error) {
		t.Fatal("queried proxy IP")
		return Location{}, nil
	}
	if err := s.CaptureSource(1, Source{IP: "8.8.8.8", Method: "edgeone", Issue: "missing_cdn_header"}, "Windows NT 10.0"); err != nil {
		t.Fatal(err)
	}
	s.process(t.Context(), <-s.queue)
	r, err := s.PrivateRecord(1)
	if err != nil || r.IP != "" || r.Status != "unavailable" || r.Source != "edgeone" || r.SourceIssue != "missing_cdn_header" {
		t.Fatalf("record=%+v, %v", r, err)
	}
	b := s.Badges(1, []string{"guest"})
	if b.Location != "" || b.Device == "" {
		t.Fatalf("badges=%+v", b)
	}
	if err := s.Retry(); err != nil {
		t.Fatal(err)
	}
	r, _ = s.PrivateRecord(1)
	if r.Status != "unavailable" {
		t.Fatal("retry fabricated an address")
	}
}

func TestLookupCannotUndoDeletionRetentionOrRetry(t *testing.T) {
	for _, operation := range []string{"delete", "retention", "retry"} {
		t.Run(operation, func(t *testing.T) {
			s, m, root := originFixture(t)
			started, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			s.lookup = func(context.Context, Settings, string) (Location, error) {
				close(started)
				<-finish
				return Location{City: "stale", Precision: "city"}, nil
			}
			if err := s.Capture(1, "8.8.8.8", "Windows"); err != nil {
				t.Fatal(err)
			}
			go func() { defer close(done); s.process(t.Context(), <-s.queue) }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("lookup did not start")
			}
			switch operation {
			case "delete":
				if err := s.Forget(1); err != nil {
					t.Fatal(err)
				}
			case "retention":
				err := m.WithData(plugin.CommentOrigin, false, 0, func(dir string, _ uint64) error {
					r, err := plugin.ReadPrivate[Record](recordPath(dir, 1), 16<<10)
					if err != nil {
						return err
					}
					r.Created = time.Now().Add(-400 * 24 * time.Hour)
					return plugin.WritePrivate(recordPath(dir, 1), r)
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.PrivateRecord(1); err != nil {
					t.Fatal(err)
				}
			case "retry":
				if err := s.Retry(); err != nil {
					t.Fatal(err)
				}
			}
			close(finish)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not finish")
			}
			r, err := s.PrivateRecord(1)
			if operation == "delete" {
				if !os.IsNotExist(err) {
					t.Fatalf("deleted record recreated: %+v %v", r, err)
				}
			} else {
				if err != nil || r.Location.City != "" || r.Status == "resolved" {
					t.Fatalf("stale result committed: %+v %v", r, err)
				}
				if operation == "retention" && (r.IP != "" || r.UA != "" || r.Status != "expired") {
					t.Fatalf("retained raw source: %+v", r)
				}
				if operation == "retry" && (r.Attempts != 0 || !r.Next.IsZero()) {
					t.Fatalf("retry overwritten: %+v", r)
				}
			}
			entries, _ := os.ReadDir(filepath.Join(root, plugin.CommentOrigin, "data", "cache"))
			if len(entries) != 0 {
				t.Fatal("stale result leaked into cache")
			}
		})
	}
}

func TestOrphanCleanupKeepsRecordsOnStoreOutage(t *testing.T) {
	s, m, _ := originFixture(t)
	for _, id := range []int64{1, 2} {
		if err := s.Capture(id, "8.8.8.8", "Windows"); err != nil {
			t.Fatal(err)
		}
	}
	s.commentExists = func(context.Context, int64) (bool, error) { return false, errors.New("store offline") }
	s.pruneDeleted(t.Context())
	if _, err := s.PrivateRecord(1); err != nil {
		t.Fatalf("outage erased data: %v", err)
	}
	s.commentExists = func(_ context.Context, id int64) (bool, error) { return id == 2, nil }
	if err := m.SetEnabled(plugin.CommentOrigin, false); err != nil {
		t.Fatal(err)
	}
	s.pruneDeleted(t.Context())
	if _, err := s.PrivateRecord(1); !os.IsNotExist(err) {
		t.Fatalf("orphan remains: %v", err)
	}
	if _, err := s.PrivateRecord(2); err != nil {
		t.Fatalf("live record removed: %v", err)
	}
}

func TestCacheHitsDoNotExtendProviderResultLifetime(t *testing.T) {
	s, _, root := originFixture(t)
	calls := 0
	s.lookup = func(context.Context, Settings, string) (Location, error) {
		calls++
		return Location{City: "fixture", Precision: "city"}, nil
	}
	if err := s.Capture(1, "8.8.8.8", "Windows"); err != nil {
		t.Fatal(err)
	}
	s.process(t.Context(), <-s.queue)
	dir := filepath.Join(root, plugin.CommentOrigin, "data", "cache")
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("cache not created: %v", err)
	}
	path := filepath.Join(dir, files[0].Name())
	cached, err := plugin.ReadPrivate[cacheEntry](path, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cached.Expires = time.Now().UTC().Add(time.Hour)
	if err := plugin.WritePrivate(path, cached); err != nil {
		t.Fatal(err)
	}
	if err := s.Capture(2, "8.8.8.8", "Windows"); err != nil {
		t.Fatal(err)
	}
	s.process(t.Context(), <-s.queue)
	after, err := plugin.ReadPrivate[cacheEntry](path, 2048)
	if err != nil || !after.Expires.Equal(cached.Expires) || calls != 1 {
		t.Fatalf("cache TTL renewed: before=%v after=%v calls=%d err=%v", cached.Expires, after.Expires, calls, err)
	}
	if r, err := s.PrivateRecord(2); err != nil || r.Status != "resolved" {
		t.Fatalf("cache hit failed: %+v %v", r, err)
	}
}
