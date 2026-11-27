package database

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/recognition"
)

type RecognitionBackend struct{ pool *pgxpool.Pool }

func NewRecognitionBackend(pool *pgxpool.Pool) *RecognitionBackend {
	return &RecognitionBackend{pool: pool}
}
func (b *RecognitionBackend) Read(ctx context.Context) (recognition.State, error) {
	var raw []byte
	e := b.pool.QueryRow(ctx, `SELECT data FROM np_recognition_state WHERE id=1`).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return recognition.InitialState(), nil
	}
	if e != nil {
		return recognition.State{}, e
	}
	var state recognition.State
	if e = json.Unmarshal(raw, &state); e == nil {
		e = recognition.ValidateState(state)
	}
	return state, e
}
func (b *RecognitionBackend) Update(ctx context.Context, fn func(*recognition.State) error) error {
	tx, e := b.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	initial, _ := json.Marshal(recognition.InitialState())
	if _, e = tx.Exec(ctx, `INSERT INTO np_recognition_state(id,data) VALUES(1,$1) ON CONFLICT(id) DO NOTHING`, initial); e != nil {
		return e
	}
	var raw []byte
	if e = tx.QueryRow(ctx, `SELECT data FROM np_recognition_state WHERE id=1 FOR UPDATE`).Scan(&raw); e != nil {
		return e
	}
	var state recognition.State
	if e = json.Unmarshal(raw, &state); e != nil {
		return e
	}
	if e = recognition.ValidateState(state); e != nil {
		return e
	}
	if e = fn(&state); e != nil {
		return e
	}
	if e = recognition.ValidateState(state); e != nil {
		return e
	}
	raw, e = json.Marshal(state)
	if e != nil {
		return e
	}
	if len(raw) > recognition.MaxStateBytes {
		return recognition.ErrUnavailable
	}
	if _, e = tx.Exec(ctx, `UPDATE np_recognition_state SET data=$1 WHERE id=1`, raw); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
