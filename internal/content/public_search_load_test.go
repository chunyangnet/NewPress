package content

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublicSearchFileMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_PUBLIC_SEARCH_LOAD") != "1" {
		t.Skip("opt-in public search scale measurement")
	}
	for _, size := range []struct{ rows, bytes int }{{1000, 1024}, {10000, 1024}, {1000, 16384}} {
		t.Run(fmt.Sprintf("%d-%d", size.rows, size.bytes), func(t *testing.T) {
			state := NewMemoryRepository(false)
			base, err := state.Create(t.Context(), PostInput{Title: "ordinary", Slug: "search-scale", Status: StatusPublish, Excerpt: "summary", ContentSource: "<p>" + strings.Repeat("plain text ", size.bytes/11) + "</p>", PublishedAt: time.Now().Add(-time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			state.posts = nil
			state.revisions = make(map[int64][]Revision)
			for i := 1; i <= size.rows; i++ {
				post := clonePost(base)
				post.ID, post.Slug = int64(i), fmt.Sprintf("scale-%d", i)
				if i%10 == 0 {
					post.Status = StatusPrivate
				}
				if i%5 == 1 {
					post.ContentSource += "<p>needle 深度正文</p>"
					post.ContentHTML = post.ContentSource
				}
				if i%100 == 1 {
					post.Title = "needle title"
				}
				state.posts = append(state.posts, post)
			}
			state.nextID = int64(size.rows + 1)
			payload, err := encodeContentDocument(state)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "content.json")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			began := time.Now()
			file, err := NewFileRepository(path, false)
			open := time.Since(began)
			if err != nil {
				t.Fatal(err)
			}
			var samples []map[string]any
			for round := range 3 {
				for _, needle := range []string{"needle", "深度正文", "missing-phrase"} {
					began = time.Now()
					page, err := file.SearchPublished(t.Context(), PublishedQuery{Search: needle})
					elapsed := time.Since(began)
					want := size.rows / 5
					if needle == "missing-phrase" {
						want = 0
					}
					if err != nil || page.Total != want {
						t.Fatal(needle, page.Total, want, err)
					}
					samples = append(samples, map[string]any{"round": round + 1, "query": needle, "milliseconds": float64(elapsed.Microseconds()) / 1000, "total": page.Total})
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || sha256.Sum256(after) != sha256.Sum256(payload) {
				t.Fatal("search changed persisted content", err)
			}
			raw, err := json.Marshal(map[string]any{"rows": size.rows, "base_html_bytes": len(base.ContentHTML), "file_bytes": len(payload), "open_milliseconds": float64(open.Microseconds()) / 1000, "samples": samples, "file_unchanged": true})
			if err != nil {
				t.Fatal(err)
			}
			t.Log("PUBLIC_SEARCH_FILE_LOAD " + string(raw))
		})
	}
}
