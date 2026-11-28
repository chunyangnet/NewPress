package database

import (
	"context"

	"newpress.local/newpress/internal/comment"
)

func (r *CommentRepository) GetCommentStates(ctx context.Context, ids []int64) (map[int64]comment.State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := make(map[int64]comment.State, len(ids))
	if len(ids) == 0 {
		return values, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT id,post_id,COALESCE(user_id,0),version,status FROM np_comments WHERE id=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v comment.State
		if err = rows.Scan(&v.ID, &v.PostID, &v.UserID, &v.Version, &v.Status); err != nil {
			return nil, err
		}
		values[v.ID] = v
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, ok := values[id]; !ok {
			return nil, comment.ErrNotFound
		}
	}
	return values, nil
}
