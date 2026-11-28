package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"newpress.local/newpress/internal/content"
)

func (r *PostRepository) SaveAutosave(ctx context.Context, autosave content.Autosave) (content.Autosave, error) {
	var err error
	autosave, err = content.NormalizeAutosave(autosave)
	if err != nil {
		return content.Autosave{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.Autosave{}, fmt.Errorf("begin save post autosave: %w", err)
	}
	defer tx.Rollback(ctx)
	current, err := queryPostByID(ctx, tx, autosave.PostID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Autosave{}, content.ErrNotFound
	}
	if err != nil {
		return content.Autosave{}, fmt.Errorf("lock autosave post: %w", err)
	}
	if current.Version != autosave.SourceVersion || autosave.Snapshot.Version != autosave.SourceVersion {
		return content.Autosave{}, content.ErrVersionConflict
	}
	if autosave.Snapshot.ID != current.ID || autosave.Snapshot.AuthorID != current.AuthorID {
		return content.Autosave{}, &content.ValidationError{Fields: map[string]string{"snapshot": "自动保存快照与内容对象不匹配"}}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if !autosave.ExpiresAt.After(now) {
		return content.Autosave{}, &content.ValidationError{Fields: map[string]string{"expires_at": "自动保存过期时间必须晚于当前时间"}}
	}
	snapshot, err := json.Marshal(autosave.Snapshot)
	if err != nil {
		return content.Autosave{}, fmt.Errorf("encode post autosave: %w", err)
	}
	var storedSnapshot []byte
	err = tx.QueryRow(ctx, `
		INSERT INTO np_post_autosaves(post_id,owner_key,source_version,snapshot,updated_at,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (post_id,owner_key) DO UPDATE SET
			source_version=EXCLUDED.source_version,
			snapshot=EXCLUDED.snapshot,
			updated_at=GREATEST(EXCLUDED.updated_at,np_post_autosaves.updated_at + interval '1 microsecond'),
			expires_at=EXCLUDED.expires_at
		RETURNING source_version,snapshot,updated_at,expires_at`,
		autosave.PostID, autosave.OwnerKey, autosave.SourceVersion, snapshot, now, autosave.ExpiresAt.UTC(),
	).Scan(&autosave.SourceVersion, &storedSnapshot, &autosave.UpdatedAt, &autosave.ExpiresAt)
	if err != nil {
		return content.Autosave{}, fmt.Errorf("save post autosave: %w", err)
	}
	autosave.Snapshot, err = decodeContentJSON[content.Post](storedSnapshot)
	if err != nil {
		return content.Autosave{}, fmt.Errorf("decode saved post autosave: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return content.Autosave{}, fmt.Errorf("commit post autosave: %w", err)
	}
	return autosave, nil
}

func (r *PostRepository) GetAutosave(ctx context.Context, postID int64, ownerKey string) (content.Autosave, error) {
	if postID <= 0 || !content.ValidAutosaveOwnerKey(ownerKey) {
		return content.Autosave{}, content.ErrAutosaveNotFound
	}
	var autosave content.Autosave
	var snapshot []byte
	err := r.pool.QueryRow(ctx, `
		SELECT post_id,owner_key,source_version,snapshot,updated_at,expires_at
		FROM np_post_autosaves
		WHERE post_id=$1 AND owner_key=$2 AND expires_at > now()`, postID, ownerKey).Scan(
		&autosave.PostID, &autosave.OwnerKey, &autosave.SourceVersion, &snapshot, &autosave.UpdatedAt, &autosave.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Autosave{}, content.ErrAutosaveNotFound
	}
	if err != nil {
		return content.Autosave{}, fmt.Errorf("load post autosave: %w", err)
	}
	autosave.Snapshot, err = decodeContentJSON[content.Post](snapshot)
	if err != nil {
		return content.Autosave{}, fmt.Errorf("decode post autosave: %w", err)
	}
	return content.NormalizeAutosave(autosave)
}

func (r *PostRepository) GetAutosaveState(ctx context.Context, postID int64, ownerKey string) (content.AutosaveState, error) {
	if err := ctx.Err(); err != nil {
		return content.AutosaveState{}, err
	}
	if postID <= 0 || !content.ValidAutosaveOwnerKey(ownerKey) {
		return content.AutosaveState{}, content.ErrAutosaveNotFound
	}
	var state content.AutosaveState
	err := r.pool.QueryRow(ctx, `
		SELECT p.id, CASE WHEN u.status='system' THEN 0 ELSE COALESCE(p.author_id,0) END,
		       p.type,p.version,a.source_version,a.updated_at,a.expires_at
		FROM np_post_autosaves a JOIN np_posts p ON p.id=a.post_id
		LEFT JOIN np_users u ON u.id=p.author_id
		WHERE a.post_id=$1 AND a.owner_key=$2 AND a.expires_at > now()`, postID, ownerKey).Scan(
		&state.PostID, &state.AuthorID, &state.Type, &state.PostVersion,
		&state.SourceVersion, &state.UpdatedAt, &state.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.AutosaveState{}, content.ErrAutosaveNotFound
	}
	if err != nil {
		return content.AutosaveState{}, fmt.Errorf("load post autosave state: %w", err)
	}
	return state, ctx.Err()
}

func (r *PostRepository) DeleteAutosave(ctx context.Context, postID int64, ownerKey string) error {
	if postID <= 0 || !content.ValidAutosaveOwnerKey(ownerKey) {
		return nil
	}
	if _, err := r.pool.Exec(ctx, `DELETE FROM np_post_autosaves WHERE post_id=$1 AND owner_key=$2`, postID, ownerKey); err != nil {
		return fmt.Errorf("delete post autosave: %w", err)
	}
	return nil
}

func deletePostAutosaves(ctx context.Context, tx pgx.Tx, postID int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM np_post_autosaves WHERE post_id=$1`, postID); err != nil {
		return fmt.Errorf("clear post autosaves: %w", err)
	}
	return nil
}
