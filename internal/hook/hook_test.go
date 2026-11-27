package hook

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestDispatchUsesPriorityOrder(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	var order []string
	for _, subscription := range []Subscription{
		{Plugin: "late", Priority: 200, Handler: record(&order, "late")},
		{Plugin: "early", Priority: 10, Handler: record(&order, "early")},
		{Plugin: "middle", Priority: 100, Handler: record(&order, "middle")},
	} {
		if err := registry.Subscribe("content.filter", subscription); err != nil {
			t.Fatal(err)
		}
	}
	_, err := registry.Dispatch(context.Background(), Event{Name: "content.filter", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if expected := []string{"early", "middle", "late"}; !reflect.DeepEqual(order, expected) {
		t.Fatalf("order = %v, want %v", order, expected)
	}
}

func TestDispatchEnforcesTimeout(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	if err := registry.Subscribe("slow", Subscription{
		Plugin:  "slow-plugin",
		Timeout: 10 * time.Millisecond,
		Handler: func(ctx context.Context, event Event) (Event, error) {
			<-ctx.Done()
			return event, ctx.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := registry.Dispatch(context.Background(), Event{Name: "slow"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func record(order *[]string, value string) Handler {
	return func(_ context.Context, event Event) (Event, error) {
		*order = append(*order, value)
		return event, nil
	}
}
