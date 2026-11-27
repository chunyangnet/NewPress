package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestPostgreSQLRedirectTransactions(t *testing.T) {
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
	prefix := fmt.Sprintf("seo-%d", time.Now().UnixNano())
	ids := []int64{}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool.Exec(cleanup, `DELETE FROM np_outbox WHERE topic='notification.post' AND (payload->>'post_id')::bigint=ANY($1::bigint[])`, ids)
		pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=ANY($1::bigint[])`, ids)
	}()
	p, err := r.Create(ctx, content.PostInput{Title: "旧文章", Slug: prefix, Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	ids = append(ids, p.ID)
	revisions, _ := r.ListRevisions(ctx, p.ID, 10)
	first := revisions[0]
	manual, err := r.SaveRedirect(ctx, 0, content.Mutation{}, content.RedirectInput{Path: "/旧文章/" + prefix + "/", PostID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	p, err = r.Update(ctx, p.ID, content.Mutation{ExpectedVersion: p.Version}, content.PostInput{Title: "新文章", Slug: prefix + "-new", Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	r = NewPostRepository(pool)
	for _, path := range []string{manual.Path, "/posts/" + prefix} {
		target, err := r.ResolveRedirect(ctx, path)
		if err != nil || target != "/posts/"+p.Slug {
			t.Fatal(target, err)
		}
	}
	list, err := r.ListRedirects(ctx, content.RedirectQuery{Search: prefix})
	if err != nil || list.Total != 2 {
		t.Fatal(list, err)
	}
	p, err = r.RestoreRevision(ctx, p.ID, first.ID, content.Mutation{ExpectedVersion: p.Version})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.ResolveRedirect(ctx, "/posts/"+prefix+"-new"); err != nil || got != "/posts/"+prefix {
		t.Fatal(got, err)
	}
	if _, err = r.ResolveRedirect(ctx, "/posts/"+prefix); !errors.Is(err, content.ErrNotFound) {
		t.Fatal("occupied current path redirects", err)
	}
	p, err = r.ChangeStatus(ctx, p.ID, content.Mutation{ExpectedVersion: p.Version}, content.StatusPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ResolveRedirect(ctx, manual.Path); !errors.Is(err, content.ErrNotFound) {
		t.Fatal("private metadata leaked", err)
	}
	p, err = r.ChangeStatus(ctx, p.ID, content.Mutation{ExpectedVersion: p.Version}, content.StatusPublish)
	if err != nil {
		t.Fatal(err)
	}
	occupier, err := r.Create(ctx, content.PostInput{Title: "占用已有规则地址", Slug: prefix + "-new", Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	ids = append(ids, occupier.ID)
	if _, err = r.ResolveRedirect(ctx, content.PublicPath(occupier)); !errors.Is(err, content.ErrNotFound) {
		t.Fatal(err)
	}
	_, err = r.Update(ctx, occupier.ID, content.Mutation{ExpectedVersion: occupier.Version}, content.PostInput{Title: "不能部分保存", Slug: prefix + "-moved", Status: content.StatusPublish})
	if !errors.Is(err, content.ErrRedirectConflict) {
		t.Fatal(err)
	}
	after, _ := r.GetByID(ctx, occupier.ID)
	if after.Version != occupier.Version || after.Title != occupier.Title {
		t.Fatal("failed capture changed post")
	}
	// A duplicate canonical slug aborts the transaction including the inserted
	// alias. This exercises rollback after automatic capture, not just validation.
	other, err := r.Create(ctx, content.PostInput{Title: "Rollback", Slug: prefix + "-rollback", Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	ids = append(ids, other.ID)
	_, err = r.Update(ctx, other.ID, content.Mutation{ExpectedVersion: other.Version}, content.PostInput{Title: "Collision", Slug: p.Slug, Status: content.StatusPublish})
	if !errors.Is(err, content.ErrSlugConflict) {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_content_redirects WHERE path=$1`, content.PublicPath(other)).Scan(&count); err != nil || count != 0 {
		t.Fatal("alias escaped rolled back transaction", count, err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.SaveRedirect(ctx, manual.ID, content.Mutation{ExpectedVersion: manual.Version}, content.RedirectInput{Path: "/updated/" + prefix, PostID: p.ID})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, content.ErrVersionConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	if err = r.DeleteRedirect(ctx, manual.ID, content.Mutation{ExpectedVersion: 1}); !errors.Is(err, content.ErrVersionConflict) {
		t.Fatal(err)
	}
	if err = r.DeleteRedirect(ctx, manual.ID, content.Mutation{ExpectedVersion: 2}); err != nil {
		t.Fatal(err)
	}
}
