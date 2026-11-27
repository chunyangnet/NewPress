package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/analytics"
)

func TestPostgreSQLAnalytics(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
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
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// This suite uses the explicitly configured isolated integration database.
	var original []byte
	if err = pool.QueryRow(ctx, `SELECT data FROM np_analytics WHERE id=1`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool.Exec(cleanup, `UPDATE np_analytics SET data=$1 WHERE id=1`, original)
	}()
	empty, _ := json.Marshal(analytics.EmptyState())
	if _, err = pool.Exec(ctx, `UPDATE np_analytics SET data=$1 WHERE id=1`, empty); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var id [16]byte
	rand.Read(id[:])
	batch := analytics.Batch{Writer: hex.EncodeToString(id[:]), Sequence: 1, Days: map[string]analytics.Day{}}
	for range 3 {
		analytics.AddEvent(batch.Days, analytics.Event{At: now, Source: "direct", PostID: 17})
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- NewAnalyticsRepository(pool).Apply(ctx, batch, now) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	r := NewAnalyticsRepository(pool)
	state, err := r.Snapshot(ctx, now)
	if err != nil || state.Days[analytics.Date(now)].Views != 3 {
		t.Fatal(state, err)
	}
	batch.Sequence = 2
	if err = r.Apply(ctx, batch, now); err != nil {
		t.Fatal(err)
	}
	state, err = NewAnalyticsRepository(pool).Snapshot(ctx, now)
	if err != nil || state.Days[analytics.Date(now)].Views != 6 {
		t.Fatal(state, err)
	}
	later := now.AddDate(0, 0, 90)
	batch.Sequence = 3
	batch.Days = map[string]analytics.Day{}
	if err = r.Apply(ctx, batch, later); err != nil {
		t.Fatal(err)
	}
	state, err = r.Snapshot(ctx, later)
	if err != nil || len(state.Days) != 0 {
		t.Fatal("retention failed", state, err)
	}

	reset := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE np_analytics SET data=$1 WHERE id=1`, empty); err != nil {
			t.Fatal(err)
		}
	}
	calendarNow := time.Date(2026, 11, 14, 18, 30, 0, 0, time.UTC)
	east := analyticsTestLocation(t, "Asia/Shanghai")
	west := analyticsTestLocation(t, "America/New_York")
	for _, name := range []string{"UTC", "Asia/Shanghai", "America/New_York", "Asia/Kathmandu"} {
		t.Run("context/"+name, func(t *testing.T) {
			reset(t)
			location := analyticsTestLocation(t, name)
			r := NewAnalyticsRepository(pool)
			r.SetLocationFunc(func() *time.Location {
				t.Error("frozen context consulted repository callback")
				return time.UTC
			})
			operation := analytics.WithLocation(ctx, location)
			batch := analytics.Batch{Writer: strings.Repeat("a", 32), Sequence: 1, Days: map[string]analytics.Day{}}
			analytics.AddEventIn(batch.Days, analytics.Event{At: calendarNow, Source: "direct"}, location)
			if err := r.Apply(operation, batch, calendarNow); err != nil {
				t.Fatal(err)
			}
			state, err := r.Snapshot(operation, calendarNow)
			if err != nil || state.Days[analytics.DateIn(calendarNow, location)].Views != 1 {
				t.Fatal("operation calendar changed", state, err)
			}
		})
	}
	t.Run("westward_receipt_capacity_and_heartbeat", func(t *testing.T) {
		reset(t)
		r := NewAnalyticsRepository(pool)
		batch := analytics.Batch{Writer: strings.Repeat("b", 32), Sequence: 1, Days: map[string]analytics.Day{}}
		for i := range analytics.RetentionDays {
			analytics.AddEventIn(batch.Days, analytics.Event{At: calendarNow.In(east).AddDate(0, 0, -i), Source: "direct"}, east)
		}
		if err := r.Apply(analytics.WithLocation(ctx, east), batch, calendarNow); err != nil {
			t.Fatal(err)
		}
		r = NewAnalyticsRepository(pool)
		r.SetLocationFunc(func() *time.Location { return west })
		if err := r.Apply(ctx, batch, calendarNow); err != nil {
			t.Fatal("confirmed receipt rejected after westward change", err)
		}
		batch.Sequence++
		batch.Days = map[string]analytics.Day{}
		oldest := calendarNow.In(west).AddDate(0, 0, 1-analytics.RetentionDays)
		analytics.AddEventIn(batch.Days, analytics.Event{At: oldest, Source: "direct"}, west)
		if err := r.Apply(ctx, batch, calendarNow); err != nil {
			t.Fatal("91st date blocked the writer", err)
		}
		state, err := r.Snapshot(ctx, calendarNow)
		if err != nil || len(state.Days) != analytics.RetentionDays || state.Days[analytics.DateIn(calendarNow, east)].Views != 1 || state.Days[analytics.DateIn(oldest, west)].Views != 0 {
			t.Fatal("future historical bucket lost or oldest date not evicted", state, err)
		}
		analytics.AddEventIn(batch.Days, analytics.Event{At: oldest, Source: "direct"}, west)
		if err := r.Apply(ctx, batch, calendarNow); !errors.Is(err, analytics.ErrInvalid) {
			t.Fatal("altered receipt accepted", err)
		}
		collector := analytics.NewCollector(r)
		collector.SetLocationFunc(func() *time.Location { return west })
		later := calendarNow.AddDate(0, 0, analytics.RetentionDays+1)
		if err := collector.Flush(ctx, later); err != nil {
			t.Fatal(err)
		}
		var body []byte
		if err := pool.QueryRow(ctx, `SELECT data FROM np_analytics WHERE id=1`).Scan(&body); err != nil {
			t.Fatal(err)
		}
		state, err = analytics.Decode(body)
		if err != nil || len(state.Days) != 0 || !state.UpdatedAt.Equal(later) {
			t.Fatal("heartbeat did not persist retention", state, err)
		}
	})
	t.Run("snapshot_retention_context", func(t *testing.T) {
		reset(t)
		r := NewAnalyticsRepository(pool)
		batch := analytics.Batch{Writer: strings.Repeat("c", 32), Sequence: 1, Days: map[string]analytics.Day{}}
		oldest := calendarNow.In(west).AddDate(0, 0, 1-analytics.RetentionDays)
		analytics.AddEventIn(batch.Days, analytics.Event{At: oldest, Source: "direct"}, west)
		if err := r.Apply(analytics.WithLocation(ctx, west), batch, calendarNow); err != nil {
			t.Fatal(err)
		}
		r.SetLocationFunc(func() *time.Location { return east })
		state, err := r.Snapshot(analytics.WithLocation(ctx, west), calendarNow)
		if err != nil || len(state.Days) != 1 {
			t.Fatal("snapshot ignored the frozen calendar", state, err)
		}
		state, err = r.Snapshot(ctx, calendarNow)
		if err != nil || len(state.Days) != 0 {
			t.Fatal("unfrozen snapshot ignored configured calendar", state, err)
		}
	})
	for _, committed := range []bool{false, true} {
		name := "uncommitted_retry"
		if committed {
			name = "committed_reply_lost_retry"
		}
		t.Run(name, func(t *testing.T) {
			reset(t)
			store := &analyticsCalendarFailureStore{AnalyticsRepository: NewAnalyticsRepository(pool), fail: true, committed: committed}
			collector := analytics.NewCollector(store)
			collector.SetLocationFunc(func() *time.Location { return east })
			store.before = func() { collector.SetLocationFunc(func() *time.Location { return west }) }
			collector.Record(analytics.Event{At: calendarNow, Source: "direct"})
			if err := collector.Flush(ctx, calendarNow); err == nil {
				t.Fatal("missing injected failure")
			}
			collector.Record(analytics.Event{At: calendarNow, Source: "direct"})
			state, err := collector.Snapshot(analytics.WithLocation(ctx, west), calendarNow)
			if err != nil || len(state.Days) != 2 || state.Days[analytics.DateIn(calendarNow, east)].Views != 1 || state.Days[analytics.DateIn(calendarNow, west)].Views != 1 {
				t.Fatal("retry changed calendar or double-counted", state, err)
			}
			state, err = NewAnalyticsRepository(pool).Snapshot(analytics.WithLocation(ctx, west), calendarNow)
			if err != nil || len(state.Days) != 2 || state.Days[analytics.DateIn(calendarNow, east)].Views != 1 || state.Days[analytics.DateIn(calendarNow, west)].Views != 1 {
				t.Fatal("retry did not persist unchanged labels", state, err)
			}
		})
	}
}

type analyticsCalendarFailureStore struct {
	*AnalyticsRepository
	fail, committed bool
	before          func()
}

func (s *analyticsCalendarFailureStore) Apply(ctx context.Context, batch analytics.Batch, now time.Time) error {
	first := s.fail
	if first {
		s.fail = false
		s.before()
		if !s.committed {
			return errors.New("injected pre-commit failure")
		}
	}
	if err := s.AnalyticsRepository.Apply(ctx, batch, now); err != nil {
		return err
	}
	if first {
		return errors.New("injected commit reply loss")
	}
	return nil
}

func analyticsTestLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func TestAnalyticsRepositoryCalendarCallbacks(t *testing.T) {
	r := NewAnalyticsRepository(nil)
	if r.zone(t.Context()) != time.UTC {
		t.Fatal("default calendar is not UTC")
	}
	locations := []*time.Location{time.UTC, analyticsTestLocation(t, "Asia/Shanghai"), analyticsTestLocation(t, "America/New_York"), analyticsTestLocation(t, "Asia/Kathmandu")}
	r.SetLocationFunc(func() *time.Location {
		t.Error("frozen context consulted callback")
		return time.UTC
	})
	for _, location := range locations {
		if r.zone(analytics.WithLocation(t.Context(), location)) != location {
			t.Fatal("repository ignored context calendar", location)
		}
	}
	r.SetLocationFunc(func() *time.Location {
		r.SetLocationFunc(func() *time.Location { return locations[1] })
		return locations[1]
	})
	done := make(chan *time.Location, 1)
	go func() { done <- r.zone(t.Context()) }()
	select {
	case location := <-done:
		if location != locations[1] {
			t.Fatal(location)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("repository callback ran while its lock was held")
	}
	var wg sync.WaitGroup
	for _, location := range locations {
		wg.Go(func() {
			for range 100 {
				r.SetLocationFunc(func() *time.Location { return location })
				if r.zone(t.Context()) == nil {
					t.Error("concurrent callback returned nil calendar")
				}
			}
		})
	}
	wg.Wait()
	r.SetLocationFunc(func() *time.Location { return nil })
	if r.zone(t.Context()) != time.UTC {
		t.Fatal("nil callback result did not fall back to UTC")
	}
}
