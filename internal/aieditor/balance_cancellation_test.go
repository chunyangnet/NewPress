package aieditor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func balanceCancellationSettings(t *testing.T) *SettingsStore {
	t.Helper()
	s, err := NewSettingsStore("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultTelemetryConfig()
	cfg.Balances = true
	_, err = s.Save(Settings{Revision: s.Snapshot().Revision, DefaultProvider: "p", Telemetry: &cfg, Providers: []Provider{{ID: "p", Name: "Test", BaseURL: "https://api.example.test/v1", APIKey: "synthetic", Models: []string{"m"}, Balance: BalanceConfig{Enabled: true, Format: "sub2api", CacheSeconds: 60}}}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func balanceCancellationResponse() *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"remaining":20,"quota":{"used":10,"limit":30},"unit":"USD"}`))}
}

func TestBalanceQueryCancelledBeforeLookup(t *testing.T) {
	for _, mode := range []string{"cold", "cached", "cached-error"} {
		t.Run(mode, func(t *testing.T) {
			s := NewBalanceService()
			settings := balanceCancellationSettings(t)
			var calls atomic.Int32
			s.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				if mode == "cached-error" {
					return nil, errors.New("upstream unavailable")
				}
				return balanceCancellationResponse(), nil
			})}
			if mode != "cold" {
				_, _ = s.Query(t.Context(), settings, "p")
			}
			before := calls.Load()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			result, err := s.Query(ctx, settings, "p")
			if !errors.Is(err, context.Canceled) || result.Remaining != nil || calls.Load() != before {
				t.Fatal("cancelled caller read cache or started a probe", err, calls.Load(), before)
			}
		})
	}
}

type balanceQueryOutcome struct {
	result BalanceResult
	err    error
}

func balanceQueryAsync(ctx context.Context, s *BalanceService, settings *SettingsStore) <-chan balanceQueryOutcome {
	done := make(chan balanceQueryOutcome, 1)
	go func() {
		result, err := s.Query(ctx, settings, "p")
		done <- balanceQueryOutcome{result, err}
	}()
	return done
}

type balanceWaitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *balanceWaitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func balanceWait(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal(description)
	}
}

func balanceOutcome(t *testing.T, done <-chan balanceQueryOutcome) balanceQueryOutcome {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("balance query did not return")
		return balanceQueryOutcome{}
	}
}

func TestBalanceQueryCancellationSharesWorker(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		name := "waiter"
		if cancelFirst {
			name = "initiator"
		}
		t.Run(name, func(t *testing.T) {
			s := NewBalanceService()
			settings := balanceCancellationSettings(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			finish := func() { releaseOnce.Do(func() { close(release) }) }
			defer finish()
			var calls atomic.Int32
			s.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 {
					close(entered)
				}
				select {
				case <-release:
					return balanceCancellationResponse(), nil
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			})}
			firstCtx, cancel1 := context.WithCancel(t.Context())
			defer cancel1()
			first := balanceQueryAsync(firstCtx, s, settings)
			balanceWait(t, entered, "probe did not start")
			secondCtx, cancel2 := context.WithCancel(t.Context())
			defer cancel2()
			observed := &balanceWaitingContext{Context: secondCtx, waiting: make(chan struct{})}
			second := balanceQueryAsync(observed, s, settings)
			balanceWait(t, observed.waiting, "second caller did not join")
			cancelled, remaining := second, first
			if cancelFirst {
				cancel1()
				cancelled, remaining = first, second
			} else {
				cancel2()
			}
			outcome := balanceOutcome(t, cancelled)
			if !errors.Is(outcome.err, context.Canceled) || outcome.result.Remaining != nil {
				t.Fatal("cancelled caller returned balance", outcome.err)
			}
			// The other browser still needs the shared request.
			finish()
			outcome = balanceOutcome(t, remaining)
			if outcome.err != nil || outcome.result.Remaining == nil || *outcome.result.Remaining != 20 || calls.Load() != 1 {
				t.Fatal("one caller cancelled the shared query", outcome.err, calls.Load())
			}
		})
	}
}

func TestBalanceQueryLastWaiterCancelsWorker(t *testing.T) {
	s := NewBalanceService()
	settings := balanceCancellationSettings(t)
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	s.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) > 1 {
			return balanceCancellationResponse(), nil
		}
		close(entered)
		select {
		case <-r.Context().Done():
			close(cancelled)
			return nil, r.Context().Err()
		case <-release:
			return balanceCancellationResponse(), nil
		}
	})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := balanceQueryAsync(ctx, s, settings)
	balanceWait(t, entered, "probe did not start")
	cancel()
	if outcome := balanceOutcome(t, done); !errors.Is(outcome.err, context.Canceled) {
		t.Fatal("last caller did not cancel", outcome.err)
	}
	balanceWait(t, cancelled, "unused upstream was not cancelled")
	result, err := s.Query(t.Context(), settings, "p")
	if err != nil || result.Remaining == nil || *result.Remaining != 20 || result.Cached || calls.Load() != 2 {
		t.Fatal("abandoned query poisoned cache", err, calls.Load())
	}
}

func TestBalanceQueryCancellationWhileQueued(t *testing.T) {
	s := NewBalanceService()
	settings := balanceCancellationSettings(t)
	// Occupy both upstream slots before this provider joins the queue.
	s.slots <- struct{}{}
	s.slots <- struct{}{}
	defer func() { <-s.slots; <-s.slots }()
	var calls atomic.Int32
	s.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return balanceCancellationResponse(), nil
	})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := &balanceWaitingContext{Context: ctx, waiting: make(chan struct{})}
	done := balanceQueryAsync(observed, s, settings)
	// Cancellation must return even when the caller is the queued initiator.
	balanceWait(t, observed.waiting, "queued caller did not start waiting")
	cancel()
	if outcome := balanceOutcome(t, done); !errors.Is(outcome.err, context.Canceled) || calls.Load() != 0 {
		t.Fatal("cancelled queued query started upstream", outcome.err, calls.Load())
	}
}

func TestBalanceQueryResultOwnership(t *testing.T) {
	s := NewBalanceService()
	settings := balanceCancellationSettings(t)
	s.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return balanceCancellationResponse(), nil })}
	first, err := s.Query(t.Context(), settings, "p")
	if err != nil || first.Remaining == nil || first.Used == nil || first.Total == nil {
		t.Fatal("invalid fixture", err)
	}
	*first.Remaining, *first.Used, *first.Total = -1, -2, -3
	second, err := s.Query(t.Context(), settings, "p")
	if err != nil || !second.Cached || *second.Remaining != 20 || *second.Used != 10 || *second.Total != 30 {
		t.Fatal("caller mutated balance cache", err, second)
	}
	*second.Remaining = -4
	third, err := s.Query(t.Context(), settings, "p")
	if err != nil || *third.Remaining != 20 {
		t.Fatal("cached result shared mutable values", err)
	}
}

func TestBalanceQuerySettingsChangedDuringFetch(t *testing.T) {
	for _, change := range []string{"disabled", "removed", "key"} {
		t.Run(change, func(t *testing.T) {
			s := NewBalanceService()
			settings := balanceCancellationSettings(t)
			var calls atomic.Int32
			s.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 {
					cfg := settings.Snapshot()
					switch change {
					case "disabled":
						cfg.Telemetry.Balances = false
					case "removed":
						cfg.Providers, cfg.DefaultProvider = nil, ""
					case "key":
						cfg.Providers[0].APIKey = "replacement"
					}
					if _, err := settings.Save(cfg); err != nil {
						t.Error(err)
					}
				} else if r.Header.Get("Authorization") != "Bearer replacement" {
					t.Error("query reused old credential")
				}
				return balanceCancellationResponse(), nil
			})}
			result, err := s.Query(t.Context(), settings, "p")
			if change == "key" {
				if err != nil || calls.Load() != 2 {
					t.Fatal("query returned result for obsolete configuration", err, calls.Load())
				}
			} else if err == nil || result.Remaining != nil || calls.Load() != 1 {
				t.Fatal("query returned disabled or removed provider", err, calls.Load())
			}
		})
	}
}
