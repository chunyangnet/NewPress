package database

import (
	"context"
	"newpress.local/newpress/internal/engagement"
)

func (s *EngagementRepository) Followers(ctx context.Context, author, after int64, limit int) ([]engagement.Entry, error) {
	if author <= 0 || after < 0 {
		return nil, engagement.ErrInvalid
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT user_id,target_id,kind,collection,created_at FROM np_engagement WHERE kind='follow' AND target_id=$1 AND user_id>$2 ORDER BY user_id LIMIT $3`, author, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []engagement.Entry{}
	for rows.Next() {
		var e engagement.Entry
		if err := rows.Scan(&e.UserID, &e.TargetID, &e.Kind, &e.Collection, &e.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	return items, rows.Err()
}
