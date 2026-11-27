package plugindispatch

import (
	"context"
	"log/slog"
	"time"
)

type Worker struct {
	Store  Store
	Sender Sender
}

func (w *Worker) Once(ctx context.Context) (bool, error) {
	if w.Store == nil || w.Sender == nil {
		return false, ErrUnavailable
	}
	i, found, err := w.Store.Claim(ctx)
	if err != nil || !found {
		return false, err
	}
	// Even a shutdown before the first byte is conservative: the durable claim
	// is not released or retried. Finishing records facts after revoked authority.
	sendCtx, cancel := context.WithTimeout(ctx, SendTimeout)
	out := w.Sender.Send(sendCtx, i)
	cancel()
	if !ValidOutcome(out) || out.Code != "http_2xx" && out.Code != "http_status" && out.Code != "transport_unconfirmed" && out.Code != "response_unconfirmed" {
		out = Outcome{Code: "transport_unconfirmed"}
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	return true, w.Store.Finish(finishCtx, i, out)
}

func (w *Worker) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			_, err := w.Once(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Warn("plugin webhook dispatch deferred")
			}
			timer.Reset(time.Second)
		}
	}
}
