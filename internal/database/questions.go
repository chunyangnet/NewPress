package database

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/questions"
	"time"
)

type QuestionsRepository struct{ pool *pgxpool.Pool }

func NewQuestionsRepository(p *pgxpool.Pool) *QuestionsRepository { return &QuestionsRepository{p} }
func (r *QuestionsRepository) Ready(ctx context.Context) error    { _, e := r.Snapshot(ctx); return e }
func (r *QuestionsRepository) Snapshot(ctx context.Context) (questions.State, error) {
	var b []byte
	e := r.pool.QueryRow(ctx, "SELECT data FROM np_questions_state WHERE id=1").Scan(&b)
	if e != nil {
		return questions.State{}, e
	}
	return questions.Decode(b)
}
func (r *QuestionsRepository) mutate(ctx context.Context, fn func(questions.State) (questions.State, questions.Result, error)) (questions.Result, error) {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return questions.Result{}, e
	}
	defer tx.Rollback(context.Background())
	var b []byte
	if e = tx.QueryRow(ctx, "SELECT data FROM np_questions_state WHERE id=1 FOR UPDATE").Scan(&b); e != nil {
		return questions.Result{}, e
	}
	s, e := questions.Decode(b)
	if e != nil {
		return questions.Result{}, e
	}
	next, result, e := fn(s)
	if e != nil || result.Replay {
		return result, e
	}
	b, e = json.Marshal(next)
	if e != nil {
		return questions.Result{}, e
	}
	if len(b) > questions.MaxBytes {
		return questions.Result{}, questions.ErrCapacity
	}
	if _, e = tx.Exec(ctx, "UPDATE np_questions_state SET data=$1 WHERE id=1", b); e != nil {
		return questions.Result{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return questions.Result{}, e
	}
	return result, nil
}
func (r *QuestionsRepository) Apply(ctx context.Context, id int64, a questions.Actor, c questions.Command, now time.Time) (questions.Result, error) {
	return r.mutate(ctx, func(s questions.State) (questions.State, questions.Result, error) {
		return questions.Apply(s, id, a, c, now)
	})
}
func (r *QuestionsRepository) PendingActivity(ctx context.Context, now time.Time, limit int) ([]activity.Event, error) {
	s, e := r.Snapshot(ctx)
	if e != nil {
		return nil, e
	}
	return activity.Pending(s.Events, now, limit), nil
}
func (r *QuestionsRepository) AckActivity(ctx context.Context, key string) error {
	_, e := r.mutate(ctx, func(s questions.State) (questions.State, questions.Result, error) {
		s.Events = activity.Without(s.Events, key)
		return s, questions.Result{}, nil
	})
	return e
}
