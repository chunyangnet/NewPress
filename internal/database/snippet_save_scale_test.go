package database

import (
	"encoding/json"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

// The same test file runs on the unmodified source in the controlled baseline.
// Measurements cover actual SaveSnippet calls, excluding fixture setup and
// postcondition checks. Heap sampling is approximate and excludes PostgreSQL.
func TestPostgreSQLSnippetSaveScale(t *testing.T) {
	if os.Getenv("NEWPRESS_SNIPPET_SAVE_SCALE") != "1" {
		t.Skip("set NEWPRESS_SNIPPET_SAVE_SCALE=1 for isolated before/after save measurements")
	}
	for _, fixture := range []struct {
		name                 string
		revisions, bodyBytes int
	}{{"many-small", 9000, 1450}, {"fewer-large", 400, 32000}} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx, pool := openSetupIntegrationPool(t)
			if err := Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			s := seedSnippetSaveHistory(t, ctx, pool, fixture.revisions, fixture.bodyBytes)
			r := NewPostRepository(pool)
			oldDigest := snippetSaveHistoryDigest(t, ctx, pool, s.ID, s.CurrentVersion)
			oldVersion := s.CurrentVersion
			var retainedBytes int64
			if err := pool.QueryRow(ctx, `SELECT 4096+sum(octet_length(data)) FROM np_snippet_revisions WHERE snippet_id=$1`, s.ID).Scan(&retainedBytes); err != nil {
				t.Fatal(err)
			}
			for round := 1; round <= 3; round++ {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				var maxHeap, maxInuse atomic.Uint64
				maxHeap.Store(before.HeapAlloc)
				maxInuse.Store(before.HeapInuse)
				done, joined := make(chan struct{}), make(chan struct{})
				go func() {
					defer close(joined)
					tick := time.NewTicker(2 * time.Millisecond)
					defer tick.Stop()
					for {
						select {
						case <-done:
							return
						case <-tick.C:
							var sample runtime.MemStats
							runtime.ReadMemStats(&sample)
							if sample.HeapAlloc > maxHeap.Load() {
								maxHeap.Store(sample.HeapAlloc)
							}
							if sample.HeapInuse > maxInuse.Load() {
								maxInuse.Store(sample.HeapInuse)
							}
						}
					}
				}()
				start := time.Now()
				next, err := r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: "saved", ContentSource: "<p>saved new version</p>", Visibility: content.SnippetShared}, snippetSaveAccess)
				elapsed := time.Since(start)
				close(done)
				<-joined
				runtime.ReadMemStats(&after)
				if after.HeapAlloc > maxHeap.Load() {
					maxHeap.Store(after.HeapAlloc)
				}
				if after.HeapInuse > maxInuse.Load() {
					maxInuse.Store(after.HeapInuse)
				}
				if err != nil || next.Version != s.Version+1 {
					t.Fatal(next, err)
				}
				s = next
				if got := snippetSaveHistoryDigest(t, ctx, pool, s.ID, oldVersion); got != oldDigest {
					t.Fatal("old bytes changed")
				}
				var count int
				if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_snippet_revisions WHERE snippet_id=$1`, s.ID).Scan(&count); err != nil || count != fixture.revisions+round {
					t.Fatal(count, err)
				}
				entry, _ := json.Marshal(map[string]any{"kind": "snippet-save-sample", "fixture": fixture.name, "round": round, "initial_revisions": fixture.revisions, "initial_logical_bytes": retainedBytes, "old_body_source_bytes": fixture.bodyBytes + 7, "milliseconds": float64(elapsed.Microseconds()) / 1000, "allocated_bytes": after.TotalAlloc - before.TotalAlloc, "heap_before_bytes": before.HeapAlloc, "sampled_peak_heap_bytes": maxHeap.Load(), "sampled_peak_heap_inuse_bytes": maxInuse.Load(), "heap_after_bytes": after.HeapAlloc, "old_history_preserved": true, "new_version": s.Version})
				t.Log(string(entry))
			}
		})
	}
}
