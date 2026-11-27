package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"newpress.local/newpress/internal/content"
)

type scanner interface {
	Scan(...any) error
}

const postColumns = `
	p.id, CASE WHEN u.status='system' THEN 0 ELSE COALESCE(p.author_id,0) END, p.type, p.status, p.slug, p.title, p.excerpt,
	p.content_source, p.content_html, p.cover_url,
	COALESCE(CASE WHEN u.status<>'system' THEN u.display_name END, NULLIF(p.metadata->>'author_name',''), 'NewPress'),
	COALESCE(p.metadata->>'category','未分类'), p.comment_status, p.published_at,
	p.created_at, p.updated_at, p.version, p.metadata`

func scanPost(row scanner) (content.Post, error) {
	var post content.Post
	var publishedAt *time.Time
	var metadata []byte
	if err := row.Scan(
		&post.ID, &post.AuthorID, &post.Type, &post.Status, &post.Slug, &post.Title,
		&post.Excerpt, &post.ContentSource, &post.ContentHTML, &post.CoverURL,
		&post.AuthorName, &post.Category, &post.CommentStatus, &publishedAt,
		&post.CreatedAt, &post.UpdatedAt, &post.Version, &metadata,
	); err != nil {
		return content.Post{}, err
	}
	var err error
	post.Meta, err = decodeContentJSON[map[string]any](metadata)
	if err != nil {
		return content.Post{}, fmt.Errorf("decode post metadata: %w", err)
	}
	if publishedAt != nil {
		post.PublishedAt = *publishedAt
	}
	return post, nil
}

type postQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func queryPostByID(ctx context.Context, querier postQuerier, id int64, forUpdate bool) (content.Post, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE OF p"
	}
	return scanPost(querier.QueryRow(ctx, `SELECT `+postColumns+` FROM np_posts p LEFT JOIN np_users u ON u.id=p.author_id WHERE p.id=$1`+suffix, id))
}

func adminWhere(query content.AdminQuery, includeStatus bool) (string, []any) {
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 4)
	add := func(condition string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(condition, len(args)))
	}
	if query.Type != "" {
		add("p.type=$%d", query.Type)
	}
	if query.AuthorID > 0 {
		add("p.author_id=$%d", query.AuthorID)
	}
	if query.Search != "" {
		args = append(args, query.Search)
		conditions = append(conditions, fmt.Sprintf("(p.title ILIKE '%%' || $%[1]d || '%%' OR p.slug ILIKE '%%' || $%[1]d || '%%' OR p.excerpt ILIKE '%%' || $%[1]d || '%%' OR COALESCE(p.metadata->>'author_name','') ILIKE '%%' || $%[1]d || '%%' OR EXISTS (SELECT 1 FROM np_post_terms relation JOIN np_terms term ON term.id=relation.term_id WHERE relation.post_id=p.id AND term.name ILIKE '%%' || $%[1]d || '%%'))", len(args)))
	}
	if !query.PublishedAfter.IsZero() {
		add("p.published_at>$%d", query.PublishedAfter)
	}
	if !query.PublishedBefore.IsZero() {
		add("p.published_at<=$%d", query.PublishedBefore)
	}
	if includeStatus && query.Status != "" {
		add("p.status=$%d", query.Status)
	}
	if len(conditions) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

func insertRevision(ctx context.Context, tx pgx.Tx, post content.Post, actor string) error {
	if err := updateSnippetProjection(ctx, tx, post.ID, post.ContentSource); err != nil {
		return fmt.Errorf("update snippet reference projection: %w", err)
	}
	if err := updatePublicSearchBody(ctx, tx, post.ID, post.ContentHTML); err != nil {
		return fmt.Errorf("update public search projection: %w", err)
	}
	var previous content.Post
	var previousJSON []byte
	if err := tx.QueryRow(ctx, `SELECT snapshot FROM np_post_revisions WHERE post_id=$1 ORDER BY post_version DESC LIMIT 1`, post.ID).Scan(&previousJSON); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if len(previousJSON) > 0 {
		decoded, err := decodeContentJSON[content.Post](previousJSON)
		if err != nil {
			return err
		}
		previous = decoded
	}
	snapshot, err := json.Marshal(post)
	if err != nil {
		return fmt.Errorf("encode post revision: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO np_post_revisions(post_id,post_version,snapshot,actor,created_at) VALUES ($1,$2,$3,$4,$5)`,
		post.ID, post.Version, snapshot, strings.TrimSpace(actor), post.UpdatedAt); err != nil {
		return fmt.Errorf("record post revision: %w", err)
	}
	return insertActivity(ctx, tx, "notification.post", content.ActivityEvents(previous, post))
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func nullableAuthorID(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func cloneMetadata(source map[string]any) map[string]any {
	result := make(map[string]any, len(source)+2)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func mapPostWriteError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" && strings.Contains(postgresError.ConstraintName, "type_slug") {
		return content.ErrSlugConflict
	}
	return fmt.Errorf("%s: %w", operation, err)
}
