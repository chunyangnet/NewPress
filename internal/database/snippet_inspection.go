package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/content"
)

// InspectSnippetRecords validates one bounded, read-only snapshot. It scans
// all four record families before returning a page of issues, not source rows.
func InspectSnippetRecords(ctx context.Context, pool *pgxpool.Pool, q content.SnippetInspectionQuery) (content.SnippetInspectionReport, error) {
	if err := q.Validate(); err != nil {
		return content.SnippetInspectionReport{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.SnippetInspectionReport{}, err
	}
	defer tx.Rollback(context.Background())
	report, err := inspectSnippetRecordsTx(ctx, tx, q)
	if err != nil {
		return content.SnippetInspectionReport{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return content.SnippetInspectionReport{}, err
	}
	return report, nil
}

// The caller owns the transaction, so final quarantine release can validate
// the complete canonical collection without opening a second snapshot.
func inspectSnippetRecordsTx(ctx context.Context, tx pgx.Tx, q content.SnippetInspectionQuery) (content.SnippetInspectionReport, error) {
	b, err := content.NewSnippetInspector(q)
	if err != nil {
		return content.SnippetInspectionReport{}, err
	}
	var totals content.SnippetInspectionTotals
	var metadataMax, historyMax, receiptMax, visibilityMax, identityMax, keyMax, editorialMax int64
	err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM np_snippets),
 (SELECT count(*) FROM np_snippet_revisions),
 (SELECT count(*) FROM np_snippet_receipts),
 (SELECT count(*) FROM np_content_submissions),
 (SELECT count(*)*4096 FROM np_snippets)+(SELECT COALESCE(sum(octet_length(data)),0) FROM np_snippet_revisions),
 (SELECT COALESCE(sum(octet_length(data)),0) FROM np_snippet_receipts),
 (SELECT COALESCE(max(octet_length(data)),0) FROM np_snippets),
 (SELECT COALESCE(max(octet_length(data)),0) FROM np_snippet_revisions),
 (SELECT COALESCE(max(octet_length(data)),0) FROM np_snippet_receipts),
 (SELECT COALESCE(max(octet_length(visibility)),0) FROM np_snippets),
 (SELECT COALESCE(max(octet_length(identity)),0) FROM np_snippet_receipts),
 (SELECT COALESCE(max(octet_length(request_key)),0) FROM np_snippet_receipts),
 (SELECT COALESCE(max(octet_length(key_hash)+octet_length(fingerprint)+octet_length(action)),0) FROM np_content_submissions)`).Scan(
		&totals.Snippets, &totals.Revisions, &totals.UpdateReceipts, &totals.EditorialReceipts, &totals.SnippetBytes, &totals.ReceiptBytes,
		&metadataMax, &historyMax, &receiptMax, &visibilityMax, &identityMax, &keyMax, &editorialMax)
	if err != nil {
		return content.SnippetInspectionReport{}, err
	}
	finish := func(fatal string) (content.SnippetInspectionReport, error) {
		if err := ctx.Err(); err != nil {
			return content.SnippetInspectionReport{}, err
		}
		return b.Finish("postgresql", fatal, totals), nil
	}
	if !totals.WithinLimits() {
		return finish("capacity_exceeded")
	}
	if metadataMax > 4096 || historyMax > 2<<20 || receiptMax > 16384 || visibilityMax > 32 || identityMax > 160 || keyMax > 64 || editorialMax > 1024 {
		return finish("record_limit_exceeded")
	}
	scan := func(sql string, accept func(pgx.Rows) error) error {
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = accept(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	steps := []struct {
		sql    string
		accept func(pgx.Rows) error
	}{
		{`SELECT id,version,visibility,data FROM np_snippets ORDER BY id LIMIT 1001`, func(rows pgx.Rows) error {
			var index content.SnippetInspectionIndex
			var raw []byte
			if err := rows.Scan(&index.SnippetID, &index.Version, &index.Visibility, &raw); err != nil {
				return err
			}
			return b.Metadata(index, raw)
		}},
		{`SELECT snippet_id,version,data FROM np_snippet_revisions ORDER BY snippet_id,version LIMIT 10001`, func(rows pgx.Rows) error {
			var index content.SnippetInspectionIndex
			var raw []byte
			if err := rows.Scan(&index.SnippetID, &index.Version, &raw); err != nil {
				return err
			}
			return b.Revision(index, raw)
		}},
		{`SELECT identity,request_key,snippet_id,data FROM np_snippet_receipts ORDER BY identity COLLATE "C",request_key COLLATE "C" LIMIT 10001`, func(rows pgx.Rows) error {
			var index content.SnippetInspectionIndex
			var raw []byte
			if err := rows.Scan(&index.Identity, &index.Key, &index.SnippetID, &raw); err != nil {
				return err
			}
			return b.UpdateReceipt(index, raw)
		}},
		{`SELECT r.key_hash,r.fingerprint,r.post_id,r.version,r.action,r.created_at,COALESCE(p.version,0),p.id IS NOT NULL
 FROM np_content_submissions r LEFT JOIN np_posts p ON p.id=r.post_id ORDER BY r.key_hash COLLATE "C" LIMIT 131073`, func(rows pgx.Rows) error {
			var r content.SubmissionReceipt
			var version int64
			var exists bool
			if err := rows.Scan(&r.Key, &r.Fingerprint, &r.PostID, &r.Version, &r.Action, &r.CreatedAt, &version, &exists); err != nil {
				return err
			}
			// PG deliberately retains receipts after permanent deletion. The
			// file format stores that fact as an explicit Deleted marker.
			r.Deleted = !exists
			return b.EditorialReceipt(r, version, exists)
		}},
	}
	for _, step := range steps {
		if err = scan(step.sql, step.accept); errors.Is(err, content.ErrSnippetCapacity) {
			return finish("record_limit_exceeded")
		} else if err != nil {
			return content.SnippetInspectionReport{}, err
		}
	}
	return finish("")
}
