package content

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestSeriesOrderVisibilityPersistenceAndConflicts(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			var r Repository = NewMemoryRepository(false)
			path := filepath.Join(t.TempDir(), "content.json")
			if mode == "file" {
				var err error
				r, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			posts := []Post{}
			for i, status := range []string{StatusPublish, StatusPrivate, StatusPublish, StatusDraft, StatusFuture} {
				p, err := r.Create(t.Context(), PostInput{Title: status, Slug: status + string(rune('a'+i)), Status: status, PublishedAt: time.Now().Add(time.Duration(i-3) * time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				posts = append(posts, p)
			}
			input := SeriesInput{Kind: SeriesKindSequence, Status: StatusPublish, Title: "顺序阅读", Slug: "sequence", Description: "不会暴露隐藏章节", PostIDs: []int64{posts[2].ID, posts[1].ID, posts[0].ID, posts[3].ID, posts[4].ID}}
			item, err := r.SaveSeries(t.Context(), 0, Mutation{}, input)
			if err != nil {
				t.Fatal(err)
			}
			input.PostIDs[0] = 999 // Caller cannot mutate the stored order.
			if mode == "file" {
				r, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			pub, chapters, err := r.PublishedSeries(t.Context(), item.Slug)
			if err != nil || len(chapters) != 2 || !slices.Equal(pub.PostIDs, []int64{posts[2].ID, posts[0].ID}) {
				t.Fatal(pub, chapters, err)
			}
			list, err := r.ListSeries(t.Context(), SeriesQuery{PublicOnly: true})
			if err != nil || list.Total != 1 || list.Items[0].Count() != 2 {
				t.Fatal(list, err)
			}
			privateMembership, err := r.ListSeries(t.Context(), SeriesQuery{PublicOnly: true, PostID: posts[1].ID})
			if err != nil || privateMembership.Total != 0 {
				t.Fatal("private membership leaked", privateMembership, err)
			}
			input = item.SeriesInput
			input.PostIDs = []int64{posts[0].ID, posts[2].ID}
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := r.SaveSeries(t.Context(), item.ID, Mutation{ExpectedVersion: item.Version}, input)
					results <- err
				}()
			}
			wg.Wait()
			close(results)
			passed, conflicts := 0, 0
			for err := range results {
				if err == nil {
					passed++
				} else if errors.Is(err, ErrVersionConflict) {
					conflicts++
				} else {
					t.Fatal(err)
				}
			}
			if passed != 1 || conflicts != 1 {
				t.Fatal(passed, conflicts)
			}
			current, _ := r.GetSeries(t.Context(), item.ID)
			bad := current.SeriesInput
			bad.PostIDs = []int64{posts[0].ID, 999999}
			if _, err = r.SaveSeries(t.Context(), item.ID, Mutation{ExpectedVersion: current.Version}, bad); len(FieldErrors(err)) == 0 {
				t.Fatal("dangling chapter accepted", err)
			}
			after, _ := r.GetSeries(t.Context(), item.ID)
			if after.Version != current.Version || !slices.Equal(after.PostIDs, current.PostIDs) {
				t.Fatal("failed update changed order")
			}
			if _, err = r.ChangeStatus(t.Context(), posts[0].ID, Mutation{ExpectedVersion: posts[0].Version}, StatusTrash); err != nil {
				t.Fatal(err)
			}
			pub, chapters, err = r.PublishedSeries(t.Context(), item.Slug)
			if err != nil || len(chapters) != 1 || chapters[0].ID != posts[2].ID {
				t.Fatal("withdrawal not filtered", pub, err)
			}
			if err = r.DeleteSeries(t.Context(), item.ID, Mutation{ExpectedVersion: item.Version}); !errors.Is(err, ErrVersionConflict) {
				t.Fatal(err)
			}
			if err = r.DeleteSeries(t.Context(), item.ID, Mutation{ExpectedVersion: current.Version}); err != nil {
				t.Fatal(err)
			}
			if _, err = r.GetByID(t.Context(), posts[2].ID); err != nil {
				t.Fatal("delete removed article", err)
			}
			if _, _, err = r.PublishedSeries(t.Context(), item.Slug); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
}

func TestSeriesValidationAndFailedFileWrite(t *testing.T) {
	base := SeriesInput{Kind: SeriesKindTopic, Status: StatusDraft, Title: "专题", Slug: "topic", PostIDs: []int64{1}}
	for _, change := range []func(*SeriesInput){func(s *SeriesInput) { s.PostIDs = []int64{1, 1} }, func(s *SeriesInput) { s.PostIDs = []int64{-1} }, func(s *SeriesInput) { s.PostIDs = make([]int64, 201) }, func(s *SeriesInput) { s.Kind = "unknown" }, func(s *SeriesInput) { s.Status = StatusPrivate }, func(s *SeriesInput) { s.CoverURL = "javascript:alert(1)" }} {
		input := base
		change(&input)
		if _, fields := NormalizeSeriesInput(input); len(fields) == 0 {
			t.Fatal(input)
		}
	}
	path := filepath.Join(t.TempDir(), "content.json")
	r, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	base.PostIDs = []int64{}
	item, err := r.SaveSeries(t.Context(), 0, Mutation{}, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.PublishedSeries(t.Context(), item.Slug); !errors.Is(err, ErrNotFound) {
		t.Fatal("draft exposed")
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(bytes, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	base.Title = "不应生效"
	if _, err = r.SaveSeries(t.Context(), item.ID, Mutation{ExpectedVersion: item.Version}, base); err == nil {
		t.Fatal("external file edit ignored")
	}
	stored, _ := r.GetSeries(t.Context(), item.ID)
	if stored.Title != item.Title || stored.Version != item.Version {
		t.Fatal("failure changed in-memory state")
	}
}
