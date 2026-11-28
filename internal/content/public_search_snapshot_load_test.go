package content

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Finite, opt-in mixed load. These samples include real file replacement and
// query time; they do not assert portable latency or a process memory ceiling.
func TestPublicSearchFileSnapshotLoadMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_SEARCH_SNAPSHOT_LOAD") != "1" {
		t.Skip("opt-in file search and edit measurements")
	}
	for _, size := range []struct{ count, bytes int }{{1000, 1024}, {10000, 1024}, {1000, 16384}} {
		t.Run(fmt.Sprintf("%d-%d", size.count, size.bytes), func(t *testing.T) {
			r := NewMemoryRepository(false)
			stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			base, err := r.Create(t.Context(), PostInput{Title: "ordinary", Slug: "fixture", Status: StatusPublish,
				Excerpt: "summary", ContentSource: "<p>plain</p>", PublishedAt: stamp})
			if err != nil {
				t.Fatal(err)
			}
			r.posts = nil
			r.revisions = make(map[int64][]Revision)
			for i := range size.count {
				p := clonePost(base)
				p.ID, p.Slug = int64(i+1), fmt.Sprintf("load-%d", i)
				body := fmt.Sprintf("<p>%d needle %s</p>", i, strings.Repeat("plain 中文 ", size.bytes/13))
				p.ContentSource, p.ContentHTML = body, body
				r.posts = append(r.posts, p)
			}
			r.nextID = int64(size.count + 1)
			payload, err := encodeContentDocument(r)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "content.json")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			file, err := NewFileRepository(path, false)
			if err != nil {
				t.Fatal(err)
			}
			if page, err := file.SearchPublished(t.Context(), PublishedQuery{Search: "needle"}); err != nil || page.Total != size.count {
				t.Fatal("warmup", page.Total, err)
			}
			type sample struct {
				Operation string  `json:"operation"`
				Worker    int     `json:"worker"`
				Round     int     `json:"round"`
				Millis    float64 `json:"milliseconds"`
			}
			start := make(chan struct{})
			results := make(chan sample, 4*24+8)
			var workers sync.WaitGroup
			for worker := range 4 {
				workers.Go(func() {
					<-start
					for i := range 24 {
						query := PublishedQuery{Search: "needle", Limit: 20, Offset: 20 * (i % 3)}
						began := time.Now()
						page, err := file.SearchPublished(context.Background(), query)
						results <- sample{"query", worker, i, float64(time.Since(began).Microseconds()) / 1000}
						if err != nil || page.Total != size.count && page.Total != size.count-1 || len(page.Items) != 20 {
							t.Error("mixed query result", page.Total, err)
						}
						for _, p := range page.Items {
							if p.Status != StatusPublish || !strings.Contains(PublicSearchBody(p.ContentHTML), "needle") {
								t.Error("stale public result")
							}
						}
					}
				})
			}
			workers.Go(func() {
				<-start
				p := base
				for i := range 8 {
					began := time.Now()
					p, err = file.ChangeStatus(t.Context(), 1, Mutation{ExpectedVersion: p.Version}, []string{StatusPrivate, StatusPublish}[i%2])
					results <- sample{"write", 4, i, float64(time.Since(began).Microseconds()) / 1000}
					if err != nil {
						t.Error("mixed write", err)
						return
					}
				}
			})
			runtime.GC()
			var before, after, settled, reclaimed runtime.MemStats
			runtime.ReadMemStats(&before)
			began := time.Now()
			close(start)
			workers.Wait()
			elapsed := time.Since(began)
			runtime.ReadMemStats(&after)
			runtime.GC()
			runtime.ReadMemStats(&settled)
			// A second collection distinguishes persistent state from transient
			// objects retained for a GC cycle (including library buffer pools).
			runtime.GC()
			runtime.ReadMemStats(&reclaimed)
			close(results)
			var samples []sample
			for s := range results {
				samples = append(samples, s)
			}
			if len(samples) != 104 {
				t.Fatal("missing mixed load operations", len(samples))
			}
			// Confirm the terminal committed state survives a fresh file open.
			reopened, err := NewFileRepository(path, false)
			if err != nil {
				t.Fatal(err)
			}
			if page, err := reopened.SearchPublished(t.Context(), PublishedQuery{Search: "needle"}); err != nil || page.Total != size.count {
				t.Fatal("reopened final state", page.Total, err)
			}
			report := map[string]any{"rows": size.count, "body_bytes_approx": size.bytes, "seconds": elapsed.Seconds(), "samples": samples,
				"allocated_bytes": after.TotalAlloc - before.TotalAlloc, "heap_before": before.HeapAlloc, "heap_after": after.HeapAlloc,
				"heap_after_gc": settled.HeapAlloc, "heap_after_second_gc": reclaimed.HeapAlloc, "gc_cycles": after.NumGC - before.NumGC}
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			t.Log("SEARCH_SNAPSHOT_LOAD " + string(data))
			runtime.KeepAlive(file)
		})
	}
}
