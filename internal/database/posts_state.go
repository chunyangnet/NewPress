package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/content"
)

func (r *PostRepository) GetPostState(ctx context.Context, id int64) (content.PostState, error) {
	var state content.PostState
	err := r.pool.QueryRow(ctx, `SELECT p.id, CASE WHEN u.status='system' THEN 0 ELSE COALESCE(p.author_id,0) END,
		p.type,p.version FROM np_posts p LEFT JOIN np_users u ON u.id=p.author_id WHERE p.id=$1`, id).
		Scan(&state.ID, &state.AuthorID, &state.Type, &state.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.PostState{}, content.ErrNotFound
	}
	if err != nil {
		return content.PostState{}, err
	}
	return state, ctx.Err()
}

func (r *PostRepository) GetPostStates(ctx context.Context, ids []int64) (map[int64]content.PostState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	states := make(map[int64]content.PostState, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, content.ErrNotFound
		}
		states[id] = content.PostState{}
	}
	if len(states) == 0 {
		return states, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT p.id, CASE WHEN u.status='system' THEN 0 ELSE COALESCE(p.author_id,0) END,
		p.type,p.version FROM np_posts p LEFT JOIN np_users u ON u.id=p.author_id WHERE p.id=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state content.PostState
		if err = rows.Scan(&state.ID, &state.AuthorID, &state.Type, &state.Version); err != nil {
			return nil, err
		}
		states[state.ID] = state
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	for _, state := range states {
		if state.ID == 0 {
			return nil, content.ErrNotFound
		}
	}
	return states, nil
}
