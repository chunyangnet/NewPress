package database

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/activity"
	"time"
)

type ActivitySource struct {
	pool  *pgxpool.Pool
	topic string
}

func NewActivitySource(pool *pgxpool.Pool, topic string) *ActivitySource {
	return &ActivitySource{pool, topic}
}

func insertActivity(ctx context.Context, tx pgx.Tx, topic string, events []activity.Event) error {
	for _, e := range events {
		if err := e.Validate(); err != nil {
			return err
		}
		payload, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO np_outbox(topic,payload,available_at,event_key) VALUES($1,$2,$3,$4) ON CONFLICT(event_key) DO NOTHING`, topic, payload, e.Due, e.Key); err != nil {
			return err
		}
	}
	return nil
}
func (s *ActivitySource) PendingActivity(ctx context.Context, now time.Time, limit int) ([]activity.Event, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT payload FROM np_outbox WHERE topic=$1 AND processed_at IS NULL AND available_at <= $2 ORDER BY id LIMIT $3`, s.topic, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []activity.Event{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var e activity.Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		if err := e.Validate(); err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	return items, rows.Err()
}
func (s *ActivitySource) AckActivity(ctx context.Context, key string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM np_outbox WHERE topic=$1 AND event_key=$2`, s.topic, key)
	return err
}
