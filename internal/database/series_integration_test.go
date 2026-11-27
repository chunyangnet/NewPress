package database

import (
	"context"
	"errors"
	"fmt"
	"newpress.local/newpress/internal/content"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestPostgreSQLSeries(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	stamp := fmt.Sprintf("series-%d", time.Now().UnixNano())
	ids := []int64{}
	var seriesID int64
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool.Exec(cleanup, `DELETE FROM np_series WHERE id=$1`, seriesID)
		pool.Exec(cleanup, `DELETE FROM np_outbox WHERE topic='notification.post' AND (payload->>'post_id')::bigint=ANY($1::bigint[])`, ids)
		pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=ANY($1::bigint[])`, ids)
	}()
	posts := []content.Post{}
	for i, status := range []string{content.StatusPublish, content.StatusPrivate, content.StatusPublish, content.StatusFuture} {
		p, err := r.Create(ctx, content.PostInput{Title: status, Slug: fmt.Sprintf("%s-%d", stamp, i), Status: status, PublishedAt: time.Now().Add(time.Duration(i-2) * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		posts = append(posts, p)
		ids = append(ids, p.ID)
	}
	input := content.SeriesInput{Kind: content.SeriesKindSequence, Status: content.StatusPublish, Title: "系列集成测试", Slug: stamp, CoverURL: "/theme-assets/images/cover-core.svg", PostIDs: []int64{ids[2], ids[1], ids[0], ids[3]}}
	item, err := r.SaveSeries(ctx, 0, content.Mutation{}, input)
	if err != nil {
		t.Fatal(err)
	}
	seriesID = item.ID
	r = NewPostRepository(pool)
	pub, chapters, err := r.PublishedSeries(ctx, stamp)
	if err != nil || len(chapters) != 2 || !slices.Equal(pub.PostIDs, []int64{ids[2], ids[0]}) {
		t.Fatal(pub, chapters, err)
	}
	list, err := r.ListSeries(ctx, content.SeriesQuery{PublicOnly: true, Search: "系列集成测试"})
	if err != nil || list.Total != 1 || list.Items[0].Count() != 2 {
		t.Fatal(list, err)
	}
	list, err = r.ListSeries(ctx, content.SeriesQuery{PublicOnly: true, PostID: ids[1]})
	if err != nil || list.Total != 0 {
		t.Fatal("private membership exposed", list, err)
	}
	input.PostIDs = []int64{ids[0], ids[2]}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.SaveSeries(ctx, item.ID, content.Mutation{ExpectedVersion: item.Version}, input)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	success, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, content.ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal(success, conflicts)
	}
	current, err := r.GetSeries(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PostIDs = []int64{ids[0], 99999999}
	if _, err = r.SaveSeries(ctx, item.ID, content.Mutation{ExpectedVersion: current.Version}, input); len(content.FieldErrors(err)) == 0 {
		t.Fatal("invalid reference accepted", err)
	}
	after, _ := r.GetSeries(ctx, item.ID)
	if after.Version != current.Version || !slices.Equal(after.PostIDs, current.PostIDs) {
		t.Fatal("transaction partially committed")
	}
	var found bool
	if err = r.WalkMediaSnapshots(ctx, func(s content.MediaSnapshot) error {
		if s.Source == content.MediaSourceSeries && s.PostID == item.ID && s.CoverURL == input.CoverURL {
			found = true
		}
		return nil
	}); err != nil || !found {
		t.Fatal("series cover absent from scan", err)
	}
	if _, err = r.ChangeStatus(ctx, ids[0], content.Mutation{ExpectedVersion: posts[0].Version}, content.StatusPrivate); err != nil {
		t.Fatal(err)
	}
	_, chapters, err = r.PublishedSeries(ctx, stamp)
	if err != nil || len(chapters) != 1 || chapters[0].ID != ids[2] {
		t.Fatal("withdrawal not filtered", err)
	}
	if err = r.DeleteSeries(ctx, item.ID, content.Mutation{ExpectedVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetByID(ctx, ids[2]); err != nil {
		t.Fatal("article deleted with series", err)
	}
}
