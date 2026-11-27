package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/content"
)

func updatePublicSearchBody(ctx context.Context, tx pgx.Tx, id int64, contentHTML string) error {
	_, err := tx.Exec(ctx, `UPDATE np_posts SET public_search_body=$2
        WHERE id=$1 AND content_html=$3 AND public_search_body IS NULL`, id, content.PublicSearchBody(contentHTML), contentHTML)
	return err
}

// Old rows are backfilled in the same snapshot as search/count. Only visible,
// selected rows are touched, with bounded HTML batches and all-or-nothing commit.
func preparePublicSearchBodies(ctx context.Context, tx pgx.Tx, from string, args []any) error {
	for {
		count, err := preparePublicSearchBodyBatch(ctx, tx, from, args)
		if err != nil || count == 0 {
			return err
		}
	}
}

// PreparePublicSearch backfills old rows before serving or after `migrate`.
// Each bounded batch commits independently so cancellation can resume safely.
// No editorial version, timestamp, revision or event is created by this rebuild.
func PreparePublicSearch(ctx context.Context, pool *pgxpool.Pool) error {
	for {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin public search preparation: %w", err)
		}
		count, err := preparePublicSearchBodyBatch(ctx, tx, " FROM np_posts p WHERE true", nil)
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit public search preparation: %w", err)
		}
		if count == 0 {
			return nil
		}
	}
}

func preparePublicSearchBodyBatch(ctx context.Context, tx pgx.Tx, from string, args []any) (int, error) {
	rows, err := tx.Query(ctx, "SELECT p.id,p.content_html"+from+` AND p.public_search_body IS NULL ORDER BY p.id LIMIT 32 FOR UPDATE OF p`, args...)
	if err != nil {
		return 0, fmt.Errorf("read missing search projections: %w", err)
	}
	batch := &pgx.Batch{}
	for rows.Next() {
		var id int64
		var body string
		if err := rows.Scan(&id, &body); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan missing search projection: %w", err)
		}
		if err := ctx.Err(); err != nil {
			rows.Close()
			return 0, err
		}
		batch.Queue(`UPDATE np_posts SET public_search_body=$2 WHERE id=$1`, id, content.PublicSearchBody(body))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read search projections: %w", err)
	}
	if batch.Len() == 0 {
		return 0, nil
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return 0, fmt.Errorf("build search projections: %w", err)
	}
	return batch.Len(), nil
}
