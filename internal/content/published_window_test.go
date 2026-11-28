package content

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func paginationFixture(data []byte) *MemoryRepository {
	r := NewMemoryRepository(false)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	for n, value := range data {
		post := Post{ID: int64(n + 1), Version: 1, Type: TypePost, Status: StatusPublish,
			Slug: fmt.Sprintf("page-%d", n), Title: []string{"ALPHA", "alpha", "É", "中文", "needle"}[int(value)%5],
			AuthorID: int64(value % 3), AuthorName: "legacy", Excerpt: "summary", ContentHTML: "<p>needle 中文</p>",
			PublishedAt: now.Add(-time.Duration(value%7) * time.Hour), UpdatedAt: now.Add(-time.Duration(value%11) * time.Minute),
			Categories: []Term{{ID: 1, Taxonomy: TaxonomyCategory, Slug: "reading", Name: "深度 阅读"}},
			Tags:       []Term{{ID: 2, Taxonomy: TaxonomyTag, Slug: "tag", Name: "tag"}},
			Meta:       map[string]any{"pinned": value%13 == 0, "nested": map[string]any{"value": "original"}},
		}
		if value%5 == 0 {
			post.Status = []string{StatusPrivate, StatusDraft, StatusTrash, StatusPending, StatusFuture}[int(value/5)%5]
		}
		if value%17 == 0 {
			post.Type = TypeFlash
			ApplyFlashMetadata(post.Meta, PostInput{Flash: &FlashFields{Important: value%2 == 0}})
		}
		if value%19 == 0 {
			post.Status, post.PublishedAt = StatusFuture, now.Add(time.Hour)
		}
		if value%23 == 0 {
			post.ContentHTML, post.Categories = "<p>other</p>", nil
		}
		r.posts = append(r.posts, post)
	}
	return r
}

func assertPublishedPagination(t testing.TB, r *MemoryRepository, query PublishedQuery) {
	t.Helper()
	want, wantErr := referencePublishedSearch(r, context.Background(), query)
	// Exercise both repository entry points over the same canonical state.
	file := &FileRepository{state: r}
	file.searchState.Store(r)
	for _, store := range []Repository{r, file} {
		got, err := store.SearchPublished(context.Background(), query)
		if (err == nil) != (wantErr == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("query=%+v got=%+v err=%v want=%+v err=%v", query, got, err, want, wantErr)
		}
	}
}

func TestPublishedPaginationMatchesFullCollection(t *testing.T) {
	random := rand.New(rand.NewPCG(3154, 20261127))
	data := make([]byte, 257)
	for i := range data {
		data[i] = byte(random.IntN(256))
	}
	r := paginationFixture(data)
	selection := make([]int64, len(data))
	for i, index := range random.Perm(len(data)) {
		selection[i] = int64(index + 1)
	}
	filters := []PublishedQuery{
		{}, {Search: "needle"}, {Search: "missing"}, {Category: "深度\u00a0阅读"},
		{AuthorID: 1}, {LegacyAuthor: "legacy"}, {AuthorIDs: []int64{1, 2}}, {AuthorIDs: []int64{}},
		{PostIDs: selection[:17]}, {PostIDs: []int64{}}, {IgnorePinning: true},
		{Type: TypeFlash}, {Type: TypeFlash, Important: true}, {IncludeFlash: true},
	}
	count := 0
	for _, sort := range []string{"", "published", "oldest", "updated", "title", "relevance", "selection"} {
		for _, query := range filters {
			query.Sort = sort
			if sort == "selection" && query.PostIDs == nil {
				query.PostIDs = selection
			}
			for _, offset := range []int{-1, 0, 1, 19, 127, 256, 257, math.MaxInt} {
				query.Offset = offset
				for _, limit := range []int{-1, 1, 20, 100, math.MaxInt} {
					query.Limit = limit
					assertPublishedPagination(t, r, query)
					count++
				}
			}
		}
	}
	t.Logf("checked %d query combinations through both repository entry points", count)
	assertPublishedPagination(t, NewMemoryRepository(false), PublishedQuery{Offset: math.MaxInt})
	page, err := r.SearchPublished(t.Context(), PublishedQuery{Sort: "title", Limit: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	page.Items[0].Meta["nested"].(map[string]any)["value"] = "changed"
	page.Items[0].Tags[0].Name = "changed"
	for _, post := range r.posts {
		if post.Meta["nested"].(map[string]any)["value"] != "original" || post.Tags[0].Name != "tag" {
			t.Fatal("page result aliases repository state")
		}
	}
}

func FuzzPublishedPagination(f *testing.F) {
	for _, data := range [][]byte{{}, {1}, {1, 1, 1, 1}, {0, 5, 17, 19, 23, 255}, []byte("Unicode title ties and late best candidates")} {
		f.Add(data, uint8(0), uint16(0), uint8(1))
		f.Add(data, uint8(4), uint16(2), uint8(20))
	}
	f.Fuzz(func(t *testing.T, data []byte, order uint8, offset uint16, limit uint8) {
		if len(data) > 512 {
			t.Skip()
		}
		r := paginationFixture(data)
		sorts := []string{"published", "oldest", "updated", "title", "relevance", "selection"}
		query := PublishedQuery{Sort: sorts[int(order)%len(sorts)], Offset: int(offset), Limit: int(limit), IncludeFlash: order&8 != 0, IgnorePinning: order&16 != 0}
		if order&1 != 0 || query.Sort == "relevance" {
			query.Search = "needle"
		}
		if query.Sort == "selection" {
			query.PostIDs = make([]int64, len(data))
			for i := range query.PostIDs {
				query.PostIDs[i] = int64(len(data) - i)
			}
		}
		assertPublishedPagination(t, r, query)
	})
}

func TestPublishedPaginationConcurrentStatusAndReopen(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			r, _, reopen := unicodeWriteRepository(t, mode)
			var mutations []BulkMutation
			for i := range 48 {
				post, err := r.Create(t.Context(), PostInput{Title: fmt.Sprintf("needle %02d", (i*37)%48),
					Slug: fmt.Sprintf("window-%d", i), Status: StatusPublish, Excerpt: "summary", ContentSource: "<p>needle 中文</p>",
					PublishedAt: time.Date(2026, 1, 1, i%12, 0, 0, 0, time.UTC), Pinned: new(i%5 == 0)})
				if err != nil {
					t.Fatal(err)
				}
				mutations = append(mutations, BulkMutation{ID: post.ID, ExpectedVersion: post.Version})
			}
			queries := []PublishedQuery{{Limit: 5, Offset: 9}, {Sort: "title", Limit: 5, Offset: 9},
				{Search: "needle", Limit: 5, Offset: 9}, {Sort: "oldest", Limit: 5, Offset: 9}}
			expected := make([]PublishedPage, len(queries))
			for i, q := range queries {
				var err error
				expected[i], err = r.SearchPublished(t.Context(), q)
				if err != nil || expected[i].Total != 48 {
					t.Fatal(expected[i].Total, err)
				}
			}
			var readers sync.WaitGroup
			start := make(chan struct{})
			for worker := range 4 {
				readers.Go(func() {
					<-start
					for range 50 {
						page, err := r.SearchPublished(t.Context(), queries[worker])
						if err != nil || page.Total != 0 && page.Total != 48 || page.Total == 0 && len(page.Items) != 0 || page.Total == 48 && len(page.Items) != 5 {
							t.Error("partial public snapshot", page.Total, len(page.Items), err)
							continue
						}
						for i, post := range page.Items {
							if post.ID != expected[worker].Items[i].ID || post.Status != StatusPublish {
								t.Error("mixed ordering or visibility", post.ID)
							}
						}
					}
				})
			}
			close(start)
			for i := range 20 {
				status := []string{StatusPrivate, StatusPublish}[i%2]
				if _, err := r.BulkChangeStatus(t.Context(), BulkStatusChange{Items: mutations, Status: status}); err != nil {
					t.Error(err)
					break
				}
				for j := range mutations {
					mutations[j].ExpectedVersion++
				}
			}
			readers.Wait()
			r = reopen()
			for i, q := range queries {
				page, err := r.SearchPublished(t.Context(), q)
				if err != nil || page.Total != 48 || len(page.Items) != 5 {
					t.Fatal("reopened page", page.Total, err)
				}
				for j, post := range page.Items {
					if post.ID != expected[i].Items[j].ID {
						t.Fatal("reopened ordering changed")
					}
				}
			}
		})
	}
}

// High-match queries expose result copying and ordering costs. Both production
// versions receive identical fixtures, warm body caches and expected pages.
func BenchmarkPublishedPagination(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			r := NewMemoryRepository(false)
			stamp := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			base, err := r.Create(b.Context(), PostInput{Title: "ordinary", Slug: "benchmark", Status: StatusPublish, Excerpt: "summary", ContentSource: "<p>needle 中文正文</p>", PublishedAt: stamp})
			if err != nil {
				b.Fatal(err)
			}
			r.posts = nil
			r.revisions = make(map[int64][]Revision)
			for i := range count {
				post := clonePost(base)
				post.ID, post.Slug = int64(i+1), fmt.Sprintf("page-%d", i)
				post.Title = fmt.Sprintf("Article %05d 中文 É", (i*3571)%count)
				post.CreatedAt = stamp
				post.PublishedAt = stamp.Add(time.Duration(i%17) * time.Minute)
				post.UpdatedAt = stamp.Add(time.Duration(i%31) * time.Minute)
				post.Meta["pinned"] = i%29 == 0
				r.posts = append(r.posts, post)
			}
			r.nextID = int64(count + 1)
			payload, err := encodeContentDocument(r)
			if err != nil {
				b.Fatal(err)
			}
			path := filepath.Join(b.TempDir(), "content.json")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				b.Fatal(err)
			}
			file, err := NewFileRepository(path, false)
			if err != nil {
				b.Fatal(err)
			}
			for _, backend := range []struct {
				name  string
				store Repository
			}{{"memory", r}, {"file", file}} {
				b.Run(backend.name, func(b *testing.B) {
					for _, sort := range []string{"published", "title", "relevance"} {
						for _, offset := range []int{0, count / 2, count - 20} {
							b.Run(fmt.Sprintf("%s/%d", sort, offset), func(b *testing.B) {
								query := PublishedQuery{Sort: sort, Offset: offset, Limit: 20}
								if sort == "relevance" {
									query.Search = "needle"
								}
								want, err := referencePublishedSearch(r, b.Context(), query)
								if err != nil {
									b.Fatal(err)
								}
								if _, err := backend.store.SearchPublished(b.Context(), query); err != nil {
									b.Fatal(err)
								}
								b.ReportAllocs()
								b.ResetTimer()
								for b.Loop() {
									page, err := backend.store.SearchPublished(b.Context(), query)
									if err != nil || page.Total != count || len(page.Items) != len(want.Items) {
										b.Fatal(page.Total, err)
									}
									for i := range page.Items {
										if page.Items[i].ID != want.Items[i].ID {
											b.Fatal("page order changed")
										}
									}
								}
							})
						}
					}
				})
			}
		})
	}
}
