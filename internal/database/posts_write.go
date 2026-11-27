package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"newpress.local/newpress/internal/content"
)

func (r *PostRepository) Create(ctx context.Context, input content.PostInput) (content.Post, error) {
	now := time.Now().UTC()
	normalized, fields := content.NormalizePostInput(input, now)
	if len(fields) > 0 {
		return content.Post{}, &content.ValidationError{Fields: fields}
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.Post{}, fmt.Errorf("begin create post: %w", err)
	}
	defer tx.Rollback(ctx)
	post, err := createImportedOrRegularPost(ctx, tx, normalized, now)
	if err != nil {
		return content.Post{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return content.Post{}, fmt.Errorf("commit create post: %w", err)
	}
	return post, nil
}

func createImportedOrRegularPost(ctx context.Context, tx pgx.Tx, normalized content.PostInput, now time.Time) (content.Post, error) {
	categories, tags, err := resolvePostTerms(ctx, tx, normalized)
	if err != nil {
		return content.Post{}, err
	}
	normalized.Category = primaryCategoryName(categories)
	metadata := map[string]any{"category": normalized.Category, "author_name": normalized.AuthorName}
	content.ApplyAIMetadata(metadata, normalized)
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO np_posts(
			author_id,type,status,slug,title,excerpt,content_source,content_html,cover_url,
			comment_status,metadata,published_at,created_at,updated_at,version
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13,1)
		RETURNING id`,
		nullableAuthorID(normalized.AuthorID), normalized.Type, normalized.Status, normalized.Slug, normalized.Title, normalized.Excerpt,
		normalized.ContentSource, normalized.ContentHTML, normalized.CoverURL, normalized.CommentStatus,
		metadata, nullableTime(normalized.PublishedAt), now,
	).Scan(&id)
	if err != nil {
		return content.Post{}, mapPostWriteError("create post", err)
	}
	if err := replacePostTerms(ctx, tx, id, categories, tags); err != nil {
		return content.Post{}, err
	}
	post, err := queryPostByID(ctx, tx, id, false)
	if err != nil {
		return content.Post{}, fmt.Errorf("load created post: %w", err)
	}
	posts := []content.Post{post}
	if err := hydratePostTerms(ctx, tx, posts); err != nil {
		return content.Post{}, fmt.Errorf("load created post terms: %w", err)
	}
	post = posts[0]
	if err := insertRevision(ctx, tx, post, normalized.Actor); err != nil {
		return content.Post{}, err
	}
	return post, nil
}

func (r *PostRepository) Update(ctx context.Context, id int64, mutation content.Mutation, input content.PostInput) (content.Post, error) {
	if err := content.ValidateRevisionActor(mutation.Actor); err != nil {
		return content.Post{}, err
	}
	now := time.Now().UTC()
	normalized, fields := content.NormalizePostInput(input, now)
	if len(fields) > 0 {
		return content.Post{}, &content.ValidationError{Fields: fields}
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.Post{}, fmt.Errorf("begin update post: %w", err)
	}
	defer tx.Rollback(ctx)
	post, err := updatePostInTransaction(ctx, tx, id, mutation, normalized, now)
	if err != nil {
		return content.Post{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return content.Post{}, fmt.Errorf("commit update post: %w", err)
	}
	return post, nil
}

func updatePostInTransaction(ctx context.Context, tx pgx.Tx, id int64, mutation content.Mutation, normalized content.PostInput, now time.Time) (content.Post, error) {
	if err := content.ValidateRevisionActor(mutation.Actor); err != nil {
		return content.Post{}, err
	}
	current, err := queryPostByID(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Post{}, content.ErrNotFound
	}
	if err != nil {
		return content.Post{}, fmt.Errorf("lock post: %w", err)
	}
	if mutation.ExpectedVersion <= 0 || current.Version != mutation.ExpectedVersion {
		return content.Post{}, content.ErrVersionConflict
	}
	if err := preserveOldContentPath(ctx, tx, current, normalized.Type, normalized.Slug, now); err != nil {
		return content.Post{}, err
	}
	categories, tags, err := resolvePostTerms(ctx, tx, normalized)
	if err != nil {
		return content.Post{}, err
	}
	normalized.Category = primaryCategoryName(categories)
	metadata := cloneMetadata(current.Meta)
	metadata["category"] = normalized.Category
	metadata["author_name"] = normalized.AuthorName
	content.ApplyAIMetadata(metadata, normalized)
	command, err := tx.Exec(ctx, `
		UPDATE np_posts SET
			type=$2,status=$3,slug=$4,title=$5,excerpt=$6,content_source=$7,content_html=$8,
			cover_url=$9,comment_status=$10,metadata=$11,published_at=$12,updated_at=$13,version=version+1
		WHERE id=$1 AND version=$14`,
		id, normalized.Type, normalized.Status, normalized.Slug, normalized.Title, normalized.Excerpt,
		normalized.ContentSource, normalized.ContentHTML, normalized.CoverURL, normalized.CommentStatus,
		metadata, nullableTime(normalized.PublishedAt), now, mutation.ExpectedVersion,
	)
	if err != nil {
		return content.Post{}, mapPostWriteError("update post", err)
	}
	if command.RowsAffected() != 1 {
		return content.Post{}, content.ErrVersionConflict
	}
	if err := replacePostTerms(ctx, tx, id, categories, tags); err != nil {
		return content.Post{}, err
	}
	post, err := queryPostByID(ctx, tx, id, false)
	if err != nil {
		return content.Post{}, fmt.Errorf("load updated post: %w", err)
	}
	posts := []content.Post{post}
	if err := hydratePostTerms(ctx, tx, posts); err != nil {
		return content.Post{}, fmt.Errorf("load updated post terms: %w", err)
	}
	post = posts[0]
	actor := strings.TrimSpace(mutation.Actor)
	if actor == "" {
		actor = normalized.Actor
	}
	if err := insertRevision(ctx, tx, post, actor); err != nil {
		return content.Post{}, err
	}
	if err := deletePostAutosaves(ctx, tx, id); err != nil {
		return content.Post{}, err
	}
	return post, nil
}

func (r *PostRepository) ChangeStatus(ctx context.Context, id int64, mutation content.Mutation, status string) (content.Post, error) {
	if err := content.ValidateRevisionActor(mutation.Actor); err != nil {
		return content.Post{}, err
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if !content.ValidStatus(status) {
		return content.Post{}, &content.ValidationError{Fields: map[string]string{"status": "内容状态不受支持"}}
	}
	now := time.Now().UTC()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.Post{}, fmt.Errorf("begin change post status: %w", err)
	}
	defer tx.Rollback(ctx)
	post, err := changePostStatusInTransaction(ctx, tx, id, mutation, status, now)
	if err != nil {
		return content.Post{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return content.Post{}, fmt.Errorf("commit post status: %w", err)
	}
	return post, nil
}

func changePostStatusInTransaction(ctx context.Context, tx pgx.Tx, id int64, mutation content.Mutation, status string, now time.Time) (content.Post, error) {
	if err := content.ValidateRevisionActor(mutation.Actor); err != nil {
		return content.Post{}, err
	}
	current, err := queryPostByID(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Post{}, content.ErrNotFound
	}
	if err != nil {
		return content.Post{}, fmt.Errorf("lock post: %w", err)
	}
	if mutation.ExpectedVersion <= 0 || current.Version != mutation.ExpectedVersion {
		return content.Post{}, content.ErrVersionConflict
	}
	publishedAt := current.PublishedAt
	if status == content.StatusPublish && (publishedAt.IsZero() || publishedAt.After(now)) {
		publishedAt = now
	}
	if status == content.StatusFuture && (publishedAt.IsZero() || !publishedAt.After(now)) {
		return content.Post{}, &content.ValidationError{Fields: map[string]string{"published_at": "定时发布必须选择未来时间"}}
	}
	command, err := tx.Exec(ctx, `UPDATE np_posts SET status=$2,published_at=$3,updated_at=$4,version=version+1 WHERE id=$1 AND version=$5`,
		id, status, nullableTime(publishedAt), now, mutation.ExpectedVersion)
	if err != nil {
		return content.Post{}, mapPostWriteError("change post status", err)
	}
	if command.RowsAffected() != 1 {
		return content.Post{}, content.ErrVersionConflict
	}
	post, err := queryPostByID(ctx, tx, id, false)
	if err != nil {
		return content.Post{}, fmt.Errorf("load status-changed post: %w", err)
	}
	posts := []content.Post{post}
	if err := hydratePostTerms(ctx, tx, posts); err != nil {
		return content.Post{}, fmt.Errorf("load status-changed post terms: %w", err)
	}
	post = posts[0]
	if err := insertRevision(ctx, tx, post, mutation.Actor); err != nil {
		return content.Post{}, err
	}
	if err := deletePostAutosaves(ctx, tx, id); err != nil {
		return content.Post{}, err
	}
	return post, nil
}

func (r *PostRepository) BulkChangeStatus(ctx context.Context, change content.BulkStatusChange) ([]content.Post, error) {
	normalized, fields := content.NormalizeBulkStatusChange(change)
	if len(fields) > 0 {
		return nil, &content.ValidationError{Fields: fields}
	}
	now := time.Now().UTC()
	ids := make([]int64, len(normalized.Items))
	expectedVersions := make(map[int64]int64, len(normalized.Items))
	for index, item := range normalized.Items {
		ids[index] = item.ID
		expectedVersions[item.ID] = item.ExpectedVersion
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin bulk post status change: %w", err)
	}
	defer tx.Rollback(ctx)

	// NormalizeBulkStatusChange sorts IDs, so concurrent bulk requests acquire
	// row locks in one deterministic order instead of creating avoidable
	// deadlocks. Nothing is updated until the complete selection is present and
	// every optimistic version has been validated.
	rows, err := tx.Query(ctx, `SELECT `+postColumns+`
		FROM np_posts p
		LEFT JOIN np_users u ON u.id=p.author_id
		WHERE p.id=ANY($1::bigint[])
		ORDER BY p.id
		FOR UPDATE OF p`, ids)
	if err != nil {
		return nil, fmt.Errorf("lock bulk posts: %w", err)
	}
	locked := make([]content.Post, 0, len(ids))
	for rows.Next() {
		post, scanErr := scanPost(rows)
		if scanErr != nil {
			rows.Close()
			return nil, fmt.Errorf("scan locked bulk post: %w", scanErr)
		}
		locked = append(locked, post)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate locked bulk posts: %w", err)
	}
	rows.Close()
	if len(locked) != len(ids) {
		return nil, content.ErrNotFound
	}
	for _, post := range locked {
		if normalized.AuthorID > 0 && post.AuthorID != normalized.AuthorID {
			return nil, content.ErrAuthorScope
		}
		if post.Version != expectedVersions[post.ID] {
			return nil, content.ErrVersionConflict
		}
		if normalized.Delete && post.Status != content.StatusTrash {
			return nil, &content.ValidationError{Fields: map[string]string{"selection": "请先将全部所选内容移入回收站，再永久删除"}}
		}
		if normalized.Status == content.StatusFuture && (post.PublishedAt.IsZero() || !post.PublishedAt.After(now)) {
			return nil, &content.ValidationError{Fields: map[string]string{"published_at": "定时发布必须选择未来时间"}}
		}
	}

	if normalized.Delete {
		if _, err := tx.Exec(ctx, `UPDATE np_series SET version=version+1, updated_at=$2 WHERE id IN (SELECT series_id FROM np_series_posts WHERE post_id=ANY($1::bigint[]))`, ids, now); err != nil {
			return nil, fmt.Errorf("update deleted post series: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM np_series_posts WHERE post_id=ANY($1::bigint[])`, ids); err != nil {
			return nil, fmt.Errorf("remove deleted post series membership: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM np_posts WHERE id=ANY($1::bigint[])`, ids); err != nil {
			return nil, fmt.Errorf("permanently delete posts: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit permanent post deletion: %w", err)
		}
		return locked, nil
	}

	command, err := tx.Exec(ctx, `UPDATE np_posts
		SET status=$2,
			published_at=CASE WHEN $2='publish' AND (published_at IS NULL OR published_at>$3) THEN $3 ELSE published_at END,
			updated_at=$3,
			version=version+1
		WHERE id=ANY($1::bigint[])`, ids, normalized.Status, now)
	if err != nil {
		return nil, mapPostWriteError("bulk change post status", err)
	}
	if command.RowsAffected() != int64(len(ids)) {
		return nil, content.ErrVersionConflict
	}

	rows, err = tx.Query(ctx, `SELECT `+postColumns+`
		FROM np_posts p
		LEFT JOIN np_users u ON u.id=p.author_id
		WHERE p.id=ANY($1::bigint[])
		ORDER BY p.id`, ids)
	if err != nil {
		return nil, fmt.Errorf("load bulk status-changed posts: %w", err)
	}
	posts := make([]content.Post, 0, len(ids))
	for rows.Next() {
		post, scanErr := scanPost(rows)
		if scanErr != nil {
			rows.Close()
			return nil, fmt.Errorf("scan bulk status-changed post: %w", scanErr)
		}
		posts = append(posts, post)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate bulk status-changed posts: %w", err)
	}
	rows.Close()
	if len(posts) != len(ids) {
		return nil, fmt.Errorf("load bulk status-changed posts: expected %d rows, got %d", len(ids), len(posts))
	}
	if err := hydratePostTerms(ctx, tx, posts); err != nil {
		return nil, fmt.Errorf("load bulk status-changed post terms: %w", err)
	}
	for _, post := range posts {
		if err := insertRevision(ctx, tx, post, normalized.Actor); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM np_post_autosaves WHERE post_id=ANY($1::bigint[])`, ids); err != nil {
		return nil, fmt.Errorf("clear bulk post autosaves: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit bulk post status: %w", err)
	}
	return posts, nil
}
