package pluginjobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

type executorFunc func(context.Context, Job) error

func (f executorFunc) Execute(ctx context.Context, j Job) error { return f(ctx, j) }

func TestWorkerCompletion(t *testing.T) {
	for _, tc := range []struct {
		name         string
		err          error
		status, code string
	}{
		{"success", nil, "succeeded", ""},
		{"failure", errors.New("private payload must not be stored"), "retry", "execution_failed"},
		{"revoked", ErrAuthorization, "cancelled", "authorization_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			q := testQueue(t, "")
			j, err := q.Enqueue(ctx, testInput(), time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			w := Worker{Queue: q, Executor: executorFunc(func(ctx context.Context, got Job) error {
				if got.ID != j.ID {
					t.Fatal("wrong task")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("missing execution deadline")
				}
				return tc.err
			})}
			if worked, err := w.Tick(ctx); !worked || err != nil {
				t.Fatalf("tick: %v %v", worked, err)
			}
			s, err := q.Backend.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got := s.Jobs[j.ID]
			if got.Status != tc.status || got.ErrorCode != tc.code {
				t.Fatalf("completion: %s %s", got.Status, got.ErrorCode)
			}
		})
	}
}
func TestWorkerShutdownLeavesRecoverableLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := testQueue(t, "")
	j, err := q.Enqueue(ctx, testInput(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	w := Worker{Queue: q, Executor: executorFunc(func(context.Context, Job) error { cancel(); return nil })}
	if _, err = w.Tick(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s, err := q.Backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Jobs[j.ID].Status != "running" {
		t.Fatal("shutdown incorrectly acknowledged work")
	}
}
