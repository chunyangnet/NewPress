package content

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPersonalSelectionsFilterBeforePaging(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			var repo Repository = NewMemoryRepository(false)
			if mode == "file" {
				var err error
				repo, err = NewFileRepository(filepath.Join(t.TempDir(), "content.json"), false)
				if err != nil {
					t.Fatal(err)
				}
			}
			first, err := repo.Create(t.Context(), PostInput{Title: "First", Slug: "first", AuthorID: 1, Status: StatusPublish, PublishedAt: time.Now().Add(-time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			second, err := repo.Create(t.Context(), PostInput{Title: "Second", Slug: "second", AuthorID: 2, Status: StatusPublish, PublishedAt: time.Now().Add(-time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			hidden, err := repo.Create(t.Context(), PostInput{Title: "Private", Slug: "private", AuthorID: 1, Status: StatusPrivate})
			if err != nil {
				t.Fatal(err)
			}
			got, err := repo.SearchPublished(t.Context(), PublishedQuery{PostIDs: []int64{hidden.ID, first.ID, second.ID}, Sort: "selection", Limit: 1, Offset: 1})
			if err != nil || got.Total != 2 || len(got.Items) != 1 || got.Items[0].ID != second.ID {
				t.Fatalf("selected page %#v %v", got, err)
			}
			for _, query := range []PublishedQuery{{PostIDs: []int64{}}, {AuthorIDs: []int64{}}, {AuthorIDs: []int64{9}}} {
				got, err = repo.SearchPublished(t.Context(), query)
				if err != nil || got.Total != 0 {
					t.Fatalf("empty selection %#v %v", got, err)
				}
			}
			got, err = repo.SearchPublished(t.Context(), PublishedQuery{AuthorIDs: []int64{1}})
			if err != nil || got.Total != 1 || got.Items[0].ID != first.ID {
				t.Fatalf("follow feed %#v %v", got, err)
			}
		})
	}
}
