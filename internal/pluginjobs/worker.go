package pluginjobs

import (
	"context"
	"errors"
	"time"
)

// Executor must recheck the current plugin epoch and principal before every
// side effect, not only on entry. Side effects must use durable idempotency keys
// derived from Job.ID: a lease fences completion, not external operations.
type Executor interface {
	Execute(context.Context, Job) error
}

// ClaimingExecutor can restrict acquisition to the runtimes available on this
// instance. Storage still owns the atomic claim and lease transition.
type ClaimingExecutor interface {
	Claim(context.Context, Store, time.Time) (Job, bool, error)
}

type ClaimScope struct {
	Plugin string
	Epoch  string
}

// ScopedClaimer may also return genuinely revoked work for cancellation, but
// must leave valid work outside the supplied local scope untouched.
type ScopedClaimer interface {
	ClaimScoped(context.Context, time.Time, []ClaimScope) (Job, bool, error)
}

var ErrAuthorization = errors.New("extension task authorization changed")

// Store exposes atomic task operations without requiring a full-state snapshot.
// Relational implementations can lock only the selected task.
type Store interface {
	Enqueue(context.Context, Input, time.Time) (Job, error)
	Claim(context.Context, time.Time) (Job, bool, error)
	Finish(context.Context, Job, string, time.Time) error
	Change(context.Context, string, string, int64, string, time.Time) error
}

// ManagedStore adds bounded, redacted administrative queries to task operations.
type ManagedStore interface {
	Store
	List(context.Context, Query) (Page, error)
	CheckClaim(context.Context, Job, time.Time) error
}

// Worker processes one job per tick without logging private payloads or raw errors.
type Worker struct {
	Queue    Store
	Executor Executor
}

func (w *Worker) Tick(ctx context.Context) (bool, error) {
	if w.Queue == nil || w.Executor == nil {
		return false, ErrInvalid
	}
	var job Job
	var ok bool
	var err error
	now := time.Now().UTC()
	if executor, scoped := w.Executor.(ClaimingExecutor); scoped {
		job, ok, err = executor.Claim(ctx, w.Queue, now)
	} else {
		job, ok, err = w.Queue.Claim(ctx, now)
	}
	if err != nil || !ok {
		return false, err
	}
	// Reserve time to persist completion before the lease expires. A process
	// shutdown leaves a running task for lease recovery rather than marking it
	// successful or retrying a potentially committed side effect immediately.
	execution, cancel := context.WithDeadline(ctx, job.LeaseUntil.Add(-time.Second))
	err = w.Executor.Execute(execution, job)
	executionErr := execution.Err()
	cancel()
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	code := ""
	if errors.Is(err, ErrAuthorization) {
		code = "authorization_changed"
	} else if err != nil || executionErr != nil {
		code = "execution_failed"
	}
	return true, w.Queue.Finish(ctx, job, code, time.Now().UTC())
}

// Run drains available work without spinning when idle or storage is failing.
// The application owns cancellation and must join Run before closing storage.
func (w *Worker) Run(ctx context.Context) error {
	if w.Queue == nil || w.Executor == nil {
		return ErrInvalid
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		worked, err := w.Tick(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if worked && err == nil {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
