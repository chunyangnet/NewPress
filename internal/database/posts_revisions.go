package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"newpress.local/newpress/internal/content"
)

func (r *PostRepository) ListRevisions(ctx context.Context, postID int64, limit int) ([]content.Revision, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM np_posts WHERE id=$1)`, postID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check revision post: %w", err)
	}
	if !exists {
		return nil, content.ErrNotFound
	}
	rows, err := r.pool.Query(ctx, `SELECT id,post_id,post_version,snapshot,actor,created_at FROM np_post_revisions WHERE post_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2`, postID, limit)
	if err != nil {
		return nil, fmt.Errorf("list post revisions: %w", err)
	}
	defer rows.Close()
	revisions := make([]content.Revision, 0, limit)
	for rows.Next() {
		var revision content.Revision
		var snapshot []byte
		if err := rows.Scan(&revision.ID, &revision.PostID, &revision.Version, &snapshot, &revision.Actor, &revision.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan post revision: %w", err)
		}
		revision.Snapshot, err = decodeContentJSON[content.Post](snapshot)
		if err != nil {
			return nil, fmt.Errorf("decode post revision: %w", err)
		}
		revisions = append(revisions, revision)
	}
	return revisions, rows.Err()
}

func (r *PostRepository) GetRevision(ctx context.Context, postID, revisionID int64) (content.Revision, error) {
	var revision content.Revision
	var snapshot []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id,post_id,post_version,snapshot,actor,created_at
		FROM np_post_revisions
		WHERE post_id=$1 AND id=$2`, postID, revisionID).Scan(
		&revision.ID, &revision.PostID, &revision.Version, &snapshot, &revision.Actor, &revision.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Revision{}, content.ErrNotFound
	}
	if err != nil {
		return content.Revision{}, fmt.Errorf("load post revision: %w", err)
	}
	revision.Snapshot, err = decodeContentJSON[content.Post](snapshot)
	if err != nil {
		return content.Revision{}, fmt.Errorf("decode post revision: %w", err)
	}
	return revision, nil
}

func (r *PostRepository) RestoreRevision(ctx context.Context, postID, revisionID int64, mutation content.Mutation) (content.Post, error) {
	if err := content.ValidateRevisionActor(mutation.Actor); err != nil {
		return content.Post{}, err
	}
	now := time.Now().UTC()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.Post{}, fmt.Errorf("begin restore post revision: %w", err)
	}
	defer tx.Rollback(ctx)
	post, err := restorePostRevisionInTransaction(ctx, tx, postID, revisionID, mutation, now)
	if err != nil {
		return content.Post{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return content.Post{}, fmt.Errorf("commit post revision restore: %w", err)
	}
	return post, nil
}

func restorePostRevisionInTransaction(ctx context.Context, tx pgx.Tx, postID, revisionID int64, mutation content.Mutation, now time.Time) (content.Post, error) {
	if err := content.ValidateRevisionActor(mutation.Actor); err != nil {
		return content.Post{}, err
	}
	current, err := queryPostByID(ctx, tx, postID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Post{}, content.ErrNotFound
	}
	if err != nil {
		return content.Post{}, fmt.Errorf("lock post for revision restore: %w", err)
	}
	if mutation.ExpectedVersion <= 0 || current.Version != mutation.ExpectedVersion {
		return content.Post{}, content.ErrVersionConflict
	}
	var snapshotBytes []byte
	err = tx.QueryRow(ctx, `SELECT snapshot FROM np_post_revisions WHERE id=$1 AND post_id=$2`, revisionID, postID).Scan(&snapshotBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Post{}, content.ErrNotFound
	}
	if err != nil {
		return content.Post{}, fmt.Errorf("load post revision: %w", err)
	}
	snapshot, err := decodeContentJSON[content.Post](snapshotBytes)
	if err != nil {
		return content.Post{}, fmt.Errorf("decode post revision: %w", err)
	}
	if snapshot.Type == content.TypePost && len(snapshot.Categories) == 0 {
		return content.Post{}, &content.ValidationError{Fields: map[string]string{"category_ids": "文章修订必须包含完整的分类关系"}}
	}
	if err := preserveOldContentPath(ctx, tx, current, snapshot.Type, snapshot.Slug, now); err != nil {
		return content.Post{}, err
	}
	categories, tags, err := resolvePostTerms(ctx, tx, postInputFromSnapshot(snapshot))
	if err != nil {
		return content.Post{}, fmt.Errorf("resolve restored post terms: %w", err)
	}
	snapshot.Category = primaryCategoryName(categories)
	// Metadata is part of the editorial snapshot, including explicit removals.
	// Workflow state and publication time remain on the current canonical row.
	metadata := cloneMetadata(snapshot.Meta)
	metadata["category"] = snapshot.Category
	metadata["author_name"] = snapshot.AuthorName
	command, err := tx.Exec(ctx, `
		UPDATE np_posts SET type=$2,slug=$3,title=$4,excerpt=$5,content_source=$6,content_html=$7,
			cover_url=$8,comment_status=$9,metadata=$10,updated_at=$11,version=version+1
		WHERE id=$1 AND version=$12`,
		postID, snapshot.Type, snapshot.Slug, snapshot.Title, snapshot.Excerpt,
		snapshot.ContentSource, snapshot.ContentHTML, snapshot.CoverURL, snapshot.CommentStatus,
		metadata, now, mutation.ExpectedVersion,
	)
	if err != nil {
		return content.Post{}, mapPostWriteError("restore post revision", err)
	}
	if command.RowsAffected() != 1 {
		return content.Post{}, content.ErrVersionConflict
	}
	if err := replacePostTerms(ctx, tx, postID, categories, tags); err != nil {
		return content.Post{}, err
	}
	post, err := queryPostByID(ctx, tx, postID, false)
	if err != nil {
		return content.Post{}, fmt.Errorf("load restored post: %w", err)
	}
	posts := []content.Post{post}
	if err := hydratePostTerms(ctx, tx, posts); err != nil {
		return content.Post{}, fmt.Errorf("load restored post terms: %w", err)
	}
	post = posts[0]
	if err := insertRevision(ctx, tx, post, mutation.Actor); err != nil {
		return content.Post{}, err
	}
	if err := deletePostAutosaves(ctx, tx, postID); err != nil {
		return content.Post{}, err
	}
	return post, nil
}
