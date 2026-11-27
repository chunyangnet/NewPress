package database

import (
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestPostgreSQLPublishedChronologyAndDefaultPinPriority(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostRepository(pool)
	base := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	pinned := true
	var ids []int64
	for _, in := range []content.PostInput{
		{Title: "旧置顶", Slug: "chronology-old", Status: content.StatusPublish, PublishedAt: base, Pinned: &pinned},
		{Title: "新文章", Slug: "chronology-new", Status: content.StatusPublish, PublishedAt: base.Add(time.Hour)},
		{Title: "同时发布", Slug: "chronology-tie", Status: content.StatusPublish, PublishedAt: base.Add(time.Hour)},
		{Title: "私密置顶", Slug: "chronology-private", Status: content.StatusPrivate, PublishedAt: base, Pinned: &pinned},
		{Title: "未来置顶", Slug: "chronology-future", Status: content.StatusFuture, PublishedAt: time.Now().Add(time.Hour), Pinned: &pinned},
	} {
		post, err := repository.Create(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, post.ID)
	}
	for _, ignorePinning := range []bool{false, true} {
		want := []int64{ids[0], ids[2], ids[1]}
		if ignorePinning {
			want = []int64{ids[2], ids[1], ids[0]}
		}
		for offset, id := range want {
			page, err := repository.SearchPublished(ctx, content.PublishedQuery{IgnorePinning: ignorePinning, Limit: 1, Offset: offset})
			if err != nil || page.Total != 3 || len(page.Items) != 1 || page.Items[0].ID != id {
				t.Fatalf("ignorePinning=%v offset=%d: page=%+v err=%v want ID %d", ignorePinning, offset, page, err, id)
			}
		}
	}
}
