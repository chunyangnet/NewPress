package content

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Both code versions use identical unique bodies and exclude fixture creation
// and file opening from query timings. Cold means an empty process-local cache,
// not a cold filesystem or a database benchmark.
func BenchmarkPublicSearchReads(b *testing.B) {
	for _, size := range []struct{ rows, bytes int }{{1000, 1024}, {10000, 1024}, {1000, 16384}} {
		b.Run(fmt.Sprintf("%d-%d", size.rows, size.bytes), func(b *testing.B) {
			state := NewMemoryRepository(false)
			stamp := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			base, err := state.Create(b.Context(), PostInput{Title: "ordinary", Slug: "search-measure", Status: StatusPublish, Excerpt: "summary", ContentSource: "<p>plain</p>", PublishedAt: stamp})
			if err != nil {
				b.Fatal(err)
			}
			state.posts = nil
			state.revisions = make(map[int64][]Revision)
			for i := 1; i <= size.rows; i++ {
				post := clonePost(base)
				post.ID, post.Slug = int64(i), fmt.Sprintf("measure-%d", i)
				body := fmt.Sprintf("<p>article %d %s</p>", i, strings.Repeat("中文 plain ", size.bytes/13))
				if i%5 == 1 {
					body += "<p>needle 深度正文</p>"
				}
				post.ContentSource, post.ContentHTML = body, body
				if i%10 == 0 {
					post.Status = StatusPrivate
				}
				state.posts = append(state.posts, post)
			}
			state.nextID = int64(size.rows + 1)
			payload, err := encodeContentDocument(state)
			if err != nil {
				b.Fatal(err)
			}
			path := filepath.Join(b.TempDir(), "content.json")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				b.Fatal(err)
			}
			for _, backend := range []string{"memory", "file"} {
				b.Run(backend, func(b *testing.B) {
					fresh := func() Repository {
						if backend == "file" {
							repository, err := NewFileRepository(path, false)
							if err != nil {
								b.Fatal(err)
							}
							return repository
						}
						repository := NewMemoryRepository(false)
						repository.posts = state.posts
						return repository
					}
					query := func(repository Repository, index int) {
						needles := []string{"needle", "深度正文", "missing-phrase"}
						needle, want := needles[index%len(needles)], size.rows/5
						if needle == "missing-phrase" {
							want = 0
						}
						page, err := repository.SearchPublished(context.Background(), PublishedQuery{Search: needle})
						if err != nil || page.Total != want {
							b.Errorf("search %s: total=%d, want=%d, error=%v", needle, page.Total, want, err)
						}
					}
					for _, phase := range []string{"cold", "warm", "parallel"} {
						b.Run(phase, func(b *testing.B) {
							repository := fresh()
							if phase != "cold" {
								query(repository, 0)
							}
							b.ReportAllocs()
							b.ResetTimer()
							if phase == "parallel" {
								b.RunParallel(func(pb *testing.PB) {
									i := 0
									for pb.Next() {
										query(repository, i)
										i++
									}
								})
								return
							}
							for i := 0; i < b.N; i++ {
								if phase == "cold" && i > 0 {
									b.StopTimer()
									repository = fresh()
									b.StartTimer()
								}
								query(repository, i)
							}
						})
					}
				})
			}
		})
	}
}
