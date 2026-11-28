package database

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"newpress.local/newpress/internal/content"
)

func (r *PostRepository) GetPostHeaders(ctx context.Context, ids []int64) (map[int64]content.PostHeader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := make(map[int64]content.PostHeader, len(ids))
	if len(ids) == 0 {
		return values, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT p.id, CASE WHEN u.status='system' THEN 0 ELSE COALESCE(p.author_id,0) END,
		p.version,p.type,p.status,p.title,p.slug,p.published_at,p.updated_at
		FROM np_posts p LEFT JOIN np_users u ON u.id=p.author_id WHERE p.id=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h content.PostHeader
		var published pgtype.Timestamptz
		if err = rows.Scan(&h.ID, &h.AuthorID, &h.Version, &h.Type, &h.Status, &h.Title, &h.Slug, &published, &h.UpdatedAt); err != nil {
			return nil, err
		}
		if published.Valid {
			h.PublishedAt = published.Time
		}
		values[h.ID] = h
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return values, nil
}
