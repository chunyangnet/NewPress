package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/content"
)

func snippetDraftPlan(ctx context.Context, tx pgx.Tx, u content.SnippetDraftRequest, a content.SnippetAccess, lock bool) (content.SnippetDraftPlan, error) {
	sql := `SELECT ` + postColumns + ` FROM np_posts p LEFT JOIN np_users u ON u.id=p.author_id WHERE p.id=$1`
	if lock {
		sql += ` FOR SHARE OF p`
	}
	p, err := scanPost(tx.QueryRow(ctx, sql, u.PostID))
	if errors.Is(err, pgx.ErrNoRows) {
		err = content.ErrNotFound
	}
	if err != nil {
		return content.SnippetDraftPlan{}, err
	}
	keys, err := content.SnippetDraftKeys(p, u, a)
	if err != nil {
		return content.SnippetDraftPlan{}, err
	}
	bodies, err := loadSharedSnippetBodies(ctx, tx, keys)
	if err != nil {
		return content.SnippetDraftPlan{}, err
	}
	return content.BuildSnippetDraft(p, u, a, bodies)
}

func (r *PostRepository) PreviewSnippetDraft(ctx context.Context, u content.SnippetDraftRequest, a content.SnippetAccess) (content.SnippetDraftPlan, error) {
	if err := content.ValidateSnippetDraft(u, false); err != nil {
		return content.SnippetDraftPlan{}, err
	}
	if err := content.CheckSnippetAccess(a, true); err != nil {
		return content.SnippetDraftPlan{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.SnippetDraftPlan{}, err
	}
	defer tx.Rollback(ctx)
	plan, err := snippetDraftPlan(ctx, tx, u, a, false)
	if err != nil {
		return content.SnippetDraftPlan{}, err
	}
	return plan, tx.Commit(ctx)
}

func checkSnippetDraftReplay(ctx context.Context, tx pgx.Tx, receipt content.SubmissionReceipt, a content.SnippetAccess) error {
	var p content.Post
	err := tx.QueryRow(ctx, `SELECT id,coalesce(author_id,0) FROM np_posts WHERE id=$1 FOR SHARE`, receipt.PostID).Scan(&p.ID, &p.AuthorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.ErrSubmissionResultUnavailable
	}
	if err != nil {
		return err
	}
	if !content.SnippetPostWritable(p, a) {
		return content.ErrSnippetPermission
	}
	return nil
}
