package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/idempotency"
)

func (r *PostRepository) FindSubmission(ctx context.Context, key, fingerprint string) (content.SubmissionResult, error) {
	if err := content.ValidateSubmission(content.Submission{Key: key, Fingerprint: fingerprint, Action: "create"}); err != nil {
		return content.SubmissionResult{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.SubmissionResult{}, err
	}
	defer tx.Rollback(context.Background())
	if err = snippetMaintenanceGate(ctx, tx); err != nil {
		return content.SubmissionResult{}, err
	}
	var receipt content.SubmissionReceipt
	err = tx.QueryRow(ctx, `SELECT key_hash,fingerprint,post_id,version,action,created_at FROM np_content_submissions
		WHERE key_hash=$1 AND (action='create' OR created_at > $2)`, key, time.Now().UTC().Add(-content.SubmissionReplayWindow)).
		Scan(&receipt.Key, &receipt.Fingerprint, &receipt.PostID, &receipt.Version, &receipt.Action, &receipt.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.SubmissionResult{}, content.ErrNotFound
	}
	if err != nil {
		return content.SubmissionResult{}, err
	}
	if receipt.Fingerprint != fingerprint {
		return content.SubmissionResult{}, idempotency.ErrConflict
	}
	return content.SubmissionResult{Receipt: receipt, Replayed: true}, tx.Commit(ctx)
}

func (r *PostRepository) Submit(ctx context.Context, request content.Submission) (content.SubmissionResult, error) {
	if err := content.ValidateSubmission(request); err != nil {
		return content.SubmissionResult{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.SubmissionResult{}, fmt.Errorf("begin editorial submission: %w", err)
	}
	defer tx.Rollback(ctx)
	if g := request.SnippetDraft; g != nil {
		// This order matches snippet mutations. Ordinary editorial operations
		// never acquire snippetWriteLock after their submission lock.
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, snippetWriteLock); err != nil {
			return content.SubmissionResult{}, err
		}
		access, e := lockSnippetAccount(ctx, tx, g.Access)
		if e != nil {
			return content.SubmissionResult{}, e
		}
		request.SnippetDraft = &content.SnippetDraftGuard{Request: g.Request, Access: access}
	}
	// Coordinate retention/capacity checks and same-key writers across nodes.
	// No pending lease survives a failed transaction or a crashed connection.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-editorial-submissions', 0))`); err != nil {
		return content.SubmissionResult{}, err
	}
	// Keep all receipt identities and retention state untouched while any
	// unresolved canonical evidence might represent an earlier successful write.
	if err = snippetMaintenanceGate(ctx, tx); err != nil {
		return content.SubmissionResult{}, err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `DELETE FROM np_content_submissions WHERE action <> 'create' AND created_at <= $1`, now.Add(-content.SubmissionReplayWindow)); err != nil {
		return content.SubmissionResult{}, err
	}
	var receipt content.SubmissionReceipt
	err = tx.QueryRow(ctx, `SELECT key_hash,fingerprint,post_id,version,action,created_at FROM np_content_submissions WHERE key_hash=$1`, request.Key).
		Scan(&receipt.Key, &receipt.Fingerprint, &receipt.PostID, &receipt.Version, &receipt.Action, &receipt.CreatedAt)
	if err == nil {
		if receipt.Fingerprint != request.Fingerprint {
			return content.SubmissionResult{}, idempotency.ErrConflict
		}
		if g := request.SnippetDraft; g != nil {
			if err = checkSnippetDraftReplay(ctx, tx, receipt, g.Access); err != nil {
				return content.SubmissionResult{}, err
			}
		}
		return content.SubmissionResult{Receipt: receipt, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return content.SubmissionResult{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM np_content_submissions`).Scan(&count); err != nil {
		return content.SubmissionResult{}, err
	}
	if count >= content.MaxSubmissionReceipts {
		return content.SubmissionResult{}, idempotency.ErrCapacity
	}
	if g := request.SnippetDraft; g != nil {
		plan, e := snippetDraftPlan(ctx, tx, g.Request, g.Access, true)
		if e != nil {
			return content.SubmissionResult{}, e
		}
		if plan.Digest != g.Request.Digest {
			return content.SubmissionResult{}, content.ErrVersionConflict
		}
		request.Input = plan.Input(g.Access, request.Key)
	}
	mutation := content.Mutation{ExpectedVersion: request.Version, Actor: request.Actor}
	var post content.Post
	switch request.MutationAction() {
	case "create", "save":
		normalized, fields := content.NormalizePostInput(request.Input, now)
		if len(fields) > 0 {
			return content.SubmissionResult{}, &content.ValidationError{Fields: fields}
		}
		if request.Action == "create" {
			post, err = createImportedOrRegularPost(ctx, tx, normalized, now)
		} else {
			post, err = updatePostInTransaction(ctx, tx, request.PostID, mutation, normalized, now)
		}
	case "restore_revision":
		post, err = restorePostRevisionInTransaction(ctx, tx, request.PostID, request.RevisionID, mutation, now)
	default:
		status := map[string]string{"trash": content.StatusTrash, "restore": content.StatusDraft, "publish": content.StatusPublish}[request.Action]
		post, err = changePostStatusInTransaction(ctx, tx, request.PostID, mutation, status, now)
	}
	if err != nil {
		return content.SubmissionResult{}, err
	}
	receipt = content.SubmissionReceipt{Key: request.Key, Fingerprint: request.Fingerprint, PostID: post.ID, Version: post.Version, Action: request.Action, CreatedAt: now}
	if _, err := tx.Exec(ctx, `INSERT INTO np_content_submissions(key_hash,fingerprint,post_id,version,action,created_at) VALUES($1,$2,$3,$4,$5,$6)`,
		receipt.Key, receipt.Fingerprint, receipt.PostID, receipt.Version, receipt.Action, receipt.CreatedAt); err != nil {
		return content.SubmissionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return content.SubmissionResult{}, fmt.Errorf("commit editorial submission: %w", err)
	}
	return content.SubmissionResult{Receipt: receipt}, nil
}
