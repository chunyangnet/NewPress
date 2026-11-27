package database

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestSnippetReferenceQueryValidation(t *testing.T) {
	for _, q := range []SnippetReferenceQuery{{Limit: 0}, {Limit: -1}, {Limit: 101}, {AfterID: -1, Limit: 1}, {ThroughID: -1, Limit: 1}, {AfterID: 1, Limit: 1}, {AfterID: 3, ThroughID: 2, Limit: 1}} {
		if _, err := InspectSnippetReferences(t.Context(), nil, q); err == nil {
			t.Fatal("invalid request reached storage", q)
		}
		if _, err := RebuildSnippetReferences(t.Context(), nil, q); err == nil {
			t.Fatal("invalid rebuild reached storage", q)
		}
	}
}

func snippetMaintenanceFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() AND tablename LIKE 'np_%' AND tablename<>'np_post_snippet_refs' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, name := range names {
		expression := "to_jsonb(t)"
		if name == "np_posts" {
			expression += "-'snippet_refs_ready'"
		}
		var value string
		if err = pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT `+expression+` AS v FROM `+pgx.Identifier{name}.Sanitize()+` t) s`).Scan(&value); err != nil {
			t.Fatal(name, err)
		}
		result[name] = value
	}
	return result
}

func snippetMaintenanceIndex(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var value string
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('ready',(SELECT jsonb_agg(jsonb_build_array(id,snippet_refs_ready) ORDER BY id) FROM np_posts),'refs',(SELECT jsonb_agg(to_jsonb(r) ORDER BY post_id,snippet_id,body_version) FROM np_post_snippet_refs r))::text`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPostgreSQLSnippetReferenceMaintenanceInspectRepairAndReceipts(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	a := content.SnippetAccess{Identity: "legacy:maintenance", Actor: "maintenance", Read: true, Manage: true}
	s, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "shared", ContentSource: "<p>one</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	shell := func(id, version int64) string {
		return contenthtml.SnippetShell(contenthtml.SnippetKey{ID: id, Version: version})
	}
	posts := make([]content.Post, 6)
	for i, source := range []string{
		strings.Repeat(shell(s.ID, 1), 2) + shell(999, 3) + shell(999, 4),
		"<p>plain</p>", shell(s.ID, 1), shell(s.ID, 1), "<p>empty index</p>",
		`<aside data-np-snippet="888" data-np-snippet-version="2">` + shell(777, 1) + `</aside>`,
	} {
		posts[i], err = r.Create(ctx, content.PostInput{Title: fmt.Sprint("index fixture ", i), ContentSource: source, Status: content.StatusPrivate})
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: "shared", ContentSource: "<p>two</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	u := content.SnippetUpdate{SnippetID: s.ID, SnippetVersion: s.Version, Items: []content.SnippetSelection{{ID: posts[0].ID, Version: posts[0].Version}}, RequestKey: strings.Repeat("a", 64)}
	plan, err := r.PreviewSnippetUpdate(ctx, u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest = plan.Digest
	if _, err = r.ApplySnippetUpdate(ctx, u, a); err != nil {
		t.Fatal(err)
	}
	draft := content.SnippetDraftRequest{PostID: posts[2].ID, Version: posts[2].Version}
	draftPlan, err := r.PreviewSnippetDraft(ctx, draft, a)
	if err != nil {
		t.Fatal(err)
	}
	draft.Digest, draft.RequestKey = draftPlan.Digest, strings.Repeat("b", 64)
	submission, err := content.NewSnippetDraftSubmission(draft, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Submit(ctx, submission); err != nil {
		t.Fatal(err)
	}
	if _, err = r.WithdrawSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, a); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE np_post_snippet_refs SET occurrences=7 WHERE post_id=$1 AND snippet_id=$2`, []any{posts[0].ID, s.ID}},
		{`DELETE FROM np_post_snippet_refs WHERE post_id=$1 AND snippet_id=999 AND body_version=3`, []any{posts[0].ID}},
		{`INSERT INTO np_post_snippet_refs VALUES($1,50000,8,1),($2,100,1,1)`, []any{posts[0].ID, posts[1].ID}},
		{`UPDATE np_posts SET snippet_refs_ready=false WHERE id=ANY($1::bigint[])`, []any{[]int64{posts[2].ID, posts[4].ID}}},
		{`DELETE FROM np_post_snippet_refs WHERE post_id=$1`, []any{posts[3].ID}},
	} {
		if _, err = pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	facts, index := snippetMaintenanceFacts(t, ctx, pool), snippetMaintenanceIndex(t, ctx, pool)
	q := SnippetReferenceQuery{Limit: 100}
	page, err := InspectSnippetReferences(ctx, pool, q)
	if err != nil || page.Checked != 7 || page.NeedsRebuild != 5 || page.Rebuilt != 0 || page.HasMore {
		t.Fatal(page, err)
	}
	first := page.Items[0]
	if !first.ReadyBefore || first.ExpectedEntries != 3 || first.ActualEntries != 3 || first.MissingEntries != 1 || first.UnexpectedEntries != 1 || first.OccurrenceMismatches != 1 {
		t.Fatal(first)
	}
	if page.Items[1].UnexpectedEntries != 1 || page.Items[2].ReadyBefore || page.Items[2].MissingEntries != 0 || page.Items[3].MissingEntries != 1 || page.Items[4].ReadyBefore || page.Items[5].NeedsRebuild || page.Items[5].ExpectedEntries != 1 {
		t.Fatal(page.Items)
	}
	if index != snippetMaintenanceIndex(t, ctx, pool) || !reflect.DeepEqual(facts, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal("inspection changed data")
	}
	repaired, err := RebuildSnippetReferences(ctx, pool, q)
	if err != nil || repaired.Rebuilt != 5 || repaired.NeedsRebuild != 5 {
		t.Fatal(repaired, err)
	}
	clean, err := InspectSnippetReferences(ctx, pool, q)
	if err != nil || clean.NeedsRebuild != 0 {
		t.Fatal(clean, err)
	}
	if !reflect.DeepEqual(facts, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal("repair changed canonical records or permanent evidence")
	}
	replay, err := r.ApplySnippetUpdate(ctx, u, a)
	if err != nil || !replay.Replayed {
		t.Fatal(replay, err)
	}
	if _, err = r.FindSubmission(ctx, submission.Key, submission.Fingerprint); err != nil {
		t.Fatal("draft receipt lost", err)
	}
	if resolved, err := r.ResolveSnippets(ctx, []contenthtml.SnippetKey{{ID: s.ID, Version: 2}}); err != nil || len(resolved) != 0 {
		t.Fatal("repair revived withdrawn snippet", resolved, err)
	}
	// A clean repeat must execute no projection or readiness writes.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_index_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'unexpected index rewrite'; END $$;CREATE TRIGGER reject_index_rewrite BEFORE UPDATE ON np_posts FOR EACH ROW EXECUTE FUNCTION reject_index_rewrite();CREATE TRIGGER reject_index_rewrite BEFORE INSERT OR DELETE OR UPDATE ON np_post_snippet_refs FOR EACH ROW EXECUTE FUNCTION reject_index_rewrite()`); err != nil {
		t.Fatal(err)
	}
	if again, err := RebuildSnippetReferences(ctx, pool, q); err != nil || again.Rebuilt != 0 {
		t.Fatal(again, err)
	}
}

func TestPostgreSQLSnippetReferenceMaintenancePagination(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if empty, err := InspectSnippetReferences(ctx, pool, SnippetReferenceQuery{Limit: 32}); err != nil || empty.Checked != 0 || empty.HasMore || empty.ThroughID != 0 || empty.Items == nil {
		t.Fatal(empty, err)
	}
	r := NewPostRepository(pool)
	var ids []int64
	for i := range 66 {
		p, err := r.Create(ctx, content.PostInput{Title: fmt.Sprint("paged ", i), ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 999, Version: 1})})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	if _, err := pool.Exec(ctx, `UPDATE np_posts SET snippet_refs_ready=false; DELETE FROM np_post_snippet_refs`); err != nil {
		t.Fatal(err)
	}
	first, err := InspectSnippetReferences(ctx, pool, SnippetReferenceQuery{Limit: 32})
	if err != nil || first.Checked != 32 || !first.HasMore || first.ThroughID != ids[65] || first.NextAfterID != ids[31] {
		t.Fatal(first, err)
	}
	late, err := r.Create(ctx, content.PostInput{Title: "after sweep started", ContentSource: "<p>later</p>"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM np_posts WHERE id=$1`, ids[40]); err != nil {
		t.Fatal(err)
	}
	facts := snippetMaintenanceFacts(t, ctx, pool)
	q := SnippetReferenceQuery{Limit: 32, ThroughID: first.ThroughID}
	checked, rebuilt, pages := 0, 0, 0
	for {
		page, err := RebuildSnippetReferences(ctx, pool, q)
		if err != nil || page.ThroughID != first.ThroughID || page.NextAfterID >= late.ID {
			t.Fatal(page, err)
		}
		pages++
		checked += page.Checked
		rebuilt += page.Rebuilt
		q.AfterID = page.NextAfterID
		if !page.HasMore {
			break
		}
	}
	if checked != 65 || rebuilt != 65 || pages != 3 || !reflect.DeepEqual(facts, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal(checked, rebuilt, pages)
	}
	if empty, err := RebuildSnippetReferences(ctx, pool, q); err != nil || empty.Checked != 0 || empty.HasMore || empty.NextAfterID != first.ThroughID {
		t.Fatal(empty, err)
	}
}

func TestPostgreSQLSnippetReferenceMaintenancePageRollback(t *testing.T) {
	for _, failure := range []string{"projection statement", "oversized canonical body"} {
		t.Run(failure, func(t *testing.T) {
			ctx, pool := openSetupIntegrationPool(t)
			if err := Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			r := NewPostRepository(pool)
			var last content.Post
			for i := range 2 {
				var err error
				last, err = r.Create(ctx, content.PostInput{Title: fmt.Sprint("rollback ", i), ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 999, Version: 1})})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pool.Exec(ctx, `DELETE FROM np_post_snippet_refs`); err != nil {
				t.Fatal(err)
			}
			if failure == "projection statement" {
				if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION fail_reference_page() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.post_id=%d THEN RAISE EXCEPTION 'injected failure'; END IF; RETURN NEW; END $$;CREATE TRIGGER fail_reference_page BEFORE INSERT ON np_post_snippet_refs FOR EACH ROW EXECUTE FUNCTION fail_reference_page()`, last.ID)); err != nil {
					t.Fatal(err)
				}
			} else if _, err := pool.Exec(ctx, `UPDATE np_posts SET content_source=$2 WHERE id=$1`, last.ID, strings.Repeat("x", content.MaxContentBytes+1)); err != nil {
				t.Fatal(err)
			}
			facts, index := snippetMaintenanceFacts(t, ctx, pool), snippetMaintenanceIndex(t, ctx, pool)
			page, err := RebuildSnippetReferences(ctx, pool, SnippetReferenceQuery{Limit: 32})
			if err == nil || page.Checked != 0 || page.Rebuilt != 0 {
				t.Fatal("failure returned a committed result", page, err)
			}
			if index != snippetMaintenanceIndex(t, ctx, pool) || !reflect.DeepEqual(facts, snippetMaintenanceFacts(t, ctx, pool)) {
				t.Fatal("failed page partially changed data")
			}
			if failure == "oversized canonical body" {
				if _, err := InspectSnippetReferences(ctx, pool, SnippetReferenceQuery{Limit: 32}); err == nil || !strings.Contains(err.Error(), "canonical source exceeds") {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPostgreSQLSnippetReferenceMaintenanceConcurrentEditAndCancellation(t *testing.T) {
	for _, cancelInstead := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancelInstead), func(t *testing.T) {
			ctx, pool := openSetupIntegrationPool(t)
			if err := Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			r := NewPostRepository(pool)
			var posts []content.Post
			for i := range 2 {
				p, err := r.Create(ctx, content.PostInput{Title: fmt.Sprint("concurrent ", i), ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 888, Version: 1})})
				if err != nil {
					t.Fatal(err)
				}
				posts = append(posts, p)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM np_post_snippet_refs`); err != nil {
				t.Fatal(err)
			}
			index := snippetMaintenanceIndex(t, ctx, pool)
			writer, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Rollback(context.Background())
			if _, err = writer.Exec(ctx, `SELECT id FROM np_posts WHERE id=$1 FOR UPDATE`, posts[1].ID); err != nil {
				t.Fatal(err)
			}
			config := pool.Config()
			name := fmt.Sprintf("snippet-maintenance-%d", time.Now().UnixNano())
			config.ConnConfig.RuntimeParams["application_name"] = name
			other, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			operation, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				page, err := RebuildSnippetReferences(operation, other, SnippetReferenceQuery{Limit: 32})
				if page.Checked != 0 || page.Rebuilt != 0 {
					done <- errors.New("failed concurrent page returned partial success")
					return
				}
				done <- err
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var waiting bool
				if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, name).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("repair did not wait for canonical row lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if cancelInstead {
				cancel()
				if err = <-done; !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation not propagated", err)
				}
				if err = writer.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
				if snippetMaintenanceIndex(t, ctx, pool) != index {
					t.Fatal("cancelled page committed its first repair")
				}
			} else {
				normalized, fields := content.NormalizePostInput(content.PostInput{Title: posts[1].Title, Slug: posts[1].Slug, ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 888, Version: 2})}, time.Now().UTC())
				if len(fields) != 0 {
					t.Fatal(fields)
				}
				if _, err = updatePostInTransaction(ctx, writer, posts[1].ID, content.Mutation{ExpectedVersion: posts[1].Version}, normalized, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if err = writer.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				err = <-done
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "40001" {
					t.Fatal("stale snapshot did not abort", err)
				}
				var count int
				if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_post_snippet_refs WHERE post_id=$1`, posts[0].ID).Scan(&count); err != nil || count != 0 {
					t.Fatal("failed concurrent page committed first repair", count, err)
				}
				current, err := r.GetByID(ctx, posts[1].ID)
				if err != nil || current.Version != posts[1].Version+1 || current.ContentSource != normalized.ContentSource {
					t.Fatal(current, err)
				}
			}
			facts := snippetMaintenanceFacts(t, ctx, pool)
			if _, err = RebuildSnippetReferences(ctx, pool, SnippetReferenceQuery{Limit: 32}); err != nil {
				t.Fatal("retry failed", err)
			}
			if clean, err := InspectSnippetReferences(ctx, pool, SnippetReferenceQuery{Limit: 32}); err != nil || clean.NeedsRebuild != 0 || !reflect.DeepEqual(facts, snippetMaintenanceFacts(t, ctx, pool)) {
				t.Fatal(clean, err)
			}
		})
	}
}
