package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testBatch(now time.Time, writer string, sequence int64, n int) Batch {
	b := Batch{Writer: writer, Sequence: sequence, Days: map[string]Day{}}
	for range n {
		AddEvent(b.Days, Event{At: now, PostID: 7, Source: "search.example", Search: true, Miss: true, Term: "阅读"})
	}
	return b
}
func TestFileAnalyticsRetryRestartFailureAndRetention(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "analytics.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	b := testBatch(now, strings.Repeat("a", 32), 1, 3)
	if err = s.Apply(t.Context(), b, now); err != nil {
		t.Fatal(err)
	}
	s, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(t.Context(), b, now); err != nil {
		t.Fatal(err)
	}
	state, err := s.Snapshot(t.Context(), now)
	if err != nil || state.Days[Date(now)].Views != 3 {
		t.Fatal(state, err)
	}
	altered := testBatch(now, b.Writer, 1, 4)
	if err = s.Apply(t.Context(), altered, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("altered replay", err)
	}
	before, _ := os.ReadFile(path)
	if err = os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	b.Sequence = 2
	if err = s.Apply(t.Context(), b, now); !errors.Is(err, ErrChanged) {
		t.Fatal("external mutation accepted", err)
	}
	if s.state.Days[Date(now)].Views != 3 {
		t.Fatal("failed persistence changed memory")
	}
	if err = os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(t.Context(), b, now); err != nil {
		t.Fatal(err)
	}
	state, err = s.Snapshot(t.Context(), now)
	if err != nil || state.Days[Date(now)].Views != 6 {
		t.Fatal(state, err)
	}
	state.Days[Date(now)].Posts[7] = 999
	again, _ := s.Snapshot(t.Context(), now)
	if again.Days[Date(now)].Posts[7] != 6 {
		t.Fatal("snapshot aliases live state")
	}
	later := now.AddDate(0, 0, RetentionDays)
	empty := Batch{Writer: b.Writer, Sequence: 3, Days: map[string]Day{}}
	if err = s.Apply(t.Context(), empty, later); err != nil {
		t.Fatal(err)
	}
	s, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.Snapshot(t.Context(), later)
	if err != nil || len(state.Days) != 0 {
		t.Fatal("expired data persisted", state, err)
	}
}
func TestAnalyticsDailyCapacityPrivacyAndDecode(t *testing.T) {
	now := time.Now().UTC()
	b := Batch{Writer: strings.Repeat("b", 32), Sequence: 1, Days: map[string]Day{}}
	for i := range 1100 {
		AddEvent(b.Days, Event{At: now, PostID: int64(i + 1), Source: fmt.Sprintf("s%d.example", i), Search: true, Miss: true, Term: fmt.Sprintf("term %d", i)})
	}
	state, _, err := Merge(EmptyState(), b, now)
	if err != nil {
		t.Fatal(err)
	}
	day := state.Days[Date(now)]
	if len(day.Posts) > MaxPosts+1 || len(day.Sources) > MaxSources+1 || len(day.Terms) > MaxTerms+1 || day.Views != 1100 || day.Posts[0] == 0 || day.Sources[Other] == 0 {
		t.Fatal(day)
	}
	for _, input := range []string{"email@example.test", "https://example.test/?token=x", "12345678901", "hello\nworld", "token=secret", strings.Repeat("字", 81), "a\u200bb"} {
		if SearchTerm(input) != "" {
			t.Fatal("sensitive term retained", input)
		}
	}
	if SearchTerm("  NewPress  入门  ") != "newpress 入门" {
		t.Fatal("normal term dropped")
	}
	for _, pair := range [][2]string{{"https://Search.Example/path?secret=x#hash", "search.example"}, {"https://example.test/private", "internal"}, {"", "direct"}, {"https://127.0.0.1/private", Other}, {"https://user:pass@search.example/", "direct"}} {
		if got := Source(pair[0], "https://example.test"); got != pair[1] {
			t.Fatal(pair, got)
		}
	}
	body, _ := json.Marshal(state)
	if _, err = Decode(body); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(string(body), `"schema":1`, `"schema":1,"schema":1`, 1), strings.Replace(string(body), `"schema":1`, `"schema":1,"unknown":true`, 1), string(body) + " {}"} {
		if _, err = Decode([]byte(bad)); err == nil {
			t.Fatal("invalid document accepted")
		}
	}
}

type failAfterCommit struct {
	Store
	fail bool
}

func (s *failAfterCommit) Apply(ctx context.Context, b Batch, now time.Time) error {
	if err := s.Store.Apply(ctx, b, now); err != nil {
		return err
	}
	if s.fail {
		s.fail = false
		return errors.New("commit reply lost")
	}
	return nil
}
func TestCollectorUncertainCommitConcurrentQueueAndOverflow(t *testing.T) {
	now := time.Now()
	store, _ := NewFileStore("")
	uncertain := &failAfterCommit{Store: store, fail: true}
	c := NewCollector(uncertain)
	c.Record(Event{At: now, PostID: 1, Source: "direct"})
	if c.Flush(t.Context(), now) == nil {
		t.Fatal("missing injected failure")
	}
	c.Record(Event{At: now, PostID: 2, Source: "direct"})
	if err := c.Flush(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Snapshot(t.Context(), now)
	if state.Days[Date(now)].Views != 2 {
		t.Fatal("uncertain replay counted twice", state)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				c.Record(Event{At: now, Source: "direct"})
			}
		}()
	}
	wg.Wait()
	if err := c.Flush(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	state, _ = store.Snapshot(t.Context(), now)
	if state.Days[Date(now)].Views != 402 {
		t.Fatal(state)
	}
	for range 2050 {
		c.Record(Event{At: now, Source: "direct"})
	}
	if c.Dropped() != 2 {
		t.Fatal("unbounded queue", c.Dropped())
	}
	if err := c.Flush(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	state, _ = store.Snapshot(t.Context(), now)
	if state.Days[Date(now)].Views != 2450 {
		t.Fatal(state)
	}
}

func testLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func TestAnalyticsOperationLocation(t *testing.T) {
	if _, ok := LocationFromContext(t.Context()); ok {
		t.Fatal("an unfrozen context has a location")
	}
	if location, ok := LocationFromContext(WithLocation(t.Context(), nil)); !ok || location != time.UTC {
		t.Fatal("nil must explicitly freeze UTC", location, ok)
	}
	for _, test := range []struct {
		zone  string
		dates [2]string
	}{
		{"UTC", [2]string{"2026-11-14", "2026-11-14"}},
		{"Asia/Shanghai", [2]string{"2026-11-14", "2026-11-15"}},
		{"America/New_York", [2]string{"2026-11-13", "2026-11-14"}},
		{"Asia/Kathmandu", [2]string{"2026-11-14", "2026-11-15"}},
	} {
		t.Run(test.zone, func(t *testing.T) {
			location := testLocation(t, test.zone)
			for i, clock := range [][2]int{{1, 0}, {18, 30}} {
				now := time.Date(2026, 11, 14, clock[0], clock[1], 0, 0, time.UTC)
				store, err := NewFileStore("")
				if err != nil {
					t.Fatal(err)
				}
				collector := NewCollector(store)
				collector.SetLocationFunc(func() *time.Location {
					t.Error("frozen context unexpectedly consulted a callback")
					return time.UTC
				})
				ctx := WithLocation(t.Context(), location)
				// The event's original offset must not be reinterpreted as local time.
				collector.Record(Event{At: now.In(time.FixedZone("source", -7*3600)), Source: "direct"})
				state, err := collector.Snapshot(ctx, now)
				if err != nil || len(state.Days) != 1 || state.Days[test.dates[i]].Views != 1 {
					t.Fatalf("snapshot at %s = %#v, %v", now, state, err)
				}
			}
		})
	}
}

func TestCollectorBucketsAtFirstFlushAndBindsStore(t *testing.T) {
	now := time.Date(2026, 11, 14, 19, 0, 0, 0, time.UTC)
	for _, zones := range [][2]string{{"UTC", "Asia/Shanghai"}, {"Asia/Shanghai", "America/New_York"}} {
		t.Run(zones[0]+"_to_"+zones[1], func(t *testing.T) {
			before, after := testLocation(t, zones[0]), testLocation(t, zones[1])
			store, err := NewFileStore("")
			if err != nil {
				t.Fatal(err)
			}
			collector := NewCollector(store)
			collector.SetLocationFunc(func() *time.Location { return before })
			collector.Record(Event{At: now, Source: "direct"})
			collector.SetLocationFunc(func() *time.Location { return after })
			if store.zone() != after {
				t.Fatal("collector did not bind its store")
			}
			if err := collector.Flush(t.Context(), now); err != nil {
				t.Fatal(err)
			}
			state, err := store.Snapshot(t.Context(), now)
			if err != nil || len(state.Days) != 1 || state.Days[DateIn(now, after)].Views != 1 {
				t.Fatal("unbatched event kept the Record-time calendar", state, err)
			}
			collector.SetLocationFunc(func() *time.Location { return nil })
			if collector.zone() != time.UTC || store.zone() != time.UTC {
				t.Fatal("nil callback result did not fall back to UTC")
			}
			collector.SetLocationFunc(nil)
			if store.zone() != time.UTC {
				t.Fatal("cleared callback did not reach store")
			}
		})
	}
}

type calendarTestStore struct {
	Store
	apply    func(context.Context, Batch, time.Time) error
	snapshot func(context.Context, time.Time) (State, error)
}

func (s *calendarTestStore) Apply(ctx context.Context, batch Batch, now time.Time) error {
	return s.apply(ctx, batch, now)
}

func (s *calendarTestStore) Snapshot(ctx context.Context, now time.Time) (State, error) {
	if s.snapshot != nil {
		return s.snapshot(ctx, now)
	}
	return s.Store.Snapshot(ctx, now)
}

func TestCollectorFrozenCalendarDuringApplyAndRetry(t *testing.T) {
	now := time.Date(2026, 11, 14, 19, 0, 0, 0, time.UTC)
	for _, zones := range [][2]string{{"Asia/Shanghai", "America/New_York"}, {"America/New_York", "Asia/Shanghai"}} {
		for _, failure := range []string{"none", "uncommitted", "reply_lost"} {
			t.Run(zones[0]+"_to_"+zones[1]+"/"+failure, func(t *testing.T) {
				before, after := testLocation(t, zones[0]), testLocation(t, zones[1])
				path := filepath.Join(t.TempDir(), "analytics.json")
				store, err := NewFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				wrapped := &calendarTestStore{Store: store}
				collector := NewCollector(wrapped)
				collector.SetLocationFunc(func() *time.Location { return before })
				store.SetLocationFunc(func() *time.Location { return before })
				var applied []string
				var applyLocations, snapshotLocations []*time.Location
				wrapped.apply = func(ctx context.Context, batch Batch, now time.Time) error {
					location, ok := LocationFromContext(ctx)
					if !ok {
						t.Fatal("Apply did not receive a frozen calendar")
					}
					body, _ := json.Marshal(batch)
					applied = append(applied, string(body))
					applyLocations = append(applyLocations, location)
					if len(applied) == 1 {
						// Settings change after bucketing but before the store sees it.
						collector.SetLocationFunc(func() *time.Location { return after })
						store.SetLocationFunc(func() *time.Location { return after })
						if failure == "uncommitted" {
							return errors.New("injected pre-commit failure")
						}
					}
					if err := store.Apply(ctx, batch, now); err != nil {
						return err
					}
					if len(applied) == 1 && failure == "reply_lost" {
						return errors.New("injected commit reply loss")
					}
					return nil
				}
				wrapped.snapshot = func(ctx context.Context, now time.Time) (State, error) {
					location, ok := LocationFromContext(ctx)
					if !ok {
						t.Fatal("Snapshot did not receive a frozen calendar")
					}
					snapshotLocations = append(snapshotLocations, location)
					return store.Snapshot(ctx, now)
				}
				collector.Record(Event{At: now, Source: "direct"})
				_, err = collector.Snapshot(t.Context(), now)
				if (err != nil) != (failure != "none") {
					t.Fatalf("first attempt failure=%s: %v", failure, err)
				}
				if failure == "none" && (len(snapshotLocations) != 1 || snapshotLocations[0] != before) {
					t.Fatal("Snapshot changed calendar after Flush", snapshotLocations)
				}
				persisted, err := store.Snapshot(t.Context(), now)
				if err != nil {
					t.Fatal(err)
				}
				wantBeforeRetry := int64(1)
				if failure == "uncommitted" {
					wantBeforeRetry = 0
				}
				if persisted.Days[DateIn(now, before)].Views != wantBeforeRetry {
					t.Fatal("unexpected commit state before retry", persisted)
				}
				collector.Record(Event{At: now, Source: "direct"})
				state, err := collector.Snapshot(WithLocation(t.Context(), after), now)
				if err != nil || len(state.Days) != 2 || state.Days[DateIn(now, before)].Views != 1 || state.Days[DateIn(now, after)].Views != 1 {
					t.Fatal("retry lost, rebucketed, or double-counted events", state, err)
				}
				if applyLocations[0] != before || applyLocations[len(applyLocations)-1] != after {
					t.Fatal("new batches did not use the operation calendar", applyLocations)
				}
				if failure != "none" && (len(applied) != 3 || applied[0] != applied[1] || applyLocations[1] != before) {
					t.Fatal("retry changed batch/hash or calendar", applied, applyLocations)
				}
				reloaded, err := NewFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				reloaded.SetLocationFunc(func() *time.Location { return after })
				state, err = reloaded.Snapshot(t.Context(), now)
				if err != nil || len(state.Days) != 2 || state.Days[DateIn(now, before)].Views != 1 || state.Days[DateIn(now, after)].Views != 1 {
					t.Fatal("calendar labels did not survive reload", state, err)
				}
			})
		}
	}
}

func TestFileAnalyticsConfirmedReceiptSurvivesWestwardChange(t *testing.T) {
	now := time.Date(2026, 11, 14, 19, 0, 0, 0, time.UTC)
	east, west := testLocation(t, "Asia/Shanghai"), testLocation(t, "America/New_York")
	path := filepath.Join(t.TempDir(), "analytics.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	batch := Batch{Writer: strings.Repeat("d", 32), Sequence: 1, Days: map[string]Day{}}
	AddEventIn(batch.Days, Event{At: now, Source: "direct"}, east)
	if err := store.Apply(WithLocation(t.Context(), east), batch, now); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.SetLocationFunc(func() *time.Location { return west })
	if err := store.Apply(t.Context(), batch, now); err != nil {
		t.Fatal("confirmed receipt failed under the new calendar", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("confirmed receipt rewrote persisted data", err)
	}
	// A receipt permits only the exact payload, never a fresh future batch.
	AddEventIn(batch.Days, Event{At: now, Source: "direct"}, east)
	if err := store.Apply(t.Context(), batch, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("altered receipt accepted", err)
	}
	batch.Writer = strings.Repeat("e", 32)
	if err := store.Apply(t.Context(), batch, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("new future labels accepted", err)
	}
}

func TestFileAnalyticsTimezoneCapacityAndHeartbeat(t *testing.T) {
	now := time.Date(2026, 11, 14, 19, 0, 0, 0, time.UTC)
	east, west := testLocation(t, "Asia/Shanghai"), testLocation(t, "America/New_York")
	path := filepath.Join(t.TempDir(), "analytics.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	batch := Batch{Writer: strings.Repeat("f", 32), Sequence: 1, Days: map[string]Day{}}
	for i := range RetentionDays {
		AddEventIn(batch.Days, Event{At: now.In(east).AddDate(0, 0, -i), Source: "direct"}, east)
	}
	if err := store.Apply(WithLocation(t.Context(), east), batch, now); err != nil {
		t.Fatal(err)
	}
	store.SetLocationFunc(func() *time.Location { return west })
	batch.Sequence++
	batch.Days = map[string]Day{}
	oldest := now.In(west).AddDate(0, 0, 1-RetentionDays)
	AddEventIn(batch.Days, Event{At: oldest, Source: "direct"}, west)
	if err := store.Apply(t.Context(), batch, now); err != nil {
		t.Fatal("91st date blocked the writer", err)
	}
	state, err := store.Snapshot(t.Context(), now)
	if err != nil || len(state.Days) != RetentionDays || state.Days[DateIn(now, east)].Views != 1 || state.Days[DateIn(oldest, west)].Views != 0 {
		t.Fatal("capacity must evict oldest, not the previous timezone's future date", state, err)
	}
	if err := Validate(state); err != nil {
		t.Fatal(err)
	}
	store, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	collector := NewCollector(store)
	collector.SetLocationFunc(func() *time.Location { return west })
	if err := collector.Flush(t.Context(), now); err != nil {
		t.Fatal("same-day empty heartbeat failed", err)
	}
	// Crossing a local day must persist retention even with no new events.
	later := now.AddDate(0, 0, RetentionDays+1)
	if err := collector.Flush(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := Decode(body)
	if err != nil || len(persisted.Days) != 0 || !persisted.UpdatedAt.Equal(later) {
		t.Fatal("heartbeat did not persist pruning", persisted, err)
	}
	if err := collector.Flush(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(body) {
		t.Fatal("same-day heartbeat rewrote state", err)
	}
}

func TestFileSnapshotPrefersFrozenCalendarForRetention(t *testing.T) {
	now := time.Date(2026, 11, 14, 19, 0, 0, 0, time.UTC)
	east, west := testLocation(t, "Asia/Shanghai"), testLocation(t, "America/New_York")
	store, err := NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	oldest := now.In(west).AddDate(0, 0, 1-RetentionDays)
	batch := testBatch(oldest, strings.Repeat("a", 32), 1, 1)
	if err := store.Apply(WithLocation(t.Context(), west), batch, now); err != nil {
		t.Fatal(err)
	}
	store.SetLocationFunc(func() *time.Location { return east })
	state, err := store.Snapshot(WithLocation(t.Context(), west), now)
	if err != nil || len(state.Days) != 1 {
		t.Fatal("snapshot ignored frozen retention calendar", state, err)
	}
	state, err = store.Snapshot(t.Context(), now)
	if err != nil || len(state.Days) != 0 {
		t.Fatal("unfrozen snapshot ignored configured calendar", state, err)
	}
}

func TestAnalyticsLocationCallbacksAreOutsideLocks(t *testing.T) {
	east := testLocation(t, "Asia/Kathmandu")
	now := time.Date(2026, 11, 14, 19, 0, 0, 0, time.UTC)
	store, err := NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	collector := NewCollector(store)
	collector.SetLocationFunc(func() *time.Location {
		collector.SetLocationFunc(func() *time.Location { return east })
		return east
	})
	collector.Record(Event{At: now, Source: "direct"})
	done := make(chan error, 1)
	go func() { done <- collector.Flush(t.Context(), now) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("collector callback deadlocked while rebinding its store")
	}
	store.SetLocationFunc(func() *time.Location {
		store.SetLocationFunc(func() *time.Location { return east })
		// Reentry with an explicit calendar must not run this callback again.
		_, err := store.Snapshot(WithLocation(t.Context(), east), now)
		if err != nil {
			return nil
		}
		return east
	})
	go func() { _, err := store.Snapshot(t.Context(), now); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("store invoked callback while holding a lock")
	}
}

func TestAnalyticsConcurrentLocationReplacement(t *testing.T) {
	locations := []*time.Location{time.UTC, testLocation(t, "Asia/Shanghai"), testLocation(t, "America/New_York"), testLocation(t, "Asia/Kathmandu")}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	store, err := NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	collector := NewCollector(store)
	var wg sync.WaitGroup
	for _, location := range locations {
		wg.Go(func() {
			for range 50 {
				collector.SetLocationFunc(func() *time.Location { return location })
				collector.Record(Event{At: now, Source: "direct"})
				if _, err := collector.Snapshot(t.Context(), now); err != nil {
					t.Error(err)
				}
				if _, err := store.Snapshot(t.Context(), now); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	collector.SetLocationFunc(func() *time.Location { return locations[3] })
	if store.zone() != locations[3] {
		t.Fatal("store binding is stale after concurrent replacements")
	}
	state, err := collector.Snapshot(t.Context(), now)
	if err != nil || state.Days[Date(now)].Views != 200 {
		t.Fatal("concurrent callback replacement lost events", state, err)
	}
}
