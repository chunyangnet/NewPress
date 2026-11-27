package content

import (
	"testing"
	"time"
)

func TestImplicitSlugDistinguishesNonASCIITitlesAtSameInstant(t *testing.T) {
	repository := NewMemoryRepository(false)
	repository.now = func() time.Time { return time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) }
	seen := map[string]bool{}
	for _, title := range []string{"站点使用指南", "整理本周内容", "社区精选", "下月发布内容", "😀"} {
		post, err := repository.Create(t.Context(), PostInput{Title: title, Type: TypePost, Status: StatusDraft})
		if err != nil {
			t.Fatalf("create %q without an explicit slug: %v", title, err)
		}
		if !ValidSlug(post.Slug) || seen[post.Slug] {
			t.Fatalf("invalid or colliding generated slug: %q", post.Slug)
		}
		seen[post.Slug] = true
	}
	input := PostInput{Title: "站点使用指南", Type: TypePost, Status: StatusDraft}
	first, _ := NormalizePostInput(input, repository.now())
	second, _ := NormalizePostInput(input, repository.now())
	if first.Slug != second.Slug {
		t.Fatal("normalization must be stable for the same input and instant")
	}
}
