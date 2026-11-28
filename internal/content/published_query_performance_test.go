package content

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestPublishedTitleOrderingAndCategoryNormalization(t *testing.T) {
	r := NewMemoryRepository(false)
	now := time.Now().Add(-time.Hour)
	for i, title := range []string{" Zulu ", "éclair", "ALPHA", "alpha", "ÉCLAIR", "中文"} {
		r.posts = append(r.posts, Post{ID: int64(i + 1), Type: TypePost, Status: StatusPublish,
			Title: title, PublishedAt: now, Categories: []Term{{Name: "深度  阅读", Slug: "reading"}}})
	}
	r.posts = append(r.posts, Post{ID: 7, Type: TypePost, Status: StatusPrivate, Title: "aaa", PublishedAt: now})
	for _, category := range []string{"reading", "深度\u00a0阅读"} {
		page, err := r.SearchPublished(t.Context(), PublishedQuery{Sort: "title", Category: category, Offset: 1, Limit: 4})
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, p := range page.Items {
			ids = append(ids, p.ID)
		}
		if page.Total != 6 || !slices.Equal(ids, []int64{3, 1, 5, 2}) {
			t.Fatalf("category %q: total=%d ids=%v", category, page.Total, ids)
		}
	}
}

func BenchmarkPublishedTitlePage(b *testing.B) {
	r := NewMemoryRepository(false)
	now := time.Now().Add(-time.Hour)
	for i := range 5000 {
		r.posts = append(r.posts, Post{ID: int64(i + 1), Type: TypePost, Status: StatusPublish,
			Title: fmt.Sprintf("Article %04d — 中文 ÉCLAIR", (i*3571)%5000), PublishedAt: now,
			Categories: []Term{{Name: "技术 阅读", Slug: "technology"}}})
	}
	query := PublishedQuery{Sort: "title", Category: "技术\u00a0阅读", Limit: 20}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		page, err := r.SearchPublished(context.Background(), query)
		if err != nil || page.Total != 5000 || len(page.Items) != 20 {
			b.Fatal(page.Total, err)
		}
	}
}
