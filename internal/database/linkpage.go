package database

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/linkpage"
	"time"
)

type LinkPageRepository struct{ pool *pgxpool.Pool }

func NewLinkPageRepository(pool *pgxpool.Pool) *LinkPageRepository {
	return &LinkPageRepository{pool}
}
func (r *LinkPageRepository) Ready(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `SELECT slug,version FROM np_link_pages LIMIT 0`)
	return err
}
func (r *LinkPageRepository) List(ctx context.Context) ([]linkpage.Board, error) {
	rows, err := r.pool.Query(ctx, `SELECT data - 'items' - 'receipts' FROM np_link_pages ORDER BY slug LIMIT 65`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []linkpage.Board{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		var c linkpage.Board
		if err = json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		if err = linkpage.Validate(c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) > linkpage.MaxBoards {
		return nil, linkpage.ErrCapacity
	}
	return out, rows.Err()
}
func (r *LinkPageRepository) Get(ctx context.Context, slug string) (linkpage.Board, error) {
	var b []byte
	err := r.pool.QueryRow(ctx, `SELECT data FROM np_link_pages WHERE slug=$1`, slug).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return linkpage.Board{}, linkpage.ErrNotFound
	}
	if err != nil {
		return linkpage.Board{}, err
	}
	return linkpage.DecodeBoard(b)
}
func (r *LinkPageRepository) Apply(ctx context.Context, slug string, a linkpage.Actor, cmd linkpage.Command, now time.Time) (linkpage.Result, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return linkpage.Result{}, err
	}
	defer tx.Rollback(context.Background())
	// Serialize only registry creation/capacity, ordinary writes lock one board.
	if cmd.Action == "create" {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1760034110)`); err != nil {
			return linkpage.Result{}, err
		}
	}
	var b []byte
	var current *linkpage.Board
	err = tx.QueryRow(ctx, `SELECT data FROM np_link_pages WHERE slug=$1 FOR UPDATE`, slug).Scan(&b)
	if err == nil {
		c, e := linkpage.DecodeBoard(b)
		if e != nil {
			return linkpage.Result{}, e
		}
		current = &c
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return linkpage.Result{}, err
	}
	if current == nil && cmd.Action == "create" {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM np_link_pages`).Scan(&count); err != nil {
			return linkpage.Result{}, err
		}
		if count >= linkpage.MaxBoards {
			return linkpage.Result{}, linkpage.ErrCapacity
		}
	}
	next, result, err := linkpage.Mutate(current, slug, a, cmd, now)
	if err != nil {
		return linkpage.Result{}, err
	}
	if result.Replay {
		return result, nil
	}
	b, err = json.Marshal(next)
	if err != nil {
		return linkpage.Result{}, err
	}
	if current == nil {
		_, err = tx.Exec(ctx, `INSERT INTO np_link_pages(slug,version,data,updated_at) VALUES($1,$2,$3,$4)`, slug, next.Version, b, next.UpdatedAt)
	} else {
		_, err = tx.Exec(ctx, `UPDATE np_link_pages SET version=$2,data=$3,updated_at=$4 WHERE slug=$1`, slug, next.Version, b, next.UpdatedAt)
	}
	if err != nil {
		return linkpage.Result{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return linkpage.Result{}, err
	}
	return result, nil
}
