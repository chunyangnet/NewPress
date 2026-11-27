package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/idempotency"
)

func scanCommentSubmission(row pgx.Row, fingerprint string) (comment.SubmissionResult, error) {
	var receipt comment.SubmissionReceipt
	err := row.Scan(&receipt.Key, &receipt.Fingerprint, &receipt.CommentID, &receipt.PostID, &receipt.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return comment.SubmissionResult{}, comment.ErrNotFound
	}
	if err != nil {
		return comment.SubmissionResult{}, err
	}
	if receipt.Fingerprint != fingerprint {
		return comment.SubmissionResult{}, idempotency.ErrConflict
	}
	return comment.SubmissionResult{Receipt: receipt, Replayed: true}, nil
}

const commentSubmissionQuery = `SELECT key_hash,fingerprint,comment_id,post_id,created_at FROM np_comment_submissions WHERE key_hash=$1`

func (repository *CommentRepository) FindSubmission(ctx context.Context, key, fingerprint string) (comment.SubmissionResult, error) {
	if repository == nil || repository.pool == nil {
		return comment.SubmissionResult{}, comment.ErrInvalidInput
	}
	if err := comment.ValidateSubmissionKey(key, fingerprint); err != nil {
		return comment.SubmissionResult{}, err
	}
	return scanCommentSubmission(repository.pool.QueryRow(ctx, commentSubmissionQuery, key), fingerprint)
}

func (repository *CommentRepository) CreateOnce(ctx context.Context, request comment.Submission) (comment.SubmissionResult, error) {
	if repository == nil || repository.pool == nil {
		return comment.SubmissionResult{}, comment.ErrInvalidInput
	}
	if err := comment.ValidateSubmissionKey(request.Key, request.Fingerprint); err != nil {
		return comment.SubmissionResult{}, err
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return comment.SubmissionResult{}, err
	}
	defer tx.Rollback(ctx)
	// Bound the registry and serialize same-key creates across independent
	// processes. The transaction lock is released on rollback or connection loss.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-comment-submissions', 0))`); err != nil {
		return comment.SubmissionResult{}, err
	}
	result, err := scanCommentSubmission(tx.QueryRow(ctx, commentSubmissionQuery, request.Key), request.Fingerprint)
	if !errors.Is(err, comment.ErrNotFound) {
		return result, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM np_comment_submissions`).Scan(&count); err != nil {
		return comment.SubmissionResult{}, err
	}
	if count >= comment.MaxSubmissionReceipts {
		return comment.SubmissionResult{}, idempotency.ErrCapacity
	}
	input, fields := comment.NormalizeCreateInput(request.Input)
	if len(fields) > 0 {
		return comment.SubmissionResult{}, &comment.ValidationError{Fields: fields}
	}
	item, err := createCommentInTransaction(ctx, tx, input)
	if err != nil {
		return comment.SubmissionResult{}, err
	}
	receipt := comment.SubmissionReceipt{Key: request.Key, Fingerprint: request.Fingerprint, CommentID: item.ID, PostID: item.PostID, CreatedAt: item.CreatedAt}
	if _, err := tx.Exec(ctx, `INSERT INTO np_comment_submissions(key_hash,fingerprint,comment_id,post_id,created_at) VALUES($1,$2,$3,$4,$5)`, receipt.Key, receipt.Fingerprint, receipt.CommentID, receipt.PostID, receipt.CreatedAt); err != nil {
		return comment.SubmissionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return comment.SubmissionResult{}, fmt.Errorf("commit comment submission: %w", err)
	}
	return comment.SubmissionResult{Receipt: receipt}, nil
}
