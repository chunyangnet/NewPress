package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func snippetRepairRequest(q SnippetQuarantineSummary, raw []byte) SnippetRepairRequest {
	return SnippetRepairRequest{ID: q.ID, Operator: "operator", Reason: "restore independently verified backup", Replacement: raw, ReplacementSHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}
}

func captureSnippetTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, target SnippetMaintenanceTarget, key int) SnippetQuarantineSummary {
	t.Helper()
	review, err := ReviewSnippetMaintenance(ctx, pool, target)
	if err != nil || review.Healthy {
		t.Fatal(review, err)
	}
	q, err := QuarantineSnippetRecords(ctx, pool, SnippetCaptureRequest{Target: target, Reference: review.Reference, RequestKey: fmt.Sprintf("%064x", key), Operator: "operator", Reason: "preserve damaged evidence"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func snippetCanonicalFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	facts := snippetMaintenanceFacts(t, ctx, pool)
	delete(facts, "np_snippet_quarantines")
	return facts
}

func TestPostgreSQLSnippetMaintenanceGateRepairAndPermanentFacts(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	a := content.SnippetAccess{Identity: "legacy:private-maintenance-identity", Actor: "operator", Read: true, Manage: true}
	s := seedSnippetSaveHistory(t, ctx, pool, 3, 20)
	p, err := r.Create(ctx, content.PostInput{Title: "source", ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	u := content.SnippetUpdate{SnippetID: s.ID, SnippetVersion: s.Version, Items: []content.SnippetSelection{{ID: p.ID, Version: p.Version}}, RequestKey: strings.Repeat("a", 64)}
	plan, err := r.PreviewSnippetUpdate(ctx, u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest = plan.Digest
	updated, err := r.ApplySnippetUpdate(ctx, u, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err = r.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	draftRequest := content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}
	draft, err := r.PreviewSnippetDraft(ctx, draftRequest, a)
	if err != nil {
		t.Fatal(err)
	}
	draftRequest.Digest, draftRequest.RequestKey = draft.Digest, strings.Repeat("b", 64)
	submission, err := content.NewSnippetDraftSubmission(draftRequest, a)
	if err != nil {
		t.Fatal(err)
	}
	created, err := r.Submit(ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	target := SnippetMaintenanceTarget{SnippetID: s.ID}
	replacement, err := ExportSnippetRepair(ctx, pool, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=1`, s.ID, []byte(`{`)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO np_content_submissions(key_hash,fingerprint,post_id,version,action,created_at) VALUES($1,$2,$3,$4,'save',$5)`, strings.Repeat("e", 64), strings.Repeat("f", 64), p.ID, p.Version, time.Now().UTC().Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	canonical := snippetCanonicalFacts(t, ctx, pool)
	q := captureSnippetTest(t, ctx, pool, target, 1)
	if !reflect.DeepEqual(canonical, snippetCanonicalFacts(t, ctx, pool)) {
		t.Fatal("quarantine changed canonical records")
	}
	checks := []func() error{
		func() error { _, e := r.ListSnippets(ctx, a, 24, 0); return e },
		func() error { _, e := r.GetSnippet(ctx, s.ID, a); return e },
		func() error { _, e := r.GetSnippetPage(ctx, s.ID, a, 0, 24); return e },
		func() error {
			_, e := r.ResolveSnippets(ctx, []contenthtml.SnippetKey{{ID: s.ID, Version: 3}})
			return e
		},
		func() error {
			_, e := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "new", ContentSource: "<p>new</p>", Visibility: content.SnippetShared}, a)
			return e
		},
		func() error {
			_, e := r.WithdrawSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, a)
			return e
		},
		func() error { _, e := r.SnippetImpacts(ctx, s.ID, a, 24, 0); return e },
		func() error { _, e := r.ApplySnippetUpdate(ctx, u, a); return e },
		func() error {
			_, e := r.PreviewSnippetDraft(ctx, content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}, a)
			return e
		},
		func() error { _, e := r.Submit(ctx, submission); return e },
		func() error {
			_, e := r.Submit(ctx, content.Submission{Key: strings.Repeat("9", 64), Fingerprint: strings.Repeat("8", 64), Action: "create", Input: content.PostInput{Title: "ordinary blocked draft"}})
			return e
		},
		func() error { _, e := r.FindSubmission(ctx, submission.Key, submission.Fingerprint); return e },
		func() error {
			return r.WalkMediaSnapshots(ctx, func(content.MediaSnapshot) error { t.Error("media visitor ran while blocked"); return nil })
		},
	}
	for i, check := range checks {
		if err := check(); !errors.Is(err, content.ErrSnippetMaintenance) {
			t.Error("gate", i, err)
		}
	}
	if !reflect.DeepEqual(canonical, snippetCanonicalFacts(t, ctx, pool)) {
		t.Fatal("blocked operation changed canonical records")
	}
	for _, sql := range []string{`DELETE FROM np_snippet_quarantines`, `TRUNCATE np_snippet_quarantines`, `UPDATE np_snippet_quarantines SET data=convert_to((convert_from(data,'UTF8')::jsonb||'{"extra":true}'::jsonb)::text,'UTF8')`} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Fatal("evidence mutation accepted", sql)
		}
	}
	page, err := ListSnippetQuarantines(ctx, pool, content.SnippetInspectionQuery{Limit: 100})
	if err != nil || page.Total != 1 || page.Unresolved != 1 {
		t.Fatal(page, err)
	}
	repaired, err := RepairSnippetRecords(ctx, pool, snippetRepairRequest(q, replacement), time.Now().UTC())
	if err != nil || repaired.Repair == nil || repaired.Repair.SnippetVersion != s.Version+1 {
		t.Fatal(repaired, err)
	}
	after := snippetCanonicalFacts(t, ctx, pool)
	for table, value := range canonical {
		if table != "np_snippets" && table != "np_snippet_revisions" && after[table] != value {
			t.Error("repair changed unrelated facts", table)
		}
	}
	record, err := r.GetSnippet(ctx, s.ID, a)
	if err != nil || record.Visibility != content.SnippetWithdrawn || record.SharedSince != 0 || len(record.Revisions) != 3 {
		t.Fatal(record.Snippet, err)
	}
	resolved, err := r.ResolveSnippets(ctx, []contenthtml.SnippetKey{{ID: s.ID, Version: 1}, {ID: s.ID, Version: 3}})
	if err != nil || len(resolved) != 0 {
		t.Fatal("repair republished old history", err)
	}
	replayed, err := r.ApplySnippetUpdate(ctx, u, a)
	if err != nil || !replayed.Replayed || !reflect.DeepEqual(replayed.Receipt, updated.Receipt) {
		t.Fatal("old update identity lost", err)
	}
	draftReplay, err := r.Submit(ctx, submission)
	if err != nil || !draftReplay.Replayed || draftReplay.Receipt.PostID != created.Receipt.PostID {
		t.Fatal("old draft identity lost", err)
	}
	reshared, err := r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: record.Version}, content.SnippetInput{Title: "new sharing period", ContentSource: "<p>new sharing body</p>", Visibility: content.SnippetShared}, a)
	if err != nil || reshared.SharedSince != 5 {
		t.Fatal(reshared, err)
	}
	keys := []contenthtml.SnippetKey{{ID: s.ID, Version: 3}, {ID: s.ID, Version: 5}}
	resolved, err = r.ResolveSnippets(ctx, keys)
	if err != nil || resolved[keys[0]] != "" || resolved[keys[1]] == "" {
		t.Fatal("re-share revived pre-repair period", err)
	}
	again, err := RepairSnippetRecords(ctx, pool, snippetRepairRequest(q, replacement), time.Now().UTC())
	if err != nil || !reflect.DeepEqual(again, repaired) {
		t.Fatal("repair replay changed historical result", err)
	}
	page, err = ListSnippetQuarantines(ctx, pool, content.SnippetInspectionQuery{Limit: 100})
	if err != nil || page.Unresolved != 0 || page.Total != 1 {
		t.Fatal(page, err)
	}
	var evidence []byte
	if err = pool.QueryRow(ctx, `SELECT data FROM np_snippet_quarantines WHERE id=$1`, q.ID).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	var retained snippetQuarantine
	if err = json.Unmarshal(evidence, &retained); err != nil || retained.validate() != nil || !bytes.Equal(retained.Original.Revisions[0].Data, []byte(`{`)) {
		t.Fatal("original raw evidence was lost", err)
	}
}

func TestPostgreSQLSnippetMaintenanceEditorialAndLastRelease(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	sub := content.Submission{Key: strings.Repeat("c", 64), Fingerprint: strings.Repeat("d", 64), Action: "create", Input: content.PostInput{Title: "ordinary draft"}}
	created, err := r.Submit(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	editorial := SnippetMaintenanceTarget{EditorialKey: sub.Key}
	editorialRepair, err := ExportSnippetRepair(ctx, pool, editorial)
	if err != nil {
		t.Fatal(err)
	}
	s := seedSnippetSaveHistory(t, ctx, pool, 3, 20)
	snippet := SnippetMaintenanceTarget{SnippetID: s.ID}
	snippetRepair, err := ExportSnippetRepair(ctx, pool, snippet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_content_submissions SET created_at='0001-01-01 00:00:00+00'; DELETE FROM np_snippet_revisions WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	q1 := captureSnippetTest(t, ctx, pool, editorial, 2)
	before := snippetMaintenanceFacts(t, ctx, pool)
	var altered SnippetRepairDocument
	if err := json.Unmarshal(editorialRepair, &altered); err != nil {
		t.Fatal(err)
	}
	altered.Editorial.Fingerprint = strings.Repeat("e", 64)
	changed, _ := json.Marshal(altered)
	if _, err = RepairSnippetRecords(ctx, pool, snippetRepairRequest(q1, changed), time.Now().UTC()); !errors.Is(err, ErrSnippetMaintenanceConflict) {
		t.Fatal("repair rewrote an individually valid permanent fingerprint", err)
	}
	if _, err = RepairSnippetRecords(ctx, pool, snippetRepairRequest(q1, editorialRepair), time.Now().UTC()); !errors.Is(err, ErrSnippetMaintenanceIncomplete) {
		t.Fatal("last gate released with unhandled historical damage", err)
	}
	if !reflect.DeepEqual(before, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal("rejected final repair changed rows")
	}
	q2 := captureSnippetTest(t, ctx, pool, snippet, 3)
	if _, err = RepairSnippetRecords(ctx, pool, snippetRepairRequest(q1, editorialRepair), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err = r.FindSubmission(ctx, sub.Key, sub.Fingerprint); !errors.Is(err, content.ErrSnippetMaintenance) {
		t.Fatal("other unresolved evidence did not block lookup", err)
	}
	if _, err = RepairSnippetRecords(ctx, pool, snippetRepairRequest(q2, snippetRepair), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	replay, err := r.Submit(ctx, sub)
	if err != nil || !replay.Replayed || replay.Receipt.PostID != created.Receipt.PostID {
		t.Fatal("common creation receipt changed", err)
	}
	report, err := InspectSnippetRecords(ctx, pool, content.SnippetInspectionQuery{Limit: 100})
	if err != nil || !report.Healthy || !report.Complete {
		t.Fatal(report, err)
	}
}

func TestPostgreSQLSnippetMaintenanceConflictCancellationAndRollback(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := seedSnippetSaveHistory(t, ctx, pool, 3, 20)
	target := SnippetMaintenanceTarget{SnippetID: s.ID}
	raw, err := ExportSnippetRepair(ctx, pool, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=1`, s.ID, []byte(`{`)); err != nil {
		t.Fatal(err)
	}
	review, err := ReviewSnippetMaintenance(ctx, pool, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=1`, s.ID, []byte(`[`)); err != nil {
		t.Fatal(err)
	}
	stale := SnippetCaptureRequest{Target: target, Reference: review.Reference, RequestKey: strings.Repeat("1", 64), Operator: "operator", Reason: "stale evidence must fail"}
	before := snippetMaintenanceFacts(t, ctx, pool)
	if _, err = QuarantineSnippetRecords(ctx, pool, stale, time.Now().UTC()); !errors.Is(err, ErrSnippetMaintenanceConflict) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal("stale capture wrote evidence")
	}
	q := captureSnippetTest(t, ctx, pool, target, 5)
	request := snippetRepairRequest(q, raw)
	before = snippetMaintenanceFacts(t, ctx, pool)
	changedCapture := SnippetCaptureRequest{Target: target, Reference: q.Reference, RequestKey: fmt.Sprintf("%064x", 5), Operator: "other operator", Reason: "preserve damaged evidence"}
	if _, err = QuarantineSnippetRecords(ctx, pool, changedCapture, time.Now().UTC()); !errors.Is(err, ErrSnippetMaintenanceConflict) {
		t.Fatal("capture request identity was reused with different declarations", err)
	}
	invalid := request
	invalid.ReplacementSHA256 = strings.Repeat("0", 64)
	if _, err = RepairSnippetRecords(ctx, pool, invalid, time.Now().UTC()); !errors.Is(err, ErrSnippetMaintenanceInput) {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, snippetWriteLock); err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	_, err = RepairSnippetRecords(waiting, pool, request, time.Now().UTC())
	cancel()
	if e := blocker.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("maintenance ignored canceled lock wait", err)
	}
	if !reflect.DeepEqual(before, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal("failed or canceled repair changed records")
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION np_fail_snippet_repair() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected repair failure'; END $$;
 CREATE TRIGGER np_fail_snippet_repair BEFORE UPDATE ON np_snippet_revisions FOR EACH ROW EXECUTE FUNCTION np_fail_snippet_repair()`); err != nil {
		t.Fatal(err)
	}
	if _, err = RepairSnippetRecords(ctx, pool, request, time.Now().UTC()); err == nil {
		t.Fatal("injected failure accepted")
	}
	if !reflect.DeepEqual(before, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal("partial repair survived rollback")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER np_fail_snippet_repair ON np_snippet_revisions; DROP FUNCTION np_fail_snippet_repair()`); err != nil {
		t.Fatal(err)
	}
	if _, err = RepairSnippetRecords(ctx, pool, request, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	before = snippetMaintenanceFacts(t, ctx, pool)
	request.Reason = "different replay reason"
	if _, err = RepairSnippetRecords(ctx, pool, request, time.Now().UTC()); !errors.Is(err, ErrSnippetMaintenanceConflict) {
		t.Fatal("repair replay rewrote audited declarations", err)
	}
	if !reflect.DeepEqual(before, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal("rejected repair replay changed records")
	}
}

func TestPostgreSQLSnippetMaintenanceOrphanRestorePreservesSequence(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := seedSnippetSaveHistory(t, ctx, pool, 3, 20)
	target := SnippetMaintenanceTarget{SnippetID: s.ID}
	raw, err := ExportSnippetRepair(ctx, pool, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE np_snippet_revisions DROP CONSTRAINT np_snippet_revisions_snippet_id_fkey; DELETE FROM np_snippets`); err != nil {
		t.Fatal(err)
	}
	q := captureSnippetTest(t, ctx, pool, target, 6)
	if _, err = pool.Exec(ctx, `SELECT setval(pg_get_serial_sequence('np_snippets','id'),$1,false)`, s.ID); err != nil {
		t.Fatal(err)
	}
	before := snippetMaintenanceFacts(t, ctx, pool)
	if _, err = RepairSnippetRecords(ctx, pool, snippetRepairRequest(q, raw), time.Now().UTC()); !errors.Is(err, ErrSnippetMaintenanceConflict) {
		t.Fatal("unsafe identity sequence was accepted", err)
	}
	if !reflect.DeepEqual(before, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal("unsafe sequence repair mutated records")
	}
	var called bool
	if err = pool.QueryRow(ctx, `SELECT is_called FROM np_snippets_id_seq`).Scan(&called); err != nil || called {
		t.Fatal("repair advanced nontransactional sequence", err)
	}
	if _, err = pool.Exec(ctx, `SELECT setval(pg_get_serial_sequence('np_snippets','id'),$1,true)`, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = RepairSnippetRecords(ctx, pool, snippetRepairRequest(q, raw), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	record, err := NewPostRepository(pool).GetSnippet(ctx, s.ID, snippetSaveAccess)
	if err != nil || len(record.Revisions) != 3 || record.Visibility != content.SnippetWithdrawn {
		t.Fatal(record.Snippet, err)
	}
	created, err := NewPostRepository(pool).SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "next object", ContentSource: "<p>next</p>", Visibility: content.SnippetPrivate}, snippetSaveAccess)
	if err != nil || created.ID != s.ID+1 {
		t.Fatal("identity collision after repair", created, err)
	}
}

func TestPostgreSQLSnippetMaintenanceEvidenceCapacityRollsBack(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	first := seedSnippetSaveHistory(t, ctx, pool, 1, 8)
	second := seedSnippetSaveHistory(t, ctx, pool, 1, 8)
	if _, err := pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$1`, []byte(`{`)); err != nil {
		t.Fatal(err)
	}
	q := captureSnippetTest(t, ctx, pool, SnippetMaintenanceTarget{SnippetID: first.ID}, 8)
	var encoded []byte
	if err := pool.QueryRow(ctx, `SELECT data FROM np_snippet_quarantines WHERE id=$1`, q.ID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var original snippetQuarantine
	if err := json.Unmarshal(encoded, &original); err != nil {
		t.Fatal(err)
	}
	rows := make([][]any, 0, MaxSnippetQuarantines-1)
	for i := 1; i < MaxSnippetQuarantines; i++ {
		clone := original
		clone.Capture.RequestKey = fmt.Sprintf("%064x", i+1000)
		clone.ID = clone.Capture.id()
		raw, err := json.Marshal(clone)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, []any{clone.ID, clone.Capture.Target.scope(), clone.Capture.Reference, clone.CapturedAt, false, raw})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"np_snippet_quarantines"}, []string{"id", "scope", "reference", "captured_at", "repaired", "data"}, pgx.CopyFromRows(rows)); err != nil {
		t.Fatal(err)
	}
	target := SnippetMaintenanceTarget{SnippetID: second.ID}
	review, err := ReviewSnippetMaintenance(ctx, pool, target)
	if err != nil {
		t.Fatal(err)
	}
	before := snippetMaintenanceFacts(t, ctx, pool)
	_, err = QuarantineSnippetRecords(ctx, pool, SnippetCaptureRequest{Target: target, Reference: review.Reference, RequestKey: strings.Repeat("f", 64), Operator: "operator", Reason: "count budget rollback"}, time.Now().UTC())
	if !errors.Is(err, content.ErrSnippetCapacity) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snippetMaintenanceFacts(t, ctx, pool)) {
		t.Fatal("full evidence ledger partially captured a new target")
	}
}
