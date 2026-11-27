package database

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/resources"
)

type ResourceBackend struct{ pool *pgxpool.Pool }

func NewResourceBackend(pool *pgxpool.Pool) *ResourceBackend { return &ResourceBackend{pool: pool} }
func (b *ResourceBackend) Read(ctx context.Context) (resources.State, error) {
	var raw []byte
	e := b.pool.QueryRow(ctx, `SELECT data FROM np_resource_state WHERE id=1`).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return resources.InitialState(), nil
	}
	if e != nil {
		return resources.State{}, e
	}
	var state resources.State
	if e = json.Unmarshal(raw, &state); e == nil {
		e = resources.ValidateState(state)
	}
	return state, e
}
func (b *ResourceBackend) Update(ctx context.Context, fn func(*resources.State) error) error {
	tx, e := b.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	initial, _ := json.Marshal(resources.InitialState())
	if _, e = tx.Exec(ctx, `INSERT INTO np_resource_state(id,data) VALUES(1,$1) ON CONFLICT(id) DO NOTHING`, initial); e != nil {
		return e
	}
	var raw []byte
	if e = tx.QueryRow(ctx, `SELECT data FROM np_resource_state WHERE id=1 FOR UPDATE`).Scan(&raw); e != nil {
		return e
	}
	var state resources.State
	if e = json.Unmarshal(raw, &state); e != nil {
		return e
	}
	if e = resources.ValidateState(state); e != nil {
		return e
	}
	if e = fn(&state); e != nil {
		return e
	}
	if e = resources.ValidateState(state); e != nil {
		return e
	}
	raw, e = json.Marshal(state)
	if e != nil {
		return e
	}
	if len(raw) > resources.MaxStateBytes {
		return resources.ErrUnavailable
	}
	if _, e = tx.Exec(ctx, `UPDATE np_resource_state SET data=$1 WHERE id=1`, raw); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
