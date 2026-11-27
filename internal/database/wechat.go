package database

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/wechat"
)

type WeChatRepository struct{ pool *pgxpool.Pool }

func NewWeChatRepository(pool *pgxpool.Pool) *WeChatRepository { return &WeChatRepository{pool} }
func (r *WeChatRepository) ViewConfig(ctx context.Context) (wechat.Config, error) {
	var data []byte
	var cfg wechat.Config
	err := r.pool.QueryRow(ctx, `SELECT data->'config' FROM np_wechat_state WHERE id=1`).Scan(&data)
	if err != nil {
		return cfg, err
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func (r *WeChatRepository) View(ctx context.Context) (wechat.State, error) {
	var b []byte
	err := r.pool.QueryRow(ctx, `SELECT data FROM np_wechat_state WHERE id=1`).Scan(&b)
	if err != nil {
		return wechat.State{}, err
	}
	return wechat.Decode(b)
}
func (r *WeChatRepository) Update(ctx context.Context, fn func(*wechat.State) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var b []byte
	if err = tx.QueryRow(ctx, `SELECT data FROM np_wechat_state WHERE id=1 FOR UPDATE`).Scan(&b); err != nil {
		return err
	}
	state, err := wechat.Decode(b)
	if err != nil {
		return err
	}
	before, _ := json.Marshal(state)
	if err = fn(&state); err != nil {
		return err
	}
	if err = wechat.Validate(state); err != nil {
		return err
	}
	b, err = json.Marshal(state)
	if err != nil {
		return err
	}
	if bytes.Equal(before, b) {
		return nil
	}
	if _, err = tx.Exec(ctx, `UPDATE np_wechat_state SET data=$1 WHERE id=1`, b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
