package database

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/commerce"
)

type CommerceBackend struct{ pool *pgxpool.Pool }

func NewCommerceBackend(pool *pgxpool.Pool) *CommerceBackend { return &CommerceBackend{pool: pool} }
func (b *CommerceBackend) Read(ctx context.Context) (commerce.State, error) {
	var raw []byte
	e := b.pool.QueryRow(ctx, `SELECT data FROM np_commerce_state WHERE id=1`).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return commerce.InitialState(), nil
	}
	if e != nil {
		return commerce.State{}, e
	}
	var state commerce.State
	if e = json.Unmarshal(raw, &state); e == nil {
		e = commerce.ValidateState(state)
	}
	return state, e
}
func (b *CommerceBackend) Update(ctx context.Context, fn func(*commerce.State) error) error {
	tx, e := b.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	initial, _ := json.Marshal(commerce.InitialState())
	if _, e = tx.Exec(ctx, `INSERT INTO np_commerce_state(id,data) VALUES(1,$1) ON CONFLICT(id) DO NOTHING`, initial); e != nil {
		return e
	}
	var raw []byte
	if e = tx.QueryRow(ctx, `SELECT data FROM np_commerce_state WHERE id=1 FOR UPDATE`).Scan(&raw); e != nil {
		return e
	}
	var state commerce.State
	if e = json.Unmarshal(raw, &state); e != nil {
		return e
	}
	if e = commerce.ValidateState(state); e != nil {
		return e
	}
	if e = fn(&state); e != nil {
		return e
	}
	if e = commerce.ValidateState(state); e != nil {
		return e
	}
	raw, e = json.Marshal(state)
	if e != nil {
		return e
	}
	if len(raw) > commerce.MaxStateBytes {
		return commerce.ErrUnavailable
	}
	if _, e = tx.Exec(ctx, `UPDATE np_commerce_state SET data=$1 WHERE id=1`, raw); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
