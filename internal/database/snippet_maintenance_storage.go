package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/packagefs"
)

// SQL constraints bind this normalized flag to the audit envelope. Reading the
// partial index avoids transferring permanently retained private evidence on
// every public request. Offline maintenance validates complete audit records.
func snippetMaintenanceGate(ctx context.Context, q postQuerier) error {
	var blocked bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM np_snippet_quarantines WHERE NOT repaired)`).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return content.ErrSnippetMaintenance
	}
	return nil
}

func readSnippetMaintenanceBundle(ctx context.Context, tx pgx.Tx, target SnippetMaintenanceTarget) (snippetRawBundle, error) {
	if target.Validate() != nil {
		return snippetRawBundle{}, ErrSnippetMaintenanceInput
	}
	b := snippetRawBundle{Target: target}
	if target.EditorialKey != "" {
		var size int
		if err := tx.QueryRow(ctx, `SELECT octet_length(key_hash)+octet_length(fingerprint)+octet_length(action) FROM np_content_submissions WHERE key_hash=$1`, target.EditorialKey).Scan(&size); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				err = content.ErrNotFound
			}
			return b, err
		}
		if size > 1024 {
			return b, content.ErrSnippetCapacity
		}
		r := content.SubmissionReceipt{}
		if err := tx.QueryRow(ctx, `SELECT key_hash,fingerprint,post_id,version,action,created_at FROM np_content_submissions WHERE key_hash=$1`, target.EditorialKey).
			Scan(&r.Key, &r.Fingerprint, &r.PostID, &r.Version, &r.Action, &r.CreatedAt); err != nil {
			return b, err
		}
		r.CreatedAt = r.CreatedAt.UTC()
		b.Editorial = &r
		return b, b.validateEnvelope()
	}
	var metadataSize, historyCount, historyBytes, historyMax, receiptCount, receiptBytes, receiptMax, indexMax int64
	err := tx.QueryRow(ctx, `SELECT
 (SELECT COALESCE(max(octet_length(data)+octet_length(visibility)),0) FROM np_snippets WHERE id=$1),
 (SELECT count(*) FROM np_snippet_revisions WHERE snippet_id=$1),
 (SELECT COALESCE(sum(octet_length(data)),0) FROM np_snippet_revisions WHERE snippet_id=$1),
 (SELECT COALESCE(max(octet_length(data)),0) FROM np_snippet_revisions WHERE snippet_id=$1),
 (SELECT count(*) FROM np_snippet_receipts WHERE snippet_id=$1),
 (SELECT COALESCE(sum(octet_length(data)),0) FROM np_snippet_receipts WHERE snippet_id=$1),
 (SELECT COALESCE(max(octet_length(data)),0) FROM np_snippet_receipts WHERE snippet_id=$1),
 (SELECT COALESCE(max(octet_length(identity)+octet_length(request_key)),0) FROM np_snippet_receipts WHERE snippet_id=$1)`, target.SnippetID).
		Scan(&metadataSize, &historyCount, &historyBytes, &historyMax, &receiptCount, &receiptBytes, &receiptMax, &indexMax)
	if err != nil {
		return b, err
	}
	if metadataSize > 4096+32 || historyCount > content.MaxSnippetRevisions || historyBytes+4096 > content.MaxSnippetStorageBytes || historyMax > 2<<20 ||
		receiptCount > content.MaxSnippetReceipts || receiptBytes > content.MaxSnippetReceiptBytes || receiptMax > 16384 || indexMax > 224 {
		return b, content.ErrSnippetCapacity
	}
	m := snippetRawRow{}
	err = tx.QueryRow(ctx, `SELECT id,version,visibility,data FROM np_snippets WHERE id=$1`, target.SnippetID).
		Scan(&m.Index.SnippetID, &m.Index.Version, &m.Index.Visibility, &m.Data)
	if err == nil {
		b.Metadata = &m
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return b, err
	}
	rows, err := tx.Query(ctx, `SELECT snippet_id,version,data FROM np_snippet_revisions WHERE snippet_id=$1 ORDER BY version LIMIT $2`, target.SnippetID, content.MaxSnippetRevisions+1)
	if err != nil {
		return b, err
	}
	for rows.Next() {
		row := snippetRawRow{}
		if err = rows.Scan(&row.Index.SnippetID, &row.Index.Version, &row.Data); err != nil {
			rows.Close()
			return b, err
		}
		b.Revisions = append(b.Revisions, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b, err
	}
	rows, err = tx.Query(ctx, `SELECT snippet_id,identity,request_key,data FROM np_snippet_receipts WHERE snippet_id=$1 ORDER BY identity COLLATE "C",request_key COLLATE "C" LIMIT $2`, target.SnippetID, content.MaxSnippetReceipts+1)
	if err != nil {
		return b, err
	}
	for rows.Next() {
		row := snippetRawRow{}
		if err = rows.Scan(&row.Index.SnippetID, &row.Index.Identity, &row.Index.Key, &row.Data); err != nil {
			rows.Close()
			return b, err
		}
		b.Receipts = append(b.Receipts, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b, err
	}
	if b.Metadata == nil && len(b.Revisions)+len(b.Receipts) == 0 {
		return b, content.ErrNotFound
	}
	return b, b.validateEnvelope()
}

func editorialMaintenancePost(ctx context.Context, tx pgx.Tx, b snippetRawBundle) (int64, bool, error) {
	if b.Editorial == nil {
		return 0, false, nil
	}
	var version int64
	err := tx.QueryRow(ctx, `SELECT version FROM np_posts WHERE id=$1`, b.Editorial.PostID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return version, err == nil, err
}

func ReviewSnippetMaintenance(ctx context.Context, pool *pgxpool.Pool, target SnippetMaintenanceTarget) (SnippetMaintenanceReview, error) {
	if err := target.Validate(); err != nil {
		return SnippetMaintenanceReview{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return SnippetMaintenanceReview{}, err
	}
	defer tx.Rollback(context.Background())
	b, err := readSnippetMaintenanceBundle(ctx, tx, target)
	if err != nil {
		return SnippetMaintenanceReview{}, err
	}
	version, exists, err := editorialMaintenancePost(ctx, tx, b)
	if err != nil {
		return SnippetMaintenanceReview{}, err
	}
	result, err := reviewSnippetBundle(b, version, exists)
	if err != nil {
		return SnippetMaintenanceReview{}, err
	}
	return result, tx.Commit(ctx)
}

func ExportSnippetRepair(ctx context.Context, pool *pgxpool.Pool, target SnippetMaintenanceTarget) ([]byte, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if err = snippetMaintenanceGate(ctx, tx); err != nil {
		return nil, err
	}
	b, err := readSnippetMaintenanceBundle(ctx, tx, target)
	if err != nil {
		return nil, err
	}
	version, exists, err := editorialMaintenancePost(ctx, tx, b)
	if err != nil {
		return nil, err
	}
	review, err := reviewSnippetBundle(b, version, exists)
	if err != nil || !review.Healthy {
		return nil, ErrSnippetMaintenanceIncomplete
	}
	doc := SnippetRepairDocument{Schema: 1, Kind: "newpress.snippet-repair", Editorial: b.Editorial}
	if b.Metadata != nil {
		s, err := packagefs.DecodeJSON[content.Snippet](bytes.NewReader(b.Metadata.Data), 4096, "snippet")
		if err != nil {
			return nil, ErrSnippetMaintenanceEvidence
		}
		doc.Snippet = &content.SnippetRecord{Snippet: s}
		for _, row := range b.Revisions {
			rev, e := packagefs.DecodeJSON[content.SnippetRevision](bytes.NewReader(row.Data), 2<<20, "snippet revision")
			if e != nil {
				return nil, ErrSnippetMaintenanceEvidence
			}
			doc.Snippet.Revisions = append(doc.Snippet.Revisions, rev)
		}
		for _, row := range b.Receipts {
			receipt, e := packagefs.DecodeJSON[content.SnippetReceipt](bytes.NewReader(row.Data), 16384, "snippet receipt")
			if e != nil {
				return nil, ErrSnippetMaintenanceEvidence
			}
			doc.Receipts = append(doc.Receipts, receipt)
		}
	}
	raw, err := json.Marshal(doc)
	if err != nil || len(raw) > MaxSnippetRepairBytes {
		return nil, content.ErrSnippetCapacity
	}
	return raw, tx.Commit(ctx)
}

func loadSnippetQuarantines(ctx context.Context, tx pgx.Tx) ([]snippetQuarantine, error) {
	var count, size, largest int64
	if err := tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(octet_length(data)+CASE WHEN repaired THEN 0 ELSE $1 END),0),COALESCE(max(octet_length(data)),0) FROM np_snippet_quarantines`, snippetRepairReserve).Scan(&count, &size, &largest); err != nil {
		return nil, err
	}
	if count > MaxSnippetQuarantines || size > MaxSnippetQuarantineBytes || largest > MaxSnippetQuarantineRecordBytes {
		return nil, content.ErrSnippetCapacity
	}
	rows, err := tx.Query(ctx, `SELECT id,scope,reference,captured_at,repaired,data FROM np_snippet_quarantines ORDER BY id LIMIT $1`, MaxSnippetQuarantines+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []snippetQuarantine{}
	for rows.Next() {
		var id, scope, reference string
		var at time.Time
		var repaired bool
		var raw []byte
		if err = rows.Scan(&id, &scope, &reference, &at, &repaired, &raw); err != nil {
			return nil, err
		}
		q, e := packagefs.DecodeJSON[snippetQuarantine](bytes.NewReader(raw), MaxSnippetQuarantineRecordBytes, "snippet quarantine")
		if e != nil || q.validate() != nil || q.ID != id || q.Capture.Target.scope() != scope || q.Capture.Reference != reference || !q.CapturedAt.Equal(at) || repaired != (q.Repair != nil) {
			return nil, ErrSnippetMaintenanceEvidence
		}
		result = append(result, q)
	}
	return result, rows.Err()
}

type SnippetQuarantinePage struct {
	Total      int                        `json:"total"`
	Unresolved int                        `json:"unresolved"`
	NextOffset int                        `json:"next_offset"`
	Items      []SnippetQuarantineSummary `json:"items"`
}

func ListSnippetQuarantines(ctx context.Context, pool *pgxpool.Pool, query content.SnippetInspectionQuery) (SnippetQuarantinePage, error) {
	if err := query.Validate(); err != nil {
		return SnippetQuarantinePage{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return SnippetQuarantinePage{}, err
	}
	defer tx.Rollback(context.Background())
	all, err := loadSnippetQuarantines(ctx, tx)
	if err != nil {
		return SnippetQuarantinePage{}, err
	}
	result := SnippetQuarantinePage{Total: len(all), NextOffset: -1, Items: []SnippetQuarantineSummary{}}
	for i, q := range all {
		if q.Repair == nil {
			result.Unresolved++
		}
		if i >= query.Offset && len(result.Items) < query.Limit {
			result.Items = append(result.Items, q.summary())
		}
	}
	if query.Offset+len(result.Items) < len(all) {
		result.NextOffset = query.Offset + len(result.Items)
	}
	return result, tx.Commit(ctx)
}
