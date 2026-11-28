package database

import (
	"context"

	"newpress.local/newpress/internal/comment"
)

func (r *CommentRepository) CountApprovedByPost(ctx context.Context, postIDs []int64) (map[int64]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || r.pool == nil {
		return nil, comment.ErrInvalidInput
	}
	counts := make(map[int64]int, len(postIDs))
	for _, id := range postIDs {
		if id <= 0 {
			return nil, comment.ErrInvalidInput
		}
		counts[id] = 0
	}
	if len(counts) == 0 {
		return counts, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT post_id,count(*) FROM np_comments
		WHERE status='approved' AND post_id=ANY($1) GROUP BY post_id`, postIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		counts[id] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}
