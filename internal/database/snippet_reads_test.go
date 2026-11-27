package database

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestPostgreSQLSnippetProjectionPluginImportRestoreAndDelete(t *testing.T) {
	f := newContentEffectFixture(t, false)
	r := NewPostRepository(f.pool)
	a := content.SnippetAccess{Identity: "legacy:test", Actor: "test", Read: true, Manage: true}
	s, err := r.SaveSnippet(f.ctx, 0, content.Mutation{}, content.SnippetInput{Title: "shared", ContentSource: "<p>body</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	body := contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1})
	f.request.Update.Patch = content.TextPatch{ContentSource: &body}
	if _, err = f.effects.WriteContent(f.ctx, f.request); err != nil {
		t.Fatal(err)
	}
	check := func(id int64, want int) {
		t.Helper()
		var got int
		var ready bool
		if err := f.pool.QueryRow(f.ctx, `SELECT snippet_refs_ready FROM np_posts WHERE id=$1`, id).Scan(&ready); err != nil || !ready {
			t.Fatal(ready, err)
		}
		if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM np_post_snippet_refs WHERE post_id=$1 AND snippet_id=$2`, id, s.ID).Scan(&got); err != nil || got != want {
			t.Fatal(got, want, err)
		}
	}
	check(f.post.ID, 1)
	batch := content.ImportBatch{Source: "snippet-projection", Format: "markdown", Items: []content.ImportEntry{{Key: "body.md", Digest: strings.Repeat("f", 64), Input: content.PostInput{Title: "import", Slug: "snippet-index-import", ContentSource: body, AuthorID: f.user.ID, Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour)}}}}
	plan, err := r.PreviewImport(f.ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ApplyImport(f.ctx, batch, plan.Revision); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = f.pool.QueryRow(f.ctx, `SELECT id FROM np_posts WHERE slug='snippet-index-import'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	check(id, 1)
	revs, err := r.ListRevisions(f.ctx, id, 10)
	if err != nil || len(revs) != 1 {
		t.Fatal(revs, err)
	}
	p, err := r.GetByID(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	p, err = r.Update(f.ctx, id, content.Mutation{ExpectedVersion: p.Version}, content.PostInput{Title: p.Title, Slug: p.Slug, AuthorID: p.AuthorID, ContentSource: "<p>plain</p>"})
	if err != nil {
		t.Fatal(err)
	}
	check(id, 0)
	p, err = r.RestoreRevision(f.ctx, id, revs[0].ID, content.Mutation{ExpectedVersion: p.Version})
	if err != nil {
		t.Fatal(err)
	}
	check(id, 1)
	p, err = r.ChangeStatus(f.ctx, id, content.Mutation{ExpectedVersion: p.Version}, content.StatusTrash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.BulkChangeStatus(f.ctx, content.BulkStatusChange{Delete: true, Status: content.StatusTrash, Items: []content.BulkMutation{{ID: id, ExpectedVersion: p.Version}}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM np_post_snippet_refs WHERE post_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestPostgreSQLSnippetProjectionUpgradeRepairAndRollback(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	a := content.SnippetAccess{Identity: "legacy:test", Actor: "test", Read: true, Manage: true}
	s, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "shared", ContentSource: "<p>shared</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 65; i++ {
		source := fmt.Sprintf("<p>%d</p>", i)
		if i%2 == 0 {
			source += contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1})
		}
		if _, err = r.Create(ctx, content.PostInput{Title: fmt.Sprint("post ", i), ContentSource: source}); err != nil {
			t.Fatal(err)
		}
	}
	canonical := func() string {
		t.Helper()
		var value string
		err := pool.QueryRow(ctx, `SELECT jsonb_build_object('posts',(SELECT jsonb_agg(to_jsonb(p)-'snippet_refs_ready' ORDER BY id) FROM np_posts p),'revisions',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM np_post_revisions r))::text`).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := canonical()
	if _, err = pool.Exec(ctx, `DROP TRIGGER np_posts_invalidate_snippet_refs ON np_posts;DROP FUNCTION np_invalidate_snippet_refs();DROP TABLE np_post_snippet_refs;ALTER TABLE np_posts DROP COLUMN snippet_refs_ready;DELETE FROM newpress_schema_migrations WHERE version='0067_snippet_reference_projection.sql'`); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_snippet_second_batch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id>32 AND NEW.snippet_refs_ready THEN RAISE EXCEPTION 'injected projection failure'; END IF; RETURN NEW; END $$;CREATE TRIGGER fail_snippet_second_batch BEFORE UPDATE ON np_posts FOR EACH ROW EXECUTE FUNCTION fail_snippet_second_batch()`); err != nil {
		t.Fatal(err)
	}
	if err = PrepareSnippetReferences(ctx, pool); err == nil {
		t.Fatal("failure ignored")
	}
	var ready int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_posts WHERE snippet_refs_ready`).Scan(&ready); err != nil || ready != 32 {
		t.Fatal(ready, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_snippet_second_batch ON np_posts;DROP FUNCTION fail_snippet_second_batch()`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = PrepareSnippetReferences(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if canonical() != before {
		t.Fatal("rebuild changed editorial facts")
	}
	got, err := r.SnippetImpacts(ctx, s.ID, a, 24, 0)
	if err != nil || got.Total != 33 || len(got.Items) != 24 {
		t.Fatal(got, err)
	}
	// Old-writer source change invalidates projection even without a version bump.
	source := `<p>changed</p><ASIDE DATA-NP-SNIPPET='1' DATA-NP-SNIPPET-VERSION='5'></ASIDE>`
	if _, err = pool.Exec(ctx, `UPDATE np_posts SET content_source=$2 WHERE id=$1`, 1, source); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() { _, e := r.SnippetImpacts(ctx, s.ID, a, 24, 0); errs <- e })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	got, err = r.SnippetImpacts(ctx, s.ID, a, 24, 0)
	if err != nil || !reflect.DeepEqual(got.Items[0].Versions, []int64{5}) {
		t.Fatal(got, err)
	}
	// Even a tampered ready projection cannot authorize or change a batch.
	if _, err = pool.Exec(ctx, `UPDATE np_post_snippet_refs SET body_version=7 WHERE post_id=1`); err != nil {
		t.Fatal(err)
	}
	u := content.SnippetUpdate{SnippetID: s.ID, SnippetVersion: s.Version, Items: []content.SnippetSelection{{ID: 1, Version: 1}}}
	plan, err := r.PreviewSnippetUpdate(ctx, u, a)
	if err != nil || !reflect.DeepEqual(plan.Items[0].Versions, []int64{5}) {
		t.Fatal(plan, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_posts SET snippet_refs_ready=false WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = PrepareSnippetReferences(ctx, pool); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = r.SnippetImpacts(cancelled, s.ID, a, 24, 0); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestPostgreSQLSnippetPageDoesNotReadOldBodies(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	a := content.SnippetAccess{Identity: "legacy:test", Actor: "test", Read: true, Manage: true}
	s, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "private", ContentSource: "<p>secret</p>", Visibility: content.SnippetPrivate}, a)
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 53; i++ {
		s, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: fmt.Sprint("v", i), ContentSource: "<p>" + strings.Repeat("body ", 1024) + "</p>", Visibility: content.SnippetShared}, a)
		if err != nil {
			t.Fatal(err)
		}
	}
	reader := content.SnippetAccess{UserID: 7, Identity: "user:7", Actor: "reader", Read: true}
	page, err := r.GetSnippetPage(ctx, s.ID, reader, 0, 24)
	if err != nil || len(page.History) != 24 || page.NextBefore != 30 || page.Current.Version != 53 {
		t.Fatal(page, err)
	}
	page, err = r.GetSnippetPage(ctx, s.ID, reader, 30, 100)
	if err != nil || len(page.History) != 28 || page.History[27].Version != 2 || page.NextBefore != 0 {
		t.Fatal(page, err)
	}
	// Historical body damage is not read by summary pagination, but the legacy
	// full-history read must still detect it. This defines the limited check.
	if _, err = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=convert_to((convert_from(data,'UTF8')::jsonb || '{"content_html":"wrong"}'::jsonb)::text,'UTF8') WHERE snippet_id=$1 AND version=2`, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetSnippetPage(ctx, s.ID, reader, 0, 24); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetSnippet(ctx, s.ID, a); err == nil {
		t.Fatal("old corruption ignored by full read")
	}
	if _, err = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=convert_to((convert_from(data,'UTF8')::jsonb || '{"content_html":"wrong"}'::jsonb)::text,'UTF8') WHERE snippet_id=$1 AND version=$2`, s.ID, s.CurrentVersion); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetSnippetPage(ctx, s.ID, a, 0, 24); err == nil {
		t.Fatal("current corruption accepted")
	}
}
