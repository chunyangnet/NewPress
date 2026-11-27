package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/packagefs"
)

func (r *PostRepository) SnippetImpacts(ctx context.Context, id int64, a content.SnippetAccess, limit, offset int) (content.SnippetImpacts, error) {
	for attempt := 0; ; attempt++ {
		result, err := r.snippetImpactsSnapshot(ctx, id, a, limit, offset)
		var pgErr *pgconn.PgError
		if attempt >= 2 || !errors.As(err, &pgErr) || pgErr.Code != "40001" || ctx.Err() != nil {
			return result, err
		}
	}
}

func (r *PostRepository) snippetImpactsSnapshot(ctx context.Context, id int64, a content.SnippetAccess, limit, offset int) (content.SnippetImpacts, error) {
	if err := content.CheckSnippetAccess(a, true); err != nil {
		return content.SnippetImpacts{}, err
	}
	if limit <= 0 {
		limit = 24
	}
	limit = min(limit, 100)
	offset = max(0, offset)
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return content.SnippetImpacts{}, err
	}
	defer tx.Rollback(ctx)
	s, err := querySnippet(ctx, tx, id, false)
	if err != nil {
		return content.SnippetImpacts{}, err
	}
	if !content.SnippetReadable(s, a) {
		return content.SnippetImpacts{}, content.ErrNotFound
	}
	result := content.SnippetImpacts{Snippet: s, Items: []content.SnippetImpact{}}
	for {
		count, e := prepareSnippetProjectionBatch(ctx, tx, a.Manage, a.UserID)
		if e != nil {
			return result, e
		}
		if count == 0 {
			break
		}
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM np_posts p WHERE ($1 OR p.author_id=$2)
 AND EXISTS(SELECT 1 FROM np_post_snippet_refs ref WHERE ref.post_id=p.id AND ref.snippet_id=$3)`, a.Manage, a.UserID, id).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `WITH selected AS (
 SELECT p.id,p.version,p.title,p.status FROM np_posts p WHERE ($1 OR p.author_id=$2)
 AND EXISTS(SELECT 1 FROM np_post_snippet_refs ref WHERE ref.post_id=p.id AND ref.snippet_id=$3)
 ORDER BY p.id LIMIT $5 OFFSET $6)
 SELECT p.id,p.version,p.title,p.status,array_agg(ref.body_version ORDER BY ref.body_version),
 sum(ref.occurrences),sum(CASE WHEN ref.body_version<>$4 THEN ref.occurrences ELSE 0 END)
 FROM selected p JOIN np_post_snippet_refs ref ON ref.post_id=p.id AND ref.snippet_id=$3
 GROUP BY p.id,p.version,p.title,p.status ORDER BY p.id`, a.Manage, a.UserID, id, s.CurrentVersion, limit, offset)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var impact content.SnippetImpact
		if err = rows.Scan(&impact.ID, &impact.Version, &impact.Title, &impact.Status, &impact.Versions, &impact.References, &impact.Changes); err != nil {
			rows.Close()
			return result, err
		}
		result.Items = append(result.Items, impact)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func snippetPlan(ctx context.Context, tx pgx.Tx, u content.SnippetUpdate, a content.SnippetAccess, lock bool) (content.SnippetPlan, []content.Post, error) {
	s, err := querySnippet(ctx, tx, u.SnippetID, lock)
	if err != nil {
		return content.SnippetPlan{}, nil, err
	}
	ids := make([]int64, len(u.Items))
	for i, item := range u.Items {
		ids[i] = item.ID
	}
	sql := `SELECT ` + postColumns + ` FROM np_posts p LEFT JOIN np_users u ON u.id=p.author_id WHERE p.id=ANY($1::bigint[]) ORDER BY p.id`
	if lock {
		sql += ` FOR UPDATE OF p`
	}
	rows, err := tx.Query(ctx, sql, ids)
	if err != nil {
		return content.SnippetPlan{}, nil, err
	}
	posts := []content.Post{}
	for rows.Next() {
		p, e := scanPost(rows)
		if e != nil {
			rows.Close()
			return content.SnippetPlan{}, nil, e
		}
		posts = append(posts, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return content.SnippetPlan{}, nil, err
	}
	plan, err := content.BuildSnippetPlan(s, posts, u, a)
	return plan, posts, err
}

func (r *PostRepository) PreviewSnippetUpdate(ctx context.Context, u content.SnippetUpdate, a content.SnippetAccess) (content.SnippetPlan, error) {
	if err := content.CheckSnippetAccess(a, true); err != nil {
		return content.SnippetPlan{}, err
	}
	u, err := content.NormalizeSnippetUpdate(u, false)
	if err != nil {
		return content.SnippetPlan{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.SnippetPlan{}, err
	}
	defer tx.Rollback(ctx)
	plan, _, err := snippetPlan(ctx, tx, u, a, false)
	if err != nil {
		return plan, err
	}
	return plan, tx.Commit(ctx)
}

func (r *PostRepository) ApplySnippetUpdate(ctx context.Context, u content.SnippetUpdate, a content.SnippetAccess) (content.SnippetResult, error) {
	if err := content.CheckSnippetAccess(a, true); err != nil {
		return content.SnippetResult{}, err
	}
	u, err := content.NormalizeSnippetUpdate(u, true)
	if err != nil {
		return content.SnippetResult{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.SnippetResult{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, snippetWriteLock); err != nil {
		return content.SnippetResult{}, err
	}
	if err = snippetMaintenanceGate(ctx, tx); err != nil {
		return content.SnippetResult{}, err
	}
	a, err = lockSnippetAccount(ctx, tx, a)
	if err != nil {
		return content.SnippetResult{}, err
	}
	var raw []byte
	var snippetID int64
	err = tx.QueryRow(ctx, `SELECT snippet_id,data FROM np_snippet_receipts WHERE identity=$1 AND request_key=$2`, a.Identity, u.RequestKey).Scan(&snippetID, &raw)
	if err == nil {
		receipt, e := packagefs.DecodeJSON[content.SnippetReceipt](bytes.NewReader(raw), 16384, "snippet receipt")
		if e != nil || content.ValidateSnippetReceipt(receipt) != nil || receipt.SnippetID != snippetID || receipt.Identity != a.Identity || receipt.RequestKey != u.RequestKey {
			return content.SnippetResult{}, content.ErrSnippetCorrupt
		}
		if receipt.Fingerprint != content.SnippetUpdateFingerprint(u, a) {
			return content.SnippetResult{}, content.ErrSnippetReplay
		}
		return content.SnippetResult{Receipt: receipt, Replayed: true}, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return content.SnippetResult{}, err
	}
	plan, posts, err := snippetPlan(ctx, tx, u, a, true)
	if err != nil {
		return content.SnippetResult{}, err
	}
	if plan.Digest != u.Digest {
		return content.SnippetResult{}, content.ErrVersionConflict
	}
	now := time.Now().UTC()
	receipt := content.NewSnippetReceipt(u, plan, a, now)
	raw, err = json.Marshal(receipt)
	if err != nil {
		return content.SnippetResult{}, err
	}
	var count, size int64
	if err = tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(octet_length(data)),0) FROM np_snippet_receipts`).Scan(&count, &size); err != nil {
		return content.SnippetResult{}, err
	}
	if count >= content.MaxSnippetReceipts || size+int64(len(raw)) > content.MaxSnippetReceiptBytes {
		return content.SnippetResult{}, content.ErrSnippetCapacity
	}
	if err = hydratePostTerms(ctx, tx, posts); err != nil {
		return content.SnippetResult{}, err
	}
	for _, p := range posts {
		p.ContentSource, _ = contenthtml.UpdateSnippetReferences(p.ContentSource, u.SnippetID, plan.Snippet.CurrentVersion)
		p.ContentHTML = contenthtml.Sanitize(p.ContentSource)
		p.Version++
		p.UpdatedAt = now
		if _, err = tx.Exec(ctx, `UPDATE np_posts SET content_source=$2,content_html=$3,version=$4,updated_at=$5 WHERE id=$1`, p.ID, p.ContentSource, p.ContentHTML, p.Version, now); err != nil {
			return content.SnippetResult{}, err
		}
		if err = insertRevision(ctx, tx, p, a.Actor); err != nil {
			return content.SnippetResult{}, err
		}
		if err = deletePostAutosaves(ctx, tx, p.ID); err != nil {
			return content.SnippetResult{}, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO np_snippet_receipts(identity,request_key,snippet_id,data) VALUES($1,$2,$3,$4)`, a.Identity, u.RequestKey, u.SnippetID, raw); err != nil {
		return content.SnippetResult{}, err
	}
	return content.SnippetResult{Receipt: receipt}, tx.Commit(ctx)
}
