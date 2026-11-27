package database

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/analytics"
)

type AnalyticsRepository struct {
	pool         *pgxpool.Pool
	locationMu   sync.RWMutex
	locationFunc func() *time.Location
}

func NewAnalyticsRepository(pool *pgxpool.Pool) *AnalyticsRepository {
	return &AnalyticsRepository{pool: pool}
}

// SetLocationFunc keeps retention and batch validation on the configured site
// calendar instead of UTC.
func (r *AnalyticsRepository) SetLocationFunc(location func() *time.Location) {
	r.locationMu.Lock()
	r.locationFunc = location
	r.locationMu.Unlock()
}

func (r *AnalyticsRepository) zone(ctx context.Context) *time.Location {
	if location, ok := analytics.LocationFromContext(ctx); ok {
		return location
	}
	r.locationMu.RLock()
	fn := r.locationFunc
	r.locationMu.RUnlock()
	if fn != nil {
		if location := fn(); location != nil {
			return location
		}
	}
	return time.UTC
}
func (r *AnalyticsRepository) Snapshot(ctx context.Context, now time.Time) (analytics.State, error) {
	location := r.zone(ctx)
	var body []byte
	if err := r.pool.QueryRow(ctx, `SELECT data FROM np_analytics WHERE id=1`).Scan(&body); err != nil {
		return analytics.State{}, err
	}
	state, err := analytics.Decode(body)
	if err != nil {
		return analytics.State{}, err
	}
	analytics.PruneIn(&state, now, location)
	return state, nil
}
func (r *AnalyticsRepository) Apply(ctx context.Context, b analytics.Batch, now time.Time) error {
	location := r.zone(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var body []byte
	if err = tx.QueryRow(ctx, `SELECT data FROM np_analytics WHERE id=1 FOR UPDATE`).Scan(&body); err != nil {
		return err
	}
	state, err := analytics.Decode(body)
	if err != nil {
		return err
	}
	next, changed, err := analytics.MergeIn(state, b, now, location)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	body, err = json.Marshal(next)
	if err != nil {
		return err
	}
	if len(body) > analytics.MaxDocumentBytes {
		return analytics.ErrCapacity
	}
	if _, err = tx.Exec(ctx, `UPDATE np_analytics SET data=$1,updated_at=$2 WHERE id=1`, body, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
