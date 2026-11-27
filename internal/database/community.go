package database

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/community"
	"sort"
	"strings"
	"time"
)

type CommunityRepository struct{ pool *pgxpool.Pool }

func NewCommunityRepository(pool *pgxpool.Pool) *CommunityRepository {
	return &CommunityRepository{pool}
}
func (r *CommunityRepository) Ready(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `SELECT slug,version FROM np_community_circles LIMIT 0`)
	return err
}
func (r *CommunityRepository) List(ctx context.Context) ([]community.Circle, error) {
	rows, err := r.pool.Query(ctx, `SELECT data - 'topics' - 'receipts' - 'reports' - 'changes' - 'activities' - 'attachments' FROM np_community_circles ORDER BY slug LIMIT 101`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []community.Circle{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		var c community.Circle
		if err = json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		if err = community.Validate(c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) > community.MaxCircles {
		return nil, community.ErrCapacity
	}
	return out, rows.Err()
}
func (r *CommunityRepository) Get(ctx context.Context, slug string) (community.Circle, error) {
	var b []byte
	err := r.pool.QueryRow(ctx, `SELECT data FROM np_community_circles WHERE slug=$1`, slug).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return community.Circle{}, community.ErrNotFound
	}
	if err != nil {
		return community.Circle{}, err
	}
	return community.DecodeCircle(b)
}
func (r *CommunityRepository) Apply(ctx context.Context, slug string, a community.Actor, cmd community.Command, now time.Time) (community.Result, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return community.Result{}, err
	}
	defer tx.Rollback(context.Background())
	// Serialize only registry creation/capacity, ordinary writes lock one circle.
	if cmd.Action == "create" {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1760034107)`); err != nil {
			return community.Result{}, err
		}
	}
	var b []byte
	var current *community.Circle
	err = tx.QueryRow(ctx, `SELECT data FROM np_community_circles WHERE slug=$1 FOR UPDATE`, slug).Scan(&b)
	if err == nil {
		c, e := community.DecodeCircle(b)
		if e != nil {
			return community.Result{}, e
		}
		current = &c
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return community.Result{}, err
	}
	if current == nil && cmd.Action == "create" {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM np_community_circles`).Scan(&count); err != nil {
			return community.Result{}, err
		}
		if count >= community.MaxCircles {
			return community.Result{}, community.ErrCapacity
		}
	}
	next, result, err := community.Mutate(current, slug, a, cmd, now)
	if err != nil {
		return community.Result{}, err
	}
	if result.Replay {
		return result, nil
	}
	b, err = json.Marshal(next)
	if err != nil {
		return community.Result{}, err
	}
	if current == nil {
		_, err = tx.Exec(ctx, `INSERT INTO np_community_circles(slug,version,data,updated_at) VALUES($1,$2,$3,$4)`, slug, next.Version, b, next.UpdatedAt)
	} else {
		_, err = tx.Exec(ctx, `UPDATE np_community_circles SET version=$2,data=$3,updated_at=$4 WHERE slug=$1`, slug, next.Version, b, next.UpdatedAt)
	}
	if err != nil {
		return community.Result{}, err
	}
	event := community.AuditEvent(a, cmd, result)
	metadata, _ := json.Marshal(event.Metadata)
	if _, err = tx.Exec(ctx, `INSERT INTO np_audit_events(actor_label,action,object_type,object_id,metadata) VALUES($1,$2,$3,$4,$5)`, event.Actor, event.Action, event.ObjectType, event.ObjectID, metadata); err != nil {
		return community.Result{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return community.Result{}, err
	}
	return result, nil
}

func (r *CommunityRepository) PendingActivity(ctx context.Context, now time.Time, limit int) ([]activity.Event, error) {
	rows, err := r.pool.Query(ctx, `SELECT data->'activities' FROM np_community_circles WHERE jsonb_array_length(COALESCE(data->'activities','[]'::jsonb))>0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := []activity.Event{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		var events []activity.Event
		if err = json.Unmarshal(b, &events); err != nil {
			return nil, err
		}
		if err = activity.Validate(events); err != nil {
			return nil, err
		}
		all = append(all, events...)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].At.Equal(all[j].At) {
			return all[i].Key < all[j].Key
		}
		return all[i].At.Before(all[j].At)
	})
	return activity.Pending(all, now, limit), nil
}
func (r *CommunityRepository) AckActivity(ctx context.Context, key string) error {
	parts := strings.Split(key, ":")
	if len(parts) != 3 || parts[0] != "community" || !community.ValidSlug(parts[1]) {
		return community.ErrInvalid
	}
	_, err := r.pool.Exec(ctx, `UPDATE np_community_circles SET data=jsonb_set(data,'{activities}',COALESCE((SELECT jsonb_agg(e) FROM jsonb_array_elements(COALESCE(data->'activities','[]'::jsonb)) e WHERE e->>'key'<>$2),'[]'::jsonb)) WHERE slug=$1`, parts[1], key)
	return err
}
