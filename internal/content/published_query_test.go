package content

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPublishedSearchFiltersBeforePagination(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			var repository Repository = NewMemoryRepository(false)
			if mode == "file" {
				file, err := NewFileRepository(filepath.Join(t.TempDir(), "content.json"), false)
				if err != nil {
					t.Fatal(err)
				}
				repository = file
			}
			ctx := context.Background()
			var ids []int64
			for i := 0; i < 35; i++ {
				input := PostInput{Type: TypePost, Status: StatusPublish, Title: fmt.Sprintf("Search entry %02d", i), Slug: fmt.Sprintf("search-%02d", i), AuthorID: 7, AuthorName: "NewPress Team", Category: "深度阅读", PublishedAt: time.Now().Add(-time.Duration(100-i) * time.Hour)}
				if i == 0 {
					input.Title += " 100%_literal"
					input.TagNames = []string{"旧文章标记"}
				}
				post, err := repository.Create(ctx, input)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, post.ID)
			}
			for _, status := range []string{StatusDraft, StatusPrivate, StatusTrash, StatusPending, StatusFuture} {
				if _, err := repository.Create(ctx, PostInput{Title: "Search private 100%_literal", Slug: "hidden-" + status, Status: status, PublishedAt: time.Now().Add(time.Hour), Category: "私密分类"}); err != nil {
					t.Fatal(err)
				}
			}
			for _, needle := range []string{"100%_literal", "旧文章标记"} {
				result, err := repository.SearchPublished(ctx, PublishedQuery{Search: needle, Limit: 6})
				if err != nil || result.Total != 1 || result.Items[0].ID != ids[0] {
					t.Fatalf("older matching content = %#v, %v", result, err)
				}
			}
			second, err := repository.SearchPublished(ctx, PublishedQuery{Category: "深度阅读", AuthorID: 7, Sort: "oldest", Limit: 6, Offset: 6})
			if err != nil || second.Total != 35 {
				t.Fatalf("filtered total = %#v, %v", second, err)
			}
			got := []int64{}
			for _, post := range second.Items {
				got = append(got, post.ID)
			}
			if !slices.Equal(got, ids[6:12]) {
				t.Fatalf("second page = %v", got)
			}
			for _, query := range []PublishedQuery{{Category: "unknown"}, {Category: "私密分类"}, {AuthorID: 8}, {LegacyAuthor: "NewPress Team"}, {Offset: 1000}} {
				result, err := repository.SearchPublished(ctx, query)
				if err != nil || len(result.Items) != 0 {
					t.Fatalf("unexpected public result for %#v: %#v, %v", query, result, err)
				}
			}
			second.Items[0].Tags = []Term{{Name: "injected"}}
			fresh, _ := repository.GetByID(ctx, ids[6])
			if len(fresh.Tags) != 0 {
				t.Fatal("search result aliases canonical state")
			}
		})
	}
}

func TestPublishedSearchValidationAndAuthorRoutes(t *testing.T) {
	for _, query := range []PublishedQuery{{Search: strings.Repeat("字", 81)}, {Search: "bad\x00query"}, {Sort: "random"}, {AuthorID: -1}, {AuthorID: 7, LegacyAuthor: "Name"}, {Category: strings.Repeat("a", 161)}} {
		if _, err := NormalizePublishedQuery(query); err == nil {
			t.Fatalf("accepted %#v", query)
		}
	}
	if got := (Post{AuthorID: 17, AuthorName: "Other"}).AuthorURL(); got != "/authors/17/" {
		t.Fatal(got)
	}
	if got := (Post{AuthorName: "设计 & 开发"}).AuthorURL(); !strings.HasPrefix(got, "/explore/?q=") || strings.Contains(got, "newpress-team") {
		t.Fatal(got)
	}
}
