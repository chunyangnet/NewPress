package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/content"
)

func (r *PostRepository) WalkMediaSnapshots(ctx context.Context, visit func(content.MediaSnapshot) error) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err = snippetMaintenanceGate(ctx, tx); err != nil {
		return err
	}
	// The gate and traversal see one MVCC snapshot across history and series.
	// Stream rows instead of materializing every historical body in Go memory.
	rows, err := tx.Query(ctx, `
		SELECT p.id, CASE WHEN u.status='system' THEN 0 ELSE COALESCE(p.author_id,0) END,
		       p.status, records.source, records.revision_id, records.owner_key, records.snapshot
		FROM np_posts p
		LEFT JOIN np_users u ON u.id=p.author_id
		CROSS JOIN LATERAL (
			SELECT 0 AS source, 0::bigint AS revision_id, ''::text AS owner_key,
			       jsonb_build_object('version',p.version,'type',p.type,'slug',p.slug,'title',p.title,
			         'cover_url',p.cover_url,'content_source',p.content_source,'content_html',p.content_html,'meta',p.metadata) AS snapshot
			UNION ALL
			SELECT 1, id, '', snapshot FROM np_post_revisions WHERE post_id=p.id
			UNION ALL
			SELECT 2, 0, owner_key, snapshot FROM np_post_autosaves WHERE post_id=p.id AND expires_at>statement_timestamp()
		) records
		UNION ALL
		SELECT s.id, 0::bigint, s.status, 3, 0::bigint, '',
		       jsonb_build_object('version',s.version,'type','series','slug',s.slug,'title',s.title,'cover_url',s.cover_url)
		FROM np_series s
		UNION ALL
		SELECT s.id, 0::bigint, s.visibility, 4, r.version, '',
		       ((convert_from(r.data,'UTF8')::jsonb - 'actor') || jsonb_build_object('type','snippet'))
		FROM np_snippets s JOIN np_snippet_revisions r ON r.snippet_id=s.id
		ORDER BY 4, 1, 5 DESC, 6`)
	if err != nil {
		return fmt.Errorf("scan media references: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var current content.Post
		var source int
		var revisionID int64
		var owner string
		var encoded []byte
		if err := rows.Scan(&current.ID, &current.AuthorID, &current.Status, &source, &revisionID, &owner, &encoded); err != nil {
			return fmt.Errorf("read media reference snapshot: %w", err)
		}
		snapshot, err := decodeContentJSON[content.Post](encoded)
		if err != nil {
			return fmt.Errorf("decode media reference snapshot: %w", err)
		}
		kind := []string{content.MediaSourceCurrent, content.MediaSourceRevision, content.MediaSourceAutosave, content.MediaSourceSeries, content.MediaSourceSnippet}[source]
		if err := visit(content.MediaSnapshotOf(current, snapshot, kind, revisionID, owner)); err != nil {
			return err
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
