package hook

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDispatchCancelledContextSkipsHandlers(t *testing.T) {
	for _, subscribed := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "subscribed"}[subscribed], func(t *testing.T) {
			registry := NewRegistry()
			if subscribed {
				if err := registry.Subscribe("filter", Subscription{Plugin: "filter", Handler: func(_ context.Context, event Event) (Event, error) {
					t.Error("cancelled dispatch invoked a handler")
					return event, nil
				}}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err := registry.Dispatch(ctx, Event{Name: "filter"})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("dispatch error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestDispatchRejectsResultAfterContextEnds(t *testing.T) {
	for _, cause := range []string{"timeout", "parent cancellation"} {
		t.Run(cause, func(t *testing.T) {
			registry := NewRegistry()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wantErr := error(context.DeadlineExceeded)
			if cause == "parent cancellation" {
				wantErr = context.Canceled
			}
			if err := registry.Subscribe("filter", Subscription{Plugin: "first", Timeout: 10 * time.Millisecond, Handler: func(handlerCtx context.Context, _ Event) (Event, error) {
				if cause == "parent cancellation" {
					cancel()
				}
				<-handlerCtx.Done()
				// A handler may finish cleanup without forwarding ctx.Err().
				return Event{Name: "late result"}, nil
			}}); err != nil {
				t.Fatal(err)
			}
			if err := registry.Subscribe("filter", Subscription{Plugin: "next", Priority: 1, Handler: func(_ context.Context, event Event) (Event, error) {
				t.Error("dispatch continued after cancellation or timeout")
				return event, nil
			}}); err != nil {
				t.Fatal(err)
			}
			got, err := registry.Dispatch(ctx, Event{Name: "filter"})
			if !errors.Is(err, wantErr) || got.Name != "filter" {
				t.Fatalf("dispatch = (%q, %v), want unchanged event and %v", got.Name, err, wantErr)
			}
		})
	}
}
