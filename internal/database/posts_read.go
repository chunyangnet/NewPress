package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/content"
)

type PostRepository struct {
	pool *pgxpool.Pool
}

// JSON boolean equality matches content.Post.IsPinned and rejects malformed
// string metadata without casting user-controlled values.
const pinnedPostOrder = "COALESCE(p.type='post' AND p.metadata->'pinned'='true'::jsonb,false) DESC,"

func NewPostRepository(pool *pgxpool.Pool) *PostRepository {
	return &PostRepository{pool: pool}
}

func (r *PostRepository) Ready(ctx context.Context) error {
	return r.pool.Ping(ctx)
}

func (r *PostRepository) ListPublished(ctx context.Context, limit int) ([]content.Post, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `SELECT `+postColumns+`
		FROM np_posts p
		LEFT JOIN np_users u ON u.id=p.author_id
		WHERE p.type='post' AND p.status IN ('publish','future')
		  AND p.published_at IS NOT NULL AND p.published_at <= now()
		ORDER BY `+pinnedPostOrder+`p.published_at DESC,p.id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list posts: %w", err)
	}
	defer rows.Close()

	posts := make([]content.Post, 0, limit)
	for rows.Next() {
		post, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, post)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := hydratePostTerms(ctx, r.pool, posts); err != nil {
		return nil, fmt.Errorf("load post terms: %w", err)
	}
	return posts, nil
}

func (r *PostRepository) ListPublishedPage(ctx context.Context, limit, offset int) (content.PublishedPage, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.PublishedPage{}, fmt.Errorf("begin published posts page: %w", err)
	}
	defer tx.Rollback(ctx)
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM np_posts p
		WHERE p.type='post' AND p.status IN ('publish','future')
		  AND p.published_at IS NOT NULL AND p.published_at <= now()`).Scan(&total); err != nil {
		return content.PublishedPage{}, fmt.Errorf("count published posts: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT `+postColumns+`
		FROM np_posts p
		LEFT JOIN np_users u ON u.id=p.author_id
		WHERE p.type='post' AND p.status IN ('publish','future')
		  AND p.published_at IS NOT NULL AND p.published_at <= now()
		ORDER BY `+pinnedPostOrder+`p.published_at DESC,p.id DESC
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return content.PublishedPage{}, fmt.Errorf("list published posts page: %w", err)
	}
	defer rows.Close()
	posts := make([]content.Post, 0, min(limit, total))
	for rows.Next() {
		post, scanErr := scanPost(rows)
		if scanErr != nil {
			return content.PublishedPage{}, scanErr
		}
		posts = append(posts, post)
	}
	if err := rows.Err(); err != nil {
		return content.PublishedPage{}, err
	}
	if err := hydratePostTerms(ctx, tx, posts); err != nil {
		return content.PublishedPage{}, fmt.Errorf("load published post terms: %w", err)
	}
	return content.PublishedPage{Items: posts, Total: total}, tx.Commit(ctx)
}

func (r *PostRepository) GetPublishedBySlug(ctx context.Context, slug string) (content.Post, error) {
	return r.getPublishedBySlug(ctx, content.TypePost, slug)
}

func (r *PostRepository) GetPublishedPageBySlug(ctx context.Context, slug string) (content.Post, error) {
	return r.getPublishedBySlug(ctx, content.TypePage, slug)
}

func (r *PostRepository) GetPublishedFlashBySlug(ctx context.Context, slug string) (content.Post, error) {
	return r.getPublishedBySlug(ctx, content.TypeFlash, slug)
}

func (r *PostRepository) getPublishedBySlug(ctx context.Context, postType, slug string) (content.Post, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+postColumns+`
		FROM np_posts p
		LEFT JOIN np_users u ON u.id=p.author_id
		WHERE p.type=$1 AND p.slug=$2 AND p.status IN ('publish','future')
		  AND p.published_at IS NOT NULL AND p.published_at <= now()`, postType, slug)
	post, err := scanPost(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Post{}, content.ErrNotFound
	}
	if err != nil {
		return content.Post{}, err
	}
	posts := []content.Post{post}
	if err := hydratePostTerms(ctx, r.pool, posts); err != nil {
		return content.Post{}, fmt.Errorf("load post terms: %w", err)
	}
	return posts[0], nil
}

func (r *PostRepository) ListAdmin(ctx context.Context, query content.AdminQuery) (content.AdminList, error) {
	query, err := content.NormalizeAdminQuery(query)
	if err != nil {
		return content.AdminList{}, err
	}
	baseWhere, baseArgs := adminWhere(query, false)
	filteredWhere, filteredArgs := adminWhere(query, true)

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM np_posts p `+filteredWhere, filteredArgs...).Scan(&total); err != nil {
		return content.AdminList{}, fmt.Errorf("count admin posts: %w", err)
	}

	itemArgs := append([]any(nil), filteredArgs...)
	limitPlaceholder := len(itemArgs) + 1
	offsetPlaceholder := len(itemArgs) + 2
	itemArgs = append(itemArgs, query.Limit, query.Offset)
	order := "p.updated_at DESC, p.id DESC"
	if query.PublicationAscending {
		order = "p.published_at ASC, p.id ASC"
	}
	rows, err := r.pool.Query(ctx, `SELECT `+postColumns+`
		FROM np_posts p
		LEFT JOIN np_users u ON u.id=p.author_id
		`+filteredWhere+`
		ORDER BY `+order+`
		LIMIT $`+fmt.Sprint(limitPlaceholder)+` OFFSET $`+fmt.Sprint(offsetPlaceholder), itemArgs...)
	if err != nil {
		return content.AdminList{}, fmt.Errorf("list admin posts: %w", err)
	}
	items := make([]content.Post, 0, min(query.Limit, total))
	for rows.Next() {
		post, scanErr := scanPost(rows)
		if scanErr != nil {
			rows.Close()
			return content.AdminList{}, fmt.Errorf("scan admin post: %w", scanErr)
		}
		items = append(items, post)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return content.AdminList{}, fmt.Errorf("iterate admin posts: %w", err)
	}
	rows.Close()
	if err := hydratePostTerms(ctx, r.pool, items); err != nil {
		return content.AdminList{}, fmt.Errorf("load admin post terms: %w", err)
	}

	statusCounts := make(map[string]int)
	typeCounts := make(map[string]int)
	countRows, err := r.pool.Query(ctx, `SELECT p.status, p.type, count(*) FROM np_posts p `+baseWhere+` GROUP BY p.status, p.type`, baseArgs...)
	if err != nil {
		return content.AdminList{}, fmt.Errorf("count admin post states: %w", err)
	}
	for countRows.Next() {
		var status, postType string
		var count int
		if err := countRows.Scan(&status, &postType, &count); err != nil {
			countRows.Close()
			return content.AdminList{}, fmt.Errorf("scan admin post states: %w", err)
		}
		statusCounts[status] += count
		typeCounts[postType] += count
	}
	if err := countRows.Err(); err != nil {
		countRows.Close()
		return content.AdminList{}, fmt.Errorf("iterate admin post states: %w", err)
	}
	countRows.Close()

	return content.AdminList{
		Items: items, Total: total, StatusCounts: statusCounts, TypeCounts: typeCounts,
	}, nil
}

func (r *PostRepository) GetByID(ctx context.Context, id int64) (content.Post, error) {
	post, err := queryPostByID(ctx, r.pool, id, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Post{}, content.ErrNotFound
	}
	if err != nil {
		return content.Post{}, err
	}
	posts := []content.Post{post}
	if err := hydratePostTerms(ctx, r.pool, posts); err != nil {
		return content.Post{}, fmt.Errorf("load post terms: %w", err)
	}
	return posts[0], nil
}
