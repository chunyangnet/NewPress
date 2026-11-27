package content

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestMemoryPublishedPaginationVisibilityOrderingAndIsolation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository(false)
	repository.now = func() time.Time { return now }
	create := func(slug, postType, status string, published time.Time) Post {
		t.Helper()
		post, err := repository.Create(ctx, PostInput{
			Type: postType, Status: status, Slug: slug, Title: slug,
			ContentSource: "body", Category: "Pagination", PublishedAt: published,
		})
		if err != nil {
			t.Fatal(err)
		}
		return post
	}
	oldest := create("oldest", TypePost, StatusPublish, now.Add(-time.Hour))
	first := create("same-time-first", TypePost, StatusPublish, now)
	second := create("same-time-second", TypePost, StatusPublish, now)
	due := create("scheduled-due", TypePost, StatusFuture, now.Add(time.Hour))
	create("scheduled-later", TypePost, StatusFuture, now.Add(3*time.Hour))
	create("draft", TypePost, StatusDraft, now)
	create("private", TypePost, StatusPrivate, now)
	create("trashed", TypePost, StatusTrash, now)
	create("static-page", TypePage, StatusPublish, now)

	before, err := repository.ListPublishedPage(ctx, 20, 0)
	if err != nil || before.Total != 3 {
		t.Fatalf("before scheduled publication = %#v, %v", before, err)
	}
	now = now.Add(2 * time.Hour)
	for _, test := range []struct {
		name          string
		limit, offset int
		ids           []int64
	}{
		{"first page", 2, 0, []int64{due.ID, second.ID}},
		{"second page", 2, 2, []int64{first.ID, oldest.ID}},
		{"partial page", 3, 3, []int64{oldest.ID}},
		{"past end", 2, 100, []int64{}},
		{"default limit and offset", 0, -1, []int64{due.ID, second.ID, first.ID, oldest.ID}},
		{"oversized limit", 101, 0, []int64{due.ID, second.ID, first.ID, oldest.ID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			page, err := repository.ListPublishedPage(ctx, test.limit, test.offset)
			if err != nil || page.Total != 4 || page.Items == nil {
				t.Fatalf("page = %#v, %v", page, err)
			}
			ids := make([]int64, 0, len(page.Items))
			for _, post := range page.Items {
				ids = append(ids, post.ID)
			}
			if !slices.Equal(ids, test.ids) {
				t.Fatalf("post IDs = %v, want %v", ids, test.ids)
			}
		})
	}
	page, err := repository.ListPublishedPage(ctx, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	page.Items[0].Title = "changed"
	page.Items[0].Categories[0].Name = "changed"
	stored, err := repository.GetByID(ctx, due.ID)
	if err != nil || stored.Title != due.Title || stored.Categories[0].Name != "Pagination" {
		t.Fatalf("page mutations reached repository: %#v, %v", stored, err)
	}
	listed, err := repository.ListPublished(ctx, 2)
	if err != nil || len(listed) != 2 || listed[0].ID != due.ID || listed[1].ID != second.ID {
		t.Fatalf("unpaginated ordering differs: %#v, %v", listed, err)
	}
	empty, err := NewMemoryRepository(false).ListPublishedPage(ctx, 8, 0)
	if err != nil || empty.Total != 0 || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("empty repository page = %#v, %v", empty, err)
	}
}
