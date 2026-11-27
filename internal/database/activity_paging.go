package database

import (
	"context"
	"encoding/json"
	"newpress.local/newpress/internal/activity"
	"time"
)

func (s *ActivitySource) PendingActivityAfter(ctx context.Context, now time.Time, limit int, after string) ([]activity.Event, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT payload FROM np_outbox WHERE topic=$1 AND processed_at IS NULL AND available_at <= $2 AND event_key COLLATE "C" > $3 COLLATE "C" ORDER BY event_key COLLATE "C" LIMIT $4`, s.topic, now, after, limit)
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
		var event activity.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		if err := event.Validate(); err != nil {
			return nil, err
		}
		items = append(items, event)
	}
	return items, rows.Err()
}

func (r *QuestionsRepository) PendingActivityAfter(ctx context.Context, now time.Time, limit int, after string) ([]activity.Event, error) {
	state, err := r.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return activity.PendingAfter(state.Events, now, limit, after), nil
}

func (r *CommunityRepository) PendingActivityAfter(ctx context.Context, now time.Time, limit int, after string) ([]activity.Event, error) {
	rows, err := r.pool.Query(ctx, `SELECT data->'activities' FROM np_community_circles WHERE jsonb_array_length(COALESCE(data->'activities','[]'::jsonb))>0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var page []activity.Event
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var events []activity.Event
		if err := json.Unmarshal(raw, &events); err != nil {
			return nil, err
		}
		if err := activity.Validate(events); err != nil {
			return nil, err
		}
		candidates := activity.PendingAfter(events, now, limit, after)
		page = activity.PendingAfter(append(page, candidates...), now, limit, after)
	}
	return page, rows.Err()
}
