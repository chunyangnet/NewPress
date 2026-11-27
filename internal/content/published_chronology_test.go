package content

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPublishedChronologyIgnoresPinningBeforePagination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	repository, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-24 * time.Hour)
	pinned := true
	var ids []int64
	for _, in := range []PostInput{
		{Title: "旧置顶", Slug: "chronology-old", Status: StatusPublish, PublishedAt: base, Pinned: &pinned},
		{Title: "新文章", Slug: "chronology-new", Status: StatusPublish, PublishedAt: base.Add(time.Hour)},
		{Title: "同时发布", Slug: "chronology-tie", Status: StatusPublish, PublishedAt: base.Add(time.Hour)},
		{Title: "私密置顶", Slug: "chronology-private", Status: StatusPrivate, PublishedAt: base, Pinned: &pinned},
	} {
		post, err := repository.Create(t.Context(), in)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, post.ID)
	}
	repository, err = NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, ignorePinning := range []bool{false, true} {
		want := []int64{ids[0], ids[2], ids[1]}
		if ignorePinning {
			want = []int64{ids[2], ids[1], ids[0]}
		}
		for offset, id := range want {
			page, err := repository.SearchPublished(t.Context(), PublishedQuery{IgnorePinning: ignorePinning, Limit: 1, Offset: offset})
			if err != nil || page.Total != 3 || len(page.Items) != 1 || page.Items[0].ID != id {
				t.Fatalf("ignorePinning=%v offset=%d: page=%+v err=%v want ID %d", ignorePinning, offset, page, err, id)
			}
		}
	}
}
