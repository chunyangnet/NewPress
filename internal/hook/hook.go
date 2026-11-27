package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Event struct {
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload"`
	Meta    map[string]any  `json:"meta,omitempty"`
}

type Handler func(context.Context, Event) (Event, error)

type Subscription struct {
	Plugin   string
	Priority int
	Timeout  time.Duration
	Handler  Handler
}

type Registry struct {
	mu       sync.RWMutex
	handlers map[string][]Subscription
}

func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string][]Subscription)}
}

func (r *Registry) Subscribe(event string, subscription Subscription) error {
	if event == "" || subscription.Plugin == "" || subscription.Handler == nil {
		return fmt.Errorf("event, plugin and handler are required")
	}
	if subscription.Timeout <= 0 {
		subscription.Timeout = 250 * time.Millisecond
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[event] = append(r.handlers[event], subscription)
	sort.SliceStable(r.handlers[event], func(i, j int) bool {
		return r.handlers[event][i].Priority < r.handlers[event][j].Priority
	})
	return nil
}

func (r *Registry) Dispatch(ctx context.Context, event Event) (Event, error) {
	if err := ctx.Err(); err != nil {
		return event, err
	}
	r.mu.RLock()
	handlers := append([]Subscription(nil), r.handlers[event.Name]...)
	r.mu.RUnlock()

	current := event
	for _, subscription := range handlers {
		if err := ctx.Err(); err != nil {
			return current, err
		}
		handlerCtx, cancel := context.WithTimeout(ctx, subscription.Timeout)
		result, err := subscription.Handler(handlerCtx, current)
		if err == nil {
			err = handlerCtx.Err()
		}
		cancel()
		if err != nil {
			return current, fmt.Errorf("plugin %s handling %s: %w", subscription.Plugin, event.Name, err)
		}
		current = result
	}
	return current, nil
}
