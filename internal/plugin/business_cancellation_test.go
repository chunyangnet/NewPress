package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

var cancellationInput = BusinessInput{Event: "content.render", Payload: json.RawMessage(`{"method":"content.get","id":1}`)}

func pluginExecutionStatus(t *testing.T, manager *Manager, slug string) ExecutionStatus {
	t.Helper()
	_, _, status, err := manager.Configuration(slug)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func healthyPluginRead(context.Context, int64) (any, error) {
	return map[string]int64{"id": 1}, nil
}

func TestBusinessCallerCancellationDoesNotTripCircuit(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			manager, manifest, _ := configurationLimitFixture(t, 0)
			// Warm the compilation cache before exercising the caller's deadline.
			if _, err := manager.Dispatch(t.Context(), manifest.Slug, cancellationInput, healthyPluginRead); err != nil {
				t.Fatal(err)
			}
			for attempt := range 3 {
				ctx, cancel := context.WithCancel(t.Context())
				want := context.Canceled
				if mode == "deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(t.Context(), 75*time.Millisecond)
					want = context.DeadlineExceeded
				}
				_, err := manager.Dispatch(ctx, manifest.Slug, cancellationInput, func(call context.Context, _ int64) (any, error) {
					if mode == "cancel" {
						cancel()
					}
					<-call.Done()
					return nil, call.Err()
				})
				cancel()
				if !errors.Is(err, want) {
					t.Fatalf("attempt %d: got %v, want %v", attempt, err, want)
				}
			}
			status := pluginExecutionStatus(t, manager, manifest.Slug)
			if status.Failures != 0 || status.Consecutive != 0 || !status.BlockedUntil.IsZero() || status.LastError != "" {
				t.Errorf("caller interruptions were counted as plugin failures: %+v", status)
			}
			if _, err := manager.Dispatch(t.Context(), manifest.Slug, cancellationInput, healthyPluginRead); err != nil {
				t.Errorf("healthy request after caller cancellation was rejected: %v", err)
			}
		})
	}
}

func TestBusinessCallerCancellationPreservesFailureStreak(t *testing.T) {
	manager, manifest, _ := configurationLimitFixture(t, 0)
	_, err := manager.Dispatch(t.Context(), manifest.Slug, cancellationInput, func(ctx context.Context, _ int64) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("sandbox timeout did not fail", err)
	}
	prior := pluginExecutionStatus(t, manager, manifest.Slug)
	if prior.Failures != 1 || prior.Consecutive != 1 {
		t.Fatalf("sandbox failure was not recorded: %+v", prior)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err = manager.Dispatch(ctx, manifest.Slug, cancellationInput, func(call context.Context, _ int64) (any, error) {
		cancel()
		<-call.Done()
		return nil, call.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	after := pluginExecutionStatus(t, manager, manifest.Slug)
	if after.Calls != prior.Calls+1 || after.Failures != prior.Failures || after.Consecutive != prior.Consecutive || after.LastError != prior.LastError || !after.BlockedUntil.Equal(prior.BlockedUntil) {
		t.Fatalf("caller cancellation changed the failure history: prior=%+v after=%+v", prior, after)
	}
	if _, err = manager.Dispatch(t.Context(), manifest.Slug, cancellationInput, healthyPluginRead); err != nil {
		t.Fatal(err)
	}
	if status := pluginExecutionStatus(t, manager, manifest.Slug); status.Consecutive != 0 || status.LastError != "" || status.Failures != 1 {
		t.Fatalf("successful execution did not clear only the active failure streak: %+v", status)
	}
}

func TestBusinessSandboxTimeoutStillTripsCircuit(t *testing.T) {
	manager, manifest, _ := configurationLimitFixture(t, 0)
	for range 3 {
		ctx, cancel := context.WithCancel(t.Context())
		_, err := manager.Dispatch(ctx, manifest.Slug, cancellationInput, func(call context.Context, _ int64) (any, error) {
			<-call.Done()
			// The local sandbox deadline fired first. A later caller cancellation
			// must not erase the plugin's own timeout from the failure history.
			cancel()
			return nil, call.Err()
		})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}
	status := pluginExecutionStatus(t, manager, manifest.Slug)
	if status.Failures != 3 || status.Consecutive != 3 || !status.BlockedUntil.After(time.Now()) {
		t.Fatalf("sandbox deadlines no longer trip the circuit: %+v", status)
	}
	called := false
	if _, err := manager.Dispatch(t.Context(), manifest.Slug, cancellationInput, func(context.Context, int64) (any, error) {
		called = true
		return nil, nil
	}); err == nil || called {
		t.Fatalf("cooldown admitted another execution: called=%v err=%v", called, err)
	}
}

func TestBusinessAlreadyCancelledContextDoesNotEnterRuntime(t *testing.T) {
	manager, manifest, _ := configurationLimitFixture(t, 0)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 16 {
		if _, err := manager.Dispatch(ctx, manifest.Slug, cancellationInput, healthyPluginRead); !errors.Is(err, context.Canceled) {
			t.Errorf("already cancelled caller returned %v", err)
		}
	}
	if status := pluginExecutionStatus(t, manager, manifest.Slug); status.Calls != 0 || status.Failures != 0 {
		t.Fatalf("already cancelled caller entered runtime: %+v", status)
	}
}
