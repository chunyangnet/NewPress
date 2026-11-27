package database

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/content"
)

func lockSnippetMaintenance(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, snippetWriteLock); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-editorial-submissions',0))`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE np_snippet_quarantines,np_snippets,np_snippet_revisions,np_snippet_receipts,np_content_submissions IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `LOCK TABLE np_posts IN SHARE MODE`)
	return err
}

func saveSnippetQuarantine(ctx context.Context, tx pgx.Tx, q snippetQuarantine, insert bool) error {
	if err := q.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return ErrSnippetMaintenanceEvidence
	}
	reserve := 0
	if q.Repair == nil {
		reserve = snippetRepairReserve
	}
	if len(raw)+reserve > MaxSnippetQuarantineRecordBytes {
		return content.ErrSnippetCapacity
	}
	if insert {
		_, err = tx.Exec(ctx, `INSERT INTO np_snippet_quarantines(id,scope,reference,captured_at,repaired,data) VALUES($1,$2,$3,$4,$5,$6)`, q.ID, q.Capture.Target.scope(), q.Capture.Reference, q.CapturedAt, q.Repair != nil, raw)
	} else {
		_, err = tx.Exec(ctx, `UPDATE np_snippet_quarantines SET repaired=true,data=$2 WHERE id=$1 AND NOT repaired`, q.ID, raw)
	}
	if err != nil {
		return err
	}
	_, err = loadSnippetQuarantines(ctx, tx)
	return err
}

func QuarantineSnippetRecords(ctx context.Context, pool *pgxpool.Pool, request SnippetCaptureRequest, now time.Time) (SnippetQuarantineSummary, error) {
	if request.Validate() != nil || now.IsZero() {
		return SnippetQuarantineSummary{}, ErrSnippetMaintenanceInput
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return SnippetQuarantineSummary{}, err
	}
	defer tx.Rollback(context.Background())
	if err = lockSnippetMaintenance(ctx, tx); err != nil {
		return SnippetQuarantineSummary{}, err
	}
	all, err := loadSnippetQuarantines(ctx, tx)
	if err != nil {
		return SnippetQuarantineSummary{}, err
	}
	for _, q := range all {
		if q.ID == request.id() {
			if q.Capture != request {
				return SnippetQuarantineSummary{}, ErrSnippetMaintenanceConflict
			}
			return q.summary(), tx.Commit(ctx)
		}
		if q.Repair == nil && q.Capture.Target == request.Target {
			return SnippetQuarantineSummary{}, ErrSnippetMaintenanceConflict
		}
	}
	b, err := readSnippetMaintenanceBundle(ctx, tx, request.Target)
	if err != nil {
		return SnippetQuarantineSummary{}, err
	}
	if b.reference() != request.Reference {
		return SnippetQuarantineSummary{}, ErrSnippetMaintenanceConflict
	}
	version, exists, err := editorialMaintenancePost(ctx, tx, b)
	if err != nil {
		return SnippetQuarantineSummary{}, err
	}
	review, err := reviewSnippetBundle(b, version, exists)
	if err != nil {
		return SnippetQuarantineSummary{}, err
	}
	if review.Healthy {
		return SnippetQuarantineSummary{}, ErrSnippetMaintenanceInput
	}
	q := snippetQuarantine{ID: request.id(), Capture: request, CapturedAt: now.UTC().Truncate(time.Microsecond), Original: b}
	if err = saveSnippetQuarantine(ctx, tx, q, true); err != nil {
		return SnippetQuarantineSummary{}, err
	}
	return q.summary(), tx.Commit(ctx)
}

// Restoring an absent parent must not create a future identity collision.
// We refuse an ambiguous/reconfigured sequence instead of setval, whose effect
// would survive a rollback. Existing parent repairs never change the sequence.
func snippetIdentityAllowsRestore(ctx context.Context, tx pgx.Tx, id int64) error {
	var schema, name string
	var increment, cache, last int64
	var cycle, called bool
	err := tx.QueryRow(ctx, `SELECT n.nspname,c.relname,s.seqincrement,s.seqcache,s.seqcycle
 FROM pg_sequence s JOIN pg_class c ON c.oid=s.seqrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE s.seqrelid=pg_get_serial_sequence('np_snippets','id')::regclass`).Scan(&schema, &name, &increment, &cache, &cycle)
	if err != nil {
		return err
	}
	if increment != 1 || cache != 1 || cycle {
		return ErrSnippetMaintenanceConflict
	}
	if err = tx.QueryRow(ctx, `SELECT last_value,is_called FROM `+pgx.Identifier{schema, name}.Sanitize()).Scan(&last, &called); err != nil {
		return err
	}
	if last < id || last == id && !called {
		return ErrSnippetMaintenanceConflict
	}
	return nil
}

func storeSnippetRepair(ctx context.Context, tx pgx.Tx, before, after snippetRawBundle) error {
	if after.Editorial != nil {
		r := after.Editorial
		_, err := tx.Exec(ctx, `UPDATE np_content_submissions SET fingerprint=$2,post_id=$3,version=$4,action=$5,created_at=$6 WHERE key_hash=$1`,
			r.Key, r.Fingerprint, r.PostID, r.Version, r.Action, r.CreatedAt)
		return err
	}
	if before.Metadata == nil {
		if err := snippetIdentityAllowsRestore(ctx, tx, after.Target.SnippetID); err != nil {
			return err
		}
	}
	m := after.Metadata
	_, err := tx.Exec(ctx, `INSERT INTO np_snippets(id,version,visibility,data) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4)
 ON CONFLICT(id) DO UPDATE SET version=EXCLUDED.version,visibility=EXCLUDED.visibility,data=EXCLUDED.data`,
		m.Index.SnippetID, m.Index.Version, m.Index.Visibility, m.Data)
	if err != nil {
		return err
	}
	for _, row := range after.Revisions {
		if _, err = tx.Exec(ctx, `INSERT INTO np_snippet_revisions(snippet_id,version,data) VALUES($1,$2,$3)
 ON CONFLICT(snippet_id,version) DO UPDATE SET data=EXCLUDED.data WHERE np_snippet_revisions.data IS DISTINCT FROM EXCLUDED.data`, row.Index.SnippetID, row.Index.Version, row.Data); err != nil {
			return err
		}
	}
	for _, row := range after.Receipts {
		if _, err = tx.Exec(ctx, `INSERT INTO np_snippet_receipts(identity,request_key,snippet_id,data) VALUES($1,$2,$3,$4)
 ON CONFLICT(identity,request_key) DO UPDATE SET data=EXCLUDED.data
 WHERE np_snippet_receipts.snippet_id=EXCLUDED.snippet_id AND np_snippet_receipts.data IS DISTINCT FROM EXCLUDED.data`, row.Index.Identity, row.Index.Key, row.Index.SnippetID, row.Data); err != nil {
			return err
		}
	}
	return nil
}

func RepairSnippetRecords(ctx context.Context, pool *pgxpool.Pool, request SnippetRepairRequest, now time.Time) (SnippetQuarantineSummary, error) {
	if request.Validate() != nil || now.IsZero() {
		return SnippetQuarantineSummary{}, ErrSnippetMaintenanceInput
	}
	doc, err := decodeSnippetRepair(request.Replacement)
	if err != nil {
		return SnippetQuarantineSummary{}, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return SnippetQuarantineSummary{}, err
	}
	defer tx.Rollback(context.Background())
	if err = lockSnippetMaintenance(ctx, tx); err != nil {
		return SnippetQuarantineSummary{}, err
	}
	all, err := loadSnippetQuarantines(ctx, tx)
	if err != nil {
		return SnippetQuarantineSummary{}, err
	}
	var selected *snippetQuarantine
	unresolved := 0
	for i := range all {
		if all[i].Repair == nil {
			unresolved++
		}
		if all[i].ID == request.ID {
			selected = &all[i]
		}
	}
	if selected == nil {
		return SnippetQuarantineSummary{}, content.ErrNotFound
	}
	q := *selected
	if old := q.Repair; old != nil {
		if old.ReplacementSHA256 != request.ReplacementSHA256 || old.Operator != request.Operator || old.Reason != request.Reason {
			return SnippetQuarantineSummary{}, ErrSnippetMaintenanceConflict
		}
		return q.summary(), tx.Commit(ctx)
	}
	before, err := readSnippetMaintenanceBundle(ctx, tx, q.Capture.Target)
	if err != nil {
		return SnippetQuarantineSummary{}, err
	}
	if before.reference() != q.Capture.Reference {
		return SnippetQuarantineSummary{}, ErrSnippetMaintenanceConflict
	}
	var after snippetRawBundle
	if before.Editorial != nil {
		if doc.Editorial == nil || doc.Snippet != nil || doc.Editorial.Key != before.Target.EditorialKey || doc.Editorial.Deleted {
			return SnippetQuarantineSummary{}, ErrSnippetMaintenanceInput
		}
		old, replacement := before.Editorial, doc.Editorial
		// A damaged timestamp does not authorize inventing a new fingerprint,
		// destination or committed result. Individually valid facts are preserved.
		if snippetDigest(old.Fingerprint) && old.Fingerprint != replacement.Fingerprint ||
			old.PostID > 0 && old.PostID != replacement.PostID || old.Version > 0 && old.Version != replacement.Version ||
			content.ValidSubmissionAction(old.Action) && old.Action != replacement.Action ||
			!old.CreatedAt.IsZero() && !old.CreatedAt.Equal(replacement.CreatedAt) {
			return SnippetQuarantineSummary{}, ErrSnippetMaintenanceConflict
		}
		after = snippetRawBundle{Target: before.Target, Editorial: doc.Editorial}
		version, exists, e := editorialMaintenancePost(ctx, tx, before)
		if e != nil {
			return SnippetQuarantineSummary{}, e
		}
		review, e := reviewSnippetBundle(before, version, exists)
		if e != nil {
			return SnippetQuarantineSummary{}, e
		}
		if review.Healthy && !reflect.DeepEqual(before.Editorial, after.Editorial) {
			return SnippetQuarantineSummary{}, ErrSnippetMaintenanceConflict
		}
	} else {
		after, err = buildSnippetRepair(before, doc, now.UTC())
		if err != nil {
			return SnippetQuarantineSummary{}, err
		}
	}
	version, exists, err := editorialMaintenancePost(ctx, tx, after)
	if err != nil {
		return SnippetQuarantineSummary{}, err
	}
	review, err := reviewSnippetBundle(after, version, exists)
	if err != nil || !review.Healthy {
		return SnippetQuarantineSummary{}, ErrSnippetMaintenanceInput
	}
	if err = storeSnippetRepair(ctx, tx, before, after); err != nil {
		return SnippetQuarantineSummary{}, err
	}
	stored, err := readSnippetMaintenanceBundle(ctx, tx, after.Target)
	if err != nil {
		return SnippetQuarantineSummary{}, err
	}
	if stored.reference() != after.reference() {
		return SnippetQuarantineSummary{}, ErrSnippetMaintenanceConflict
	}
	// Even while other targets remain quarantined, no repair may exceed the
	// existing canonical budgets. The last repair must leave the whole scan clean.
	canonical, err := inspectSnippetRecordsTx(ctx, tx, content.SnippetInspectionQuery{Limit: 1})
	if err != nil {
		return SnippetQuarantineSummary{}, err
	}
	if !canonical.Complete || unresolved == 1 && !canonical.Healthy {
		return SnippetQuarantineSummary{}, ErrSnippetMaintenanceIncomplete
	}
	at := now.UTC().Truncate(time.Microsecond)
	if at.Before(q.CapturedAt) {
		at = q.CapturedAt
	}
	q.Repair = &snippetRepairAudit{Operator: request.Operator, Reason: request.Reason, At: at, ReplacementSHA256: request.ReplacementSHA256, ResultReference: after.reference()}
	if after.Metadata != nil {
		q.Repair.SnippetVersion = after.Metadata.Index.Version
	}
	if err = saveSnippetQuarantine(ctx, tx, q, false); err != nil {
		return SnippetQuarantineSummary{}, err
	}
	return q.summary(), tx.Commit(ctx)
}
