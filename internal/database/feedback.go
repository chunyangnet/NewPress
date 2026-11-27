package database

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/feedback"
	"time"
)

type FeedbackRepository struct{ pool *pgxpool.Pool }

func NewFeedbackRepository(pool *pgxpool.Pool) *FeedbackRepository {
	return &FeedbackRepository{pool}
}
func (r *FeedbackRepository) Ready(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `SELECT slug,version FROM np_feedback_boards LIMIT 0`)
	return err
}
func (r *FeedbackRepository) List(ctx context.Context) ([]feedback.Board, error) {
	rows, err := r.pool.Query(ctx, `SELECT data - 'items' - 'receipts' FROM np_feedback_boards ORDER BY slug LIMIT 65`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []feedback.Board{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		var c feedback.Board
		if err = json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		if err = feedback.Validate(c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) > feedback.MaxBoards {
		return nil, feedback.ErrCapacity
	}
	return out, rows.Err()
}
func (r *FeedbackRepository) Get(ctx context.Context, slug string) (feedback.Board, error) {
	var b []byte
	err := r.pool.QueryRow(ctx, `SELECT data FROM np_feedback_boards WHERE slug=$1`, slug).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return feedback.Board{}, feedback.ErrNotFound
	}
	if err != nil {
		return feedback.Board{}, err
	}
	return feedback.DecodeBoard(b)
}
func (r *FeedbackRepository) Apply(ctx context.Context, slug string, a feedback.Actor, cmd feedback.Command, now time.Time) (feedback.Result, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return feedback.Result{}, err
	}
	defer tx.Rollback(context.Background())
	// Serialize only registry creation/capacity, ordinary writes lock one board.
	if cmd.Action == "create" {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1760034108)`); err != nil {
			return feedback.Result{}, err
		}
	}
	var b []byte
	var current *feedback.Board
	err = tx.QueryRow(ctx, `SELECT data FROM np_feedback_boards WHERE slug=$1 FOR UPDATE`, slug).Scan(&b)
	if err == nil {
		c, e := feedback.DecodeBoard(b)
		if e != nil {
			return feedback.Result{}, e
		}
		current = &c
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return feedback.Result{}, err
	}
	if current == nil && cmd.Action == "create" {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM np_feedback_boards`).Scan(&count); err != nil {
			return feedback.Result{}, err
		}
		if count >= feedback.MaxBoards {
			return feedback.Result{}, feedback.ErrCapacity
		}
	}
	next, result, err := feedback.Mutate(current, slug, a, cmd, now)
	if err != nil {
		return feedback.Result{}, err
	}
	if result.Replay {
		return result, nil
	}
	b, err = json.Marshal(next)
	if err != nil {
		return feedback.Result{}, err
	}
	if current == nil {
		_, err = tx.Exec(ctx, `INSERT INTO np_feedback_boards(slug,version,data,updated_at) VALUES($1,$2,$3,$4)`, slug, next.Version, b, next.UpdatedAt)
	} else {
		_, err = tx.Exec(ctx, `UPDATE np_feedback_boards SET version=$2,data=$3,updated_at=$4 WHERE slug=$1`, slug, next.Version, b, next.UpdatedAt)
	}
	if err != nil {
		return feedback.Result{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return feedback.Result{}, err
	}
	return result, nil
}
