package content

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPublicSearchFieldsMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_SEARCH_FIELDS_LOAD") != "1" {
		t.Skip("opt-in complete file search measurements with normalized and changing fields")
	}
	for _, shape := range []struct {
		name  string
		posts int
		mixed bool
		tags  int
	}{{"ready-1000", 1000, false, 0}, {"ready-10000", 10000, false, 0}, {"changed-1000", 1000, true, 0}, {"tags-1000x32", 1000, false, 32}} {
		t.Run(shape.name, func(t *testing.T) {
			state := contentHistoryLoadingFixture(t, shape.posts, 0, 0)
			var tags []Term
			for i := range shape.tags {
				term, err := state.CreateTerm(t.Context(), TermInput{Taxonomy: TaxonomyTag, Slug: fmt.Sprintf("tag-%d", i), Name: fmt.Sprintf("plain tag %d 中文", i)})
				if err != nil {
					t.Fatal(err)
				}
				tags = append(tags, term)
			}
			for i := range state.posts {
				post := &state.posts[i]
				post.Title, post.Excerpt, post.AuthorName = fmt.Sprintf("ordinary title %d", i), "plain summary 中文", "editor"
				post.Tags = append([]Term(nil), tags...)
				if shape.mixed {
					post.Title, post.Excerpt, post.AuthorName = fmt.Sprintf("ARTICLE %d ÉCLAIR", i), "Summary  UPPER \u3000 text", "EDITOR"
				}
			}
			payload := contentCandidateBytes(t, state)
			fixture := fmt.Sprintf("%x", sha256.Sum256(payload))
			path := filepath.Join(t.TempDir(), "content.json")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			file, err := NewFileRepository(path, false)
			if err != nil {
				t.Fatal(err)
			}
			file.state.now = state.now
			payload, state = nil, nil
			for _, operation := range []string{"hit", "miss", "title"} {
				query := PublishedQuery{Search: "needle", Limit: 20}
				if operation == "miss" {
					query.Search = "absent-phrase"
				} else if operation == "title" {
					query.Search, query.Sort = "", "title"
				}
				want, err := referencePublishedSearch(file.state, t.Context(), query)
				if err != nil {
					t.Fatal(err)
				}
				result, err := json.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				resultHash := fmt.Sprintf("%x", sha256.Sum256(result))
				var page PublishedPage
				run := func() {
					page, err = file.SearchPublished(t.Context(), query)
					if err != nil || page.Total != want.Total || len(page.Items) != len(want.Items) {
						t.Fatal("complete query changed", err)
					}
					for i, post := range page.Items {
						if post.ID != want.Items[i].ID || post.Title != want.Items[i].Title || !strings.Contains(post.ContentHTML, "needle") {
							t.Fatal("query ordering or content changed")
						}
					}
				}
				run()
				runtime.GC()
				runtime.GC()
				var before, after, first, second runtime.MemStats
				runtime.ReadMemStats(&before)
				const batch = 64
				var batchMS, perQueryMS [6]float64
				for i := range batchMS {
					began := time.Now()
					for range batch {
						run()
					}
					batchMS[i] = float64(time.Since(began).Nanoseconds()) / 1e6
					perQueryMS[i] = batchMS[i] / batch
				}
				runtime.ReadMemStats(&after)
				runtime.GC()
				runtime.ReadMemStats(&first)
				runtime.GC()
				runtime.ReadMemStats(&second)
				if !reflect.DeepEqual(page, want) || fmt.Sprintf("%x", autosaveMixedDigest(t, file.state)) != fixture {
					t.Fatal("query changed output or persisted data")
				}
				report, err := json.Marshal(map[string]any{"shape": shape.name, "operation": operation, "batch_operations": batch,
					"batch_ms": batchMS, "milliseconds": perQueryMS, "operations": batch * len(batchMS),
					"fixture_sha256": fixture, "result_sha256": resultHash, "allocated_bytes": after.TotalAlloc - before.TotalAlloc,
					"heap_before": before.HeapAlloc, "heap_after": after.HeapAlloc, "heap_after_gc": first.HeapAlloc,
					"heap_after_second_gc": second.HeapAlloc, "gc_cycles": after.NumGC - before.NumGC})
				if err != nil {
					t.Fatal(err)
				}
				t.Log("SEARCH_NORMALIZED_FIELDS " + string(report))
				runtime.KeepAlive(file)
			}
		})
	}
}
