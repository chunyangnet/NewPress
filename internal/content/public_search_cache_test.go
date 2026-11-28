package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPublicSearchBodyCacheBoundsAndEviction(t *testing.T) {
	cache := &publicSearchBodyCache{maxEntries: 2, maxBytes: 8}
	for _, body := range []string{"<p>ABCD</p>", "<p>EFGH</p>"} {
		if got := cache.body(body); got != PublicSearchBody(body) {
			t.Fatalf("projection changed: %q", got)
		}
	}
	cache.body("<p>ABCD</p>") // Touch the older entry before admitting another.
	cache.body("<p>中文</p>")
	if cache.bytes != 6 || len(cache.entries) != 1 || cache.entries[sha256.Sum256([]byte("<p>中文</p>"))] == nil {
		t.Fatal("byte budget must evict enough complete entries")
	}
	cache.body("<!-- empty -->")
	cache.body("<p>中文</p>")
	cache.body("<img alt='unsearchable'>")
	if len(cache.entries) != 2 || cache.bytes != 6 || cache.entries[sha256.Sum256([]byte("<!-- empty -->"))] != nil {
		t.Fatal("empty projections must count toward the entry limit and respect recency")
	}
	oversized := "<p>" + strings.Repeat("word", 20) + "</p>"
	if got := cache.body(oversized); got != PublicSearchBody(oversized) || cache.bytes != 6 || len(cache.entries) != 2 {
		t.Fatal("oversized bodies must remain searchable without displacing the cache")
	}
	for _, disabled := range []*publicSearchBodyCache{nil, {}, {maxEntries: 1}, {maxBytes: 4}} {
		if got := disabled.body("<p>É &amp; 中</p>"); got != "é & 中" {
			t.Fatal("disabled cache changed search semantics", got)
		}
	}
}

func TestPublicSearchBodyDigestMatchesCompleteBytes(t *testing.T) {
	for _, size := range []int{0, 1, 31, 32, 33, 4095, 4096, 4097, 2 << 20} {
		body := strings.Repeat("中文 É &amp; ", size/15+1)[:size]
		if got := publicSearchBodyDigest(body); got != sha256.Sum256([]byte(body)) {
			t.Fatal("chunked digest changed byte identity at length", size)
		}
	}
}

func TestPublicSearchBodyCacheConcurrentChurn(t *testing.T) {
	cache := &publicSearchBodyCache{maxEntries: 17, maxBytes: 512}
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Go(func() {
			for i := range 150 {
				body := fmt.Sprintf("<p>%d:%d 中<em>文</em> &amp; É</p><!--hidden-->", worker, i%31)
				if got := cache.body(body); got != PublicSearchBody(body) {
					t.Error("concurrent projection mismatch", got)
				}
			}
		})
	}
	workers.Wait()
	total := 0
	for _, element := range cache.entries {
		total += len(element.Value.(publicSearchBodyEntry).text)
	}
	if total != cache.bytes || total > cache.maxBytes || len(cache.entries) > cache.maxEntries || cache.recent.Len() != len(cache.entries) {
		t.Fatal("concurrent eviction lost the cache bound")
	}
}

func TestPublicSearchCachedBodyKeepsCurrentMetadataAndVisibility(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			r, persisted, reopen := unicodeWriteRepository(t, mode)
			input := PostInput{Title: "ordinary", Slug: "cached-body", Status: StatusPublish, Excerpt: "summary", ContentSource: "<p>独立正文 &amp; original</p>", Category: "旧分类", TagNames: []string{"旧标签"}, AuthorName: "旧署名", PublishedAt: time.Now().Add(-time.Hour)}
			post, err := r.Create(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			search := func(needle string, want int) {
				t.Helper()
				page, err := r.SearchPublished(t.Context(), PublishedQuery{Search: needle})
				if err != nil || page.Total != want || len(page.Items) != want {
					t.Fatalf("%s: total %d, want %d, error %v", needle, page.Total, want, err)
				}
			}
			before := persisted()
			search("独立正文", 1)
			search("original", 1)
			if !bytes.Equal(before, persisted()) {
				t.Fatal("search persisted derived data")
			}
			for _, term := range []Term{post.Categories[0], post.Tags[0]} {
				if _, err = r.UpdateTerm(t.Context(), term.ID, TermMutation{ExpectedVersion: term.Version}, TermInput{Taxonomy: term.Taxonomy, Slug: term.Slug, Name: "新" + term.Name}); err != nil {
					t.Fatal(err)
				}
				search("新"+term.Name, 1)
			}
			// Term deletion does not increment the post version. A cache keyed by
			// post version or caching entire search results would miss this change.
			for _, term := range []Term{post.Categories[0], post.Tags[0]} {
				if err = r.DeleteTerm(t.Context(), term.ID, TermMutation{ExpectedVersion: term.Version + 1}); err != nil {
					t.Fatal(err)
				}
				search(term.Name, 0)
			}
			input.Category, input.TagNames, input.AuthorName = "", nil, "新署名"
			post, err = r.Update(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version}, input)
			if err != nil {
				t.Fatal(err)
			}
			search("旧署名", 0)
			search("新署名", 1)
			search("独立正文", 1)
			for _, status := range []string{StatusPrivate, StatusPublish, StatusDraft, StatusPublish, StatusPending, StatusPublish, StatusTrash} {
				post, err = r.ChangeStatus(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version}, status)
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if status == StatusPublish {
					want = 1
				}
				search("独立正文", want)
			}
			if _, err = r.BulkChangeStatus(t.Context(), BulkStatusChange{Delete: true, Status: StatusTrash, Items: []BulkMutation{{ID: post.ID, ExpectedVersion: post.Version}}}); err != nil {
				t.Fatal(err)
			}
			search("独立正文", 0)
			r = reopen()
			search("独立正文", 0)
		})
	}
}

func TestPublicSearchCachedBodyUsesCurrentHTMLAndPublicationTime(t *testing.T) {
	r := NewMemoryRepository(false)
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return stamp }
	p, err := r.Create(t.Context(), PostInput{Title: "ordinary", Slug: "time", Status: StatusFuture, Excerpt: "summary", ContentSource: "<p>alpha</p>", PublishedAt: stamp.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	r.searchBodies.body(p.ContentHTML)
	assertTotal := func(query string, want int) {
		t.Helper()
		page, err := r.SearchPublished(t.Context(), PublishedQuery{Search: query})
		if err != nil || page.Total != want {
			t.Fatal(query, page.Total, want, err)
		}
	}
	assertTotal("alpha", 0)
	stamp = stamp.Add(2 * time.Hour)
	assertTotal("alpha", 1)
	// Keep ID, version and timestamps unchanged to prove the derived projection
	// depends on the actual HTML, including after an older snapshot is restored.
	r.posts[0].ContentHTML = "<p>bravo</p>"
	assertTotal("alpha", 0)
	assertTotal("bravo", 1)
	r.posts[0].ContentHTML = p.ContentHTML
	assertTotal("alpha", 1)
	assertTotal("bravo", 0)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.SearchPublished(ctx, PublishedQuery{Search: "alpha"}); !errors.Is(err, context.Canceled) {
		t.Fatal("cached search ignored cancellation", err)
	}
}

func TestPublicSearchFileCacheAcrossCommitFailureAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	r, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	input := PostInput{Title: "ordinary", Slug: "commit", Excerpt: "summary", ContentSource: "<p>oldbody</p>", Status: StatusPublish}
	p, err := r.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.SearchPublished(t.Context(), PublishedQuery{Search: "oldbody"}); err != nil {
		t.Fatal(err)
	}
	cache := r.state.searchBodies
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path+".bak", 0700); err != nil {
		t.Fatal(err)
	}
	input.ContentSource = "<p>newbody</p>"
	if _, err = r.Update(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, input); err == nil {
		t.Fatal("expected durable replacement failure")
	}
	for needle, want := range map[string]int{"oldbody": 1, "newbody": 0} {
		page, err := r.SearchPublished(t.Context(), PublishedQuery{Search: needle})
		if err != nil || page.Total != want {
			t.Fatal("failed candidate leaked through search", needle, page, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed transaction changed disk", err)
	}
	if err = os.Remove(path + ".bak"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Update(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, input); err != nil {
		t.Fatal(err)
	}
	if r.state.searchBodies != cache {
		t.Fatal("unrelated cached bodies were discarded by a file transaction")
	}
	for _, reopen := range []bool{false, true} {
		if reopen {
			r, err = NewFileRepository(path, false)
			if err != nil {
				t.Fatal(err)
			}
			if r.state.searchBodies == cache || len(r.state.searchBodies.entries) != 0 {
				t.Fatal("derived cache was persisted or shared across file opens")
			}
		}
		for needle, want := range map[string]int{"oldbody": 0, "newbody": 1} {
			page, err := r.SearchPublished(t.Context(), PublishedQuery{Search: needle})
			if err != nil || page.Total != want {
				t.Fatal("committed/reopened body mismatch", needle, page, err)
			}
		}
	}
}

func TestPublicSearchConcurrentQueriesAndEdits(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			r, _, _ := unicodeWriteRepository(t, mode)
			input := PostInput{Title: "ordinary", Slug: "concurrent-search", Excerpt: "summary", ContentSource: "<p>alpha</p>", Status: StatusPublish}
			post, err := r.Create(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			var workers sync.WaitGroup
			start := make(chan struct{})
			for range 4 {
				workers.Go(func() {
					<-start
					for range 60 {
						for _, query := range []string{"alpha", "bravo"} {
							page, err := r.SearchPublished(t.Context(), PublishedQuery{Search: query})
							if err != nil || page.Total != len(page.Items) || page.Total > 1 {
								t.Error("inconsistent query snapshot", page, err)
							}
							for _, result := range page.Items {
								if result.Status != StatusPublish || !strings.Contains(PublicSearchBody(result.ContentHTML), query) {
									t.Error("stale body or visibility", result)
								}
							}
						}
					}
				})
			}
			close(start)
			for i := range 30 {
				input.ContentSource = "<p>" + []string{"alpha", "bravo"}[i%2] + "</p>"
				input.Status = StatusPublish
				if i%3 == 0 {
					input.Status = StatusPrivate
				}
				post, err = r.Update(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version}, input)
				if err != nil {
					t.Error(err)
					break
				}
			}
			workers.Wait()
		})
	}
}
