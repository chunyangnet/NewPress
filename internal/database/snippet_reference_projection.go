package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/contenthtml"
)

func queueSnippetProjection(batch *pgx.Batch, id int64, source string) {
	queueSnippetProjectionCounts(batch, id, contenthtml.CountSnippets(source))
}

func queueSnippetProjectionCounts(batch *pgx.Batch, id int64, counts []contenthtml.SnippetCount) {
	ids, versions, occurrences := make([]int64, len(counts)), make([]int64, len(counts)), make([]int32, len(counts))
	for i, count := range counts {
		ids[i], versions[i], occurrences[i] = count.ID, count.Version, int32(count.Count)
	}
	batch.Queue(`DELETE FROM np_post_snippet_refs WHERE post_id=$1`, id)
	if len(counts) > 0 {
		batch.Queue(`INSERT INTO np_post_snippet_refs(post_id,snippet_id,body_version,occurrences)
 SELECT $1,id,version,occurrences FROM unnest($2::bigint[],$3::bigint[],$4::integer[]) AS refs(id,version,occurrences)`, id, ids, versions, occurrences)
	}
	batch.Queue(`UPDATE np_posts SET snippet_refs_ready=true WHERE id=$1`, id)
}

// Caller holds the canonical post row through commit. Status-only mutations
// reuse the projection; source changes invalidate it even for older writers.
func updateSnippetProjection(ctx context.Context, tx pgx.Tx, id int64, source string) error {
	var ready bool
	if err := tx.QueryRow(ctx, `SELECT snippet_refs_ready FROM np_posts WHERE id=$1`, id).Scan(&ready); err != nil {
		return err
	}
	if ready {
		return nil
	}
	batch := &pgx.Batch{}
	queueSnippetProjection(batch, id, source)
	return tx.SendBatch(ctx, batch).Close()
}

func prepareSnippetProjectionBatch(ctx context.Context, tx pgx.Tx, all bool, author int64) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id,content_source FROM np_posts WHERE NOT snippet_refs_ready
 AND ($1 OR author_id=$2) ORDER BY id LIMIT 32 FOR UPDATE`, all, author)
	if err != nil {
		return 0, err
	}
	batch := &pgx.Batch{}
	count := 0
	for rows.Next() {
		var id int64
		var source string
		if err = rows.Scan(&id, &source); err != nil {
			rows.Close()
			return 0, err
		}
		if err = ctx.Err(); err != nil {
			rows.Close()
			return 0, err
		}
		queueSnippetProjection(batch, id, source)
		count++
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	return count, tx.SendBatch(ctx, batch).Close()
}

// PrepareSnippetReferences commits each batch separately before serving and
// after migration/restore. Resuming never changes editorial facts or revisions.
func PrepareSnippetReferences(ctx context.Context, pool *pgxpool.Pool) error {
	for {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		count, err := prepareSnippetProjectionBatch(ctx, tx, true, 0)
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("prepare snippet references: %w", err)
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
	}
}
