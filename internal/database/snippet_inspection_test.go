package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

type snippetReadTraceKey struct{}
type snippetReadTrace struct {
	prefix string
	after  func()
	once   sync.Once
}

func (t *snippetReadTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, snippetReadTraceKey{}, strings.HasPrefix(data.SQL, t.prefix))
}
func (t *snippetReadTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if matched, _ := ctx.Value(snippetReadTraceKey{}).(bool); matched {
		t.once.Do(t.after)
	}
}

func TestPostgreSQLSnippetInspectionAndPinnedReadUseConsistentSnapshot(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := seedSnippetSaveHistory(t, ctx, pool, 3, 20)
	q := content.SnippetInspectionQuery{Limit: 100}
	before, err := InspectSnippetRecords(ctx, pool, q)
	if err != nil || !before.Healthy {
		t.Fatal(before, err)
	}
	var injected error
	config := pool.Config()
	config.ConnConfig.Tracer = &snippetReadTrace{prefix: "SELECT\n (SELECT count(*) FROM np_snippets)", after: func() {
		_, injected = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=2`, s.ID, []byte(`{`))
	}}
	other, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	during, err := InspectSnippetRecords(ctx, other, q)
	if err != nil || injected != nil || !during.Healthy || during.SnapshotSHA256 != before.SnapshotSHA256 {
		t.Fatal("inspection mixed snapshots", during, err, injected)
	}
	after, err := InspectSnippetRecords(ctx, pool, q)
	if err != nil || after.Healthy || after.SnapshotSHA256 == before.SnapshotSHA256 {
		t.Fatal("later snapshot did not detect concurrent damage", after, err)
	}
	config = pool.Config()
	config.ConnConfig.Tracer = &snippetReadTrace{prefix: "SELECT s.id,s.version,s.visibility,s.data,r.version,r.data", after: func() {
		_, injected = pool.Exec(ctx, `DELETE FROM np_snippet_revisions WHERE snippet_id=$1 AND version=1`, s.ID)
	}}
	reader, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	keys := []contenthtml.SnippetKey{{ID: s.ID, Version: 3}}
	resolved, err := NewPostRepository(reader).ResolveSnippets(ctx, keys)
	if err != nil || injected != nil || resolved[keys[0]] == "" {
		t.Fatal("pinned read mixed sharing boundary snapshots", err, injected)
	}
	if _, err = NewPostRepository(pool).ResolveSnippets(ctx, keys); !errors.Is(err, content.ErrSnippetCorrupt) {
		t.Fatal("next pinned read ignored missing sharing boundary", err)
	}
}

func TestPostgreSQLSnippetInspectionRecordsAndNoMutation(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	a := content.SnippetAccess{Identity: "legacy:private-audit-operator", Actor: "private-actor", Read: true, Manage: true}
	s, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "private-audit-title", ContentSource: "<p>private-audit-body</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(ctx, content.PostInput{Title: "source", ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	u := content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}
	draft, err := r.PreviewSnippetDraft(ctx, u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest, u.RequestKey = draft.Digest, strings.Repeat("b", 64)
	submission, err := content.NewSnippetDraftSubmission(u, a)
	if err != nil {
		t.Fatal(err)
	}
	created, err := r.Submit(ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	s, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: "shared", ContentSource: "<p>two</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	update := content.SnippetUpdate{SnippetID: s.ID, SnippetVersion: s.Version, Items: []content.SnippetSelection{{ID: p.ID, Version: p.Version}}, RequestKey: strings.Repeat("a", 64)}
	plan, err := r.PreviewSnippetUpdate(ctx, update, a)
	if err != nil {
		t.Fatal(err)
	}
	update.Digest = plan.Digest
	if _, err = r.ApplySnippetUpdate(ctx, update, a); err != nil {
		t.Fatal(err)
	}
	if _, err = r.WithdrawSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, a); err != nil {
		t.Fatal(err)
	}
	second := seedSnippetSaveHistory(t, ctx, pool, 3, 20)
	q := content.SnippetInspectionQuery{Limit: 100}
	before := snippetMaintenanceFacts(t, ctx, pool)
	clean, err := InspectSnippetRecords(ctx, pool, q)
	if err != nil || !clean.Healthy || !clean.Complete || clean.Totals.Snippets != 2 || clean.Totals.Revisions != 5 || clean.Totals.UpdateReceipts != 1 || clean.Totals.EditorialReceipts != 1 || clean.Checked != clean.Totals {
		t.Fatal(clean, err)
	}
	if !reflect.DeepEqual(before, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal("healthy audit changed records")
	}
	// A permanently deleted target is legitimate historical evidence.
	if _, err = pool.Exec(ctx, `DELETE FROM np_posts WHERE id=$1`, created.Receipt.PostID); err != nil {
		t.Fatal(err)
	}
	deleted, err := InspectSnippetRecords(ctx, pool, q)
	if err != nil || !deleted.Healthy || deleted.SnapshotSHA256 == clean.SnapshotSHA256 {
		t.Fatal(deleted, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_snippets SET version=version+1 WHERE id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=1`, s.ID, []byte(`{`)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_snippet_receipts SET data=convert_to((convert_from(data,'UTF8')::jsonb||'{"target_version":99,"snippet_version":99}'::jsonb)::text,'UTF8')`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_content_submissions SET created_at='0001-01-01 00:00:00+00'`); err != nil {
		t.Fatal(err)
	}
	before = snippetMaintenanceFacts(t, ctx, pool)
	broken, err := InspectSnippetRecords(ctx, pool, q)
	if err != nil || !broken.Complete || broken.Healthy || broken.IssueCount < 5 {
		t.Fatal(broken, err)
	}
	want := map[string]bool{"index_mismatch": false, "invalid_json": false, "parent_unverifiable": false, "receipt_relationship": false, "receipt_target_missing": false, "invalid_editorial_receipt": false, "incomplete_or_invalid_history": false}
	for _, issue := range broken.Items {
		want[issue.Fault] = true
	}
	for fault, found := range want {
		if !found {
			t.Error("missing diagnostic", fault)
		}
	}
	var all []content.SnippetInspectionIssue
	for offset := 0; ; {
		page, err := InspectSnippetRecords(ctx, pool, content.SnippetInspectionQuery{Limit: 2, Offset: offset})
		if err != nil || page.SnapshotSHA256 != broken.SnapshotSHA256 || page.Checked != broken.Checked {
			t.Fatal(page, err)
		}
		all = append(all, page.Items...)
		if page.NextOffset < 0 {
			break
		}
		offset = page.NextOffset
	}
	if !reflect.DeepEqual(all, broken.Items) || !reflect.DeepEqual(before, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal("pagination changed records or diagnostics")
	}
	encoded, _ := json.Marshal(broken)
	for _, private := range []string{"private-audit-title", "private-audit-body", "private-actor", a.Identity, update.RequestKey, submission.Key} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatal("report leaked original data")
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=1`, s.ID, []byte(`[`)); err != nil {
		t.Fatal(err)
	}
	changed, err := InspectSnippetRecords(ctx, pool, q)
	if err != nil || changed.SnapshotSHA256 == broken.SnapshotSHA256 {
		t.Fatal("original-byte change did not change snapshot digest", err)
	}
}

func TestPostgreSQLSnippetInspectionOrphansLimitsAndCancellation(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectSnippetRecords(ctx, nil, content.SnippetInspectionQuery{Limit: 0}); err == nil {
		t.Fatal("invalid query reached storage")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := InspectSnippetRecords(cancelled, pool, content.SnippetInspectionQuery{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s := seedSnippetSaveHistory(t, ctx, pool, 1, 20)
	if _, err := pool.Exec(ctx, `ALTER TABLE np_snippet_revisions DROP CONSTRAINT np_snippet_revisions_snippet_id_fkey; ALTER TABLE np_snippet_receipts DROP CONSTRAINT np_snippet_receipts_snippet_id_fkey`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO np_snippet_revisions(snippet_id,version,data) SELECT 999,version,data FROM np_snippet_revisions WHERE snippet_id=$1`, s.ID); err != nil {
		t.Fatal(err)
	}
	receipt := content.SnippetReceipt{Identity: "legacy:orphan", RequestKey: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), SnippetID: 999, SnippetVersion: 1, TargetVersion: 1, CreatedAt: time.Now().UTC(), Items: []content.SnippetSelection{{ID: 5, Version: 2}}}
	raw, _ := json.Marshal(receipt)
	if _, err := pool.Exec(ctx, `INSERT INTO np_snippet_receipts VALUES($1,$2,$3,$4)`, receipt.Identity, receipt.RequestKey, receipt.SnippetID, raw); err != nil {
		t.Fatal(err)
	}
	report, err := InspectSnippetRecords(ctx, pool, content.SnippetInspectionQuery{Limit: 100})
	if err != nil || report.Healthy || !report.Complete {
		t.Fatal(report, err)
	}
	faults := map[string]bool{}
	for _, issue := range report.Items {
		faults[issue.Fault] = true
	}
	if !faults["orphan_revision"] || !faults["orphan_receipt"] {
		t.Fatal(report)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO np_snippets(id,version,visibility,data) OVERRIDING SYSTEM VALUE SELECT i,1,'private','{}'::bytea FROM generate_series(1001,2001) i`); err != nil {
		t.Fatal(err)
	}
	report, err = InspectSnippetRecords(ctx, pool, content.SnippetInspectionQuery{Limit: 100})
	if err != nil || report.Complete || report.Healthy || report.Fatal != "capacity_exceeded" || report.Checked.Snippets != 0 || report.SnapshotSHA256 != "" {
		t.Fatal("partial capacity scan reported clean", report, err)
	}
}

func TestPostgreSQLSnippetInspectionStrictJSONAndRecordPreflight(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := seedSnippetSaveHistory(t, ctx, pool, 1, 20)
	r := content.SnippetReceipt{Identity: "legacy:diagnostic", RequestKey: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), SnippetID: s.ID, SnippetVersion: 1, TargetVersion: 1, CreatedAt: time.Now().UTC(), Items: []content.SnippetSelection{{ID: 5, Version: 2}}}
	raw, _ := json.Marshal(r)
	if _, err := pool.Exec(ctx, `INSERT INTO np_snippet_receipts VALUES($1,$2,$3,$4)`, r.Identity, r.RequestKey, r.SnippetID, raw); err != nil {
		t.Fatal(err)
	}
	q := content.SnippetInspectionQuery{Limit: 100}
	for _, table := range []string{"np_snippets", "np_snippet_revisions", "np_snippet_receipts"} {
		t.Run(table, func(t *testing.T) {
			var original []byte
			if err := pool.QueryRow(ctx, "SELECT data FROM "+table).Scan(&original); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := pool.Exec(ctx, "UPDATE "+table+" SET data=$1", original); err != nil {
					t.Error(err)
				}
			}()
			if _, err := pool.Exec(ctx, "UPDATE "+table+" SET data=$1", []byte(`{"version":1,"version":1}`)); err != nil {
				t.Fatal(err)
			}
			before := snippetMaintenanceFacts(t, ctx, pool)
			report, err := InspectSnippetRecords(ctx, pool, q)
			if err != nil || report.Healthy || !report.Complete {
				t.Fatal(report, err)
			}
			found := false
			for _, issue := range report.Items {
				found = found || issue.Fault == "invalid_json"
			}
			if !found || !reflect.DeepEqual(before, snippetMaintenanceFacts(t, ctx, pool)) {
				t.Fatal("strict JSON inspection missed damage or changed records")
			}
		})
	}
	// Simulate an imported database without its original size constraint.
	// The preflight must reject the collection before transferring record bodies.
	if _, err := pool.Exec(ctx, `ALTER TABLE np_snippets DROP CONSTRAINT np_snippets_data_check; UPDATE np_snippets SET data=repeat('x',4097)::bytea`); err != nil {
		t.Fatal(err)
	}
	before := snippetMaintenanceFacts(t, ctx, pool)
	report, err := InspectSnippetRecords(ctx, pool, q)
	if err != nil || report.Complete || report.Healthy || report.Fatal != "record_limit_exceeded" || report.Checked != (content.SnippetInspectionTotals{}) || report.SnapshotSHA256 != "" {
		t.Fatal("oversize original transferred or reported complete", report, err)
	}
	if !reflect.DeepEqual(before, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal("record preflight changed original data")
	}
}
