package database

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/engagement"
)

type EngagementRepository struct{ pool *pgxpool.Pool }

func NewEngagementRepository(pool *pgxpool.Pool) *EngagementRepository {
	return &EngagementRepository{pool: pool}
}

func (s *EngagementRepository) Ready(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `SELECT 1 FROM np_engagement LIMIT 0`)
	return err
}

func (s *EngagementRepository) Merge(ctx context.Context, user int64, inputs []engagement.Input, preview bool) (engagement.MergeResult, error) {
	inputs, err := engagement.NormalizeMerge(user, inputs)
	if err != nil {
		return engagement.MergeResult{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return engagement.MergeResult{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('engagement-' || $1::bigint::text,0))`, user); err != nil {
		return engagement.MergeResult{}, err
	}
	rows, err := tx.Query(ctx, `SELECT kind,target_id FROM np_engagement WHERE user_id=$1`, user)
	if err != nil {
		return engagement.MergeResult{}, err
	}
	type key struct {
		kind string
		id   int64
	}
	seen, counts := map[key]bool{}, map[string]int{}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.kind, &k.id); err != nil {
			rows.Close()
			return engagement.MergeResult{}, err
		}
		seen[k] = true
		counts[k.kind]++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return engagement.MergeResult{}, err
	}
	result := engagement.MergeResult{}
	for _, input := range inputs {
		k := key{input.Kind, input.TargetID}
		if seen[k] {
			result.Existing++
			continue
		}
		counts[input.Kind]++
		if counts[input.Kind] > engagement.Capacity(input.Kind) {
			return engagement.MergeResult{}, engagement.ErrLimit
		}
		seen[k] = true
		result.Added++
		if !preview {
			if _, err := tx.Exec(ctx, `INSERT INTO np_engagement(user_id,kind,target_id,collection) VALUES($1,$2,$3,$4)`, user, input.Kind, input.TargetID, input.Collection); err != nil {
				return engagement.MergeResult{}, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return engagement.MergeResult{}, err
	}
	return result, nil
}

func (s *EngagementRepository) Set(ctx context.Context, input engagement.Input) error {
	input, err := engagement.Normalize(input)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// A per-account lock also serializes capacity checks across all instances.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('engagement-' || $1::bigint::text,0))`, input.UserID); err != nil {
		return err
	}
	if !input.Active {
		_, err = tx.Exec(ctx, `DELETE FROM np_engagement WHERE user_id=$1 AND kind=$2 AND target_id=$3`, input.UserID, input.Kind, input.TargetID)
	} else {
		var count int
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT count(*), COALESCE(bool_or(target_id=$3),false) FROM np_engagement WHERE user_id=$1 AND kind=$2`, input.UserID, input.Kind, input.TargetID).Scan(&count, &exists); err != nil {
			return err
		}
		if !exists && count >= engagement.Capacity(input.Kind) {
			return engagement.ErrLimit
		}
		_, err = tx.Exec(ctx, `INSERT INTO np_engagement(user_id,kind,target_id,collection) VALUES($1,$2,$3,$4)
		 ON CONFLICT(user_id,kind,target_id) DO UPDATE SET collection=EXCLUDED.collection
		 WHERE np_engagement.collection IS DISTINCT FROM EXCLUDED.collection`, input.UserID, input.Kind, input.TargetID, input.Collection)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *EngagementRepository) Entries(ctx context.Context, user int64, kind string) ([]engagement.Entry, error) {
	if user <= 0 || engagement.Capacity(kind) == 0 {
		return nil, engagement.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT user_id,target_id,kind,collection,created_at FROM np_engagement
	 WHERE user_id=$1 AND kind=$2 ORDER BY created_at DESC,target_id DESC LIMIT $3`, user, kind, engagement.Capacity(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []engagement.Entry{}
	for rows.Next() {
		var e engagement.Entry
		if err := rows.Scan(&e.UserID, &e.TargetID, &e.Kind, &e.Collection, &e.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *EngagementRepository) States(ctx context.Context, user int64, kind string, ids []int64) ([]engagement.State, error) {
	if !engagement.ValidTargets(user, kind, ids) {
		return nil, engagement.ErrInvalid
	}
	result := make([]engagement.State, len(ids))
	positions := make(map[int64]int, len(ids))
	for i, id := range ids {
		result[i].TargetID = id
		positions[id] = i
	}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT target_id,count(*),bool_or(user_id=$1),COALESCE(max(collection) FILTER (WHERE user_id=$1),'')
	 FROM np_engagement WHERE kind=$2 AND target_id=ANY($3::bigint[]) GROUP BY target_id`, user, kind, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state engagement.State
		if err := rows.Scan(&state.TargetID, &state.Count, &state.Active, &state.Collection); err != nil {
			return nil, err
		}
		result[positions[state.TargetID]] = state
	}
	return result, rows.Err()
}
