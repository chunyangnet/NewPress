package database

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/shop"
)

type ShopBackend struct{ pool *pgxpool.Pool }

func NewShopBackend(pool *pgxpool.Pool) *ShopBackend { return &ShopBackend{pool: pool} }
func (b *ShopBackend) Read(ctx context.Context) (shop.State, error) {
	var raw []byte
	err := b.pool.QueryRow(ctx, `SELECT data FROM np_shop_state WHERE id=1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return shop.InitialState(), nil
	}
	if err != nil {
		return shop.State{}, err
	}
	var state shop.State
	if err = json.Unmarshal(raw, &state); err == nil {
		shop.NormalizeState(&state)
		err = shop.ValidateState(state)
	}
	return state, err
}
func (b *ShopBackend) Update(ctx context.Context, fn func(*shop.State) error) error {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	initial, _ := json.Marshal(shop.InitialState())
	if _, err = tx.Exec(ctx, `INSERT INTO np_shop_state(id,data) VALUES(1,$1) ON CONFLICT(id) DO NOTHING`, initial); err != nil {
		return err
	}
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT data FROM np_shop_state WHERE id=1 FOR UPDATE`).Scan(&raw); err != nil {
		return err
	}
	var state shop.State
	if err = json.Unmarshal(raw, &state); err != nil {
		return err
	}
	shop.NormalizeState(&state)
	if err = shop.ValidateState(state); err != nil {
		return err
	}
	if err = fn(&state); err != nil {
		return err
	}
	if err = shop.ValidateState(state); err != nil {
		return err
	}
	raw, err = json.Marshal(state)
	if err != nil {
		return err
	}
	if len(raw) > shop.MaxStateBytes {
		return shop.ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `UPDATE np_shop_state SET data=$1 WHERE id=1`, raw); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
