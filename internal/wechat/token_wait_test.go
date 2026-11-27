package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type tokenWaitResult struct {
	token string
	err   error
}

func TestTokenWaitCanCancelWithoutInterruptingRefresh(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "deadline"}[deadline], func(t *testing.T) {
			client := NewClient()
			cfg := Config{AppID: "wx0123456789abcdef", Secret: strings.Repeat("s", 32)}
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			var requests atomic.Int32
			client.HTTP = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
				if requests.Add(1) == 1 {
					close(entered)
				}
				select {
				case <-release:
					return reply(req, `{"access_token":"shared-token","expires_in":7200}`), nil
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
			})}
			leader := make(chan tokenWaitResult, 1)
			go func() { token, err := client.Token(t.Context(), cfg); leader <- tokenWaitResult{token, err} }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("refresh did not start")
			}
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			started, waiting := make(chan struct{}), make(chan tokenWaitResult, 1)
			go func() {
				close(started)
				token, err := client.Token(ctx, cfg)
				waiting <- tokenWaitResult{token, err}
			}()
			<-started
			if !deadline {
				cancel()
			}
			returned := false
			select {
			case result := <-waiting:
				returned = true
				if result.token != "" || !errors.Is(result.err, want) {
					t.Error("canceled waiter received a token", result.token, result.err)
				}
			case <-time.After(time.Second):
				t.Error("canceled token waiter remained blocked behind another HTTP request")
			}
			select {
			case result := <-leader:
				t.Fatal("waiting caller interrupted active refresh", result)
			default:
			}
			unblock()
			result := <-leader
			if result.token != "shared-token" || result.err != nil || requests.Load() != 1 {
				t.Fatal(result, requests.Load())
			}
			if !returned {
				<-waiting
			}
			if token, err := client.Token(t.Context(), cfg); token != "shared-token" || err != nil || requests.Load() != 1 {
				t.Fatal("waiter discarded healthy cache", token, err, requests.Load())
			}
		})
	}
}

func TestTokenCacheRespectsCanceledContext(t *testing.T) {
	client := NewClient()
	requests := 0
	client.HTTP = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		requests++
		return reply(req, `{"access_token":"cached-token","expires_in":7200}`), nil
	})}
	cfg := Config{AppID: "wx0123456789abcdef", Secret: strings.Repeat("s", 32)}
	if _, err := client.Token(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if token, err := client.Token(ctx, cfg); token != "" || !errors.Is(err, context.Canceled) || requests != 1 {
		t.Fatal("canceled caller used cached success", token, err, requests)
	}
}

func TestTokenConcurrentRefreshAndCredentialChanges(t *testing.T) {
	client := NewClient()
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	var requests atomic.Int32
	client.HTTP = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		var input struct {
			AppID  string `json:"appid"`
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			return nil, err
		}
		if requests.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		token := "first-token"
		if input.Secret == strings.Repeat("n", 32) {
			token = "next-token"
		}
		return reply(req, `{"access_token":"`+token+`","expires_in":7200}`), nil
	})}
	cfg := Config{AppID: "wx0123456789abcdef", Secret: strings.Repeat("s", 32)}
	results := make(chan tokenWaitResult, 16)
	var started sync.WaitGroup
	started.Add(cap(results))
	for range cap(results) {
		go func() {
			started.Done()
			token, err := client.Token(t.Context(), cfg)
			results <- tokenWaitResult{token, err}
		}()
	}
	started.Wait()
	<-entered
	unblock.Do(func() { close(release) })
	for range cap(results) {
		result := <-results
		if result.token != "first-token" || result.err != nil {
			t.Fatal(result)
		}
	}
	if requests.Load() != 1 {
		t.Fatal("same credentials refreshed more than once", requests.Load())
	}
	cfg.Secret = strings.Repeat("n", 32)
	if token, err := client.Token(t.Context(), cfg); token != "next-token" || err != nil || requests.Load() != 2 {
		t.Fatal("new credentials reused an old token", token, err, requests.Load())
	}
}

func TestTokenCanceledRefreshReleasesNextCaller(t *testing.T) {
	client := NewClient()
	entered := make(chan struct{})
	var requests atomic.Int32
	client.HTTP = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			close(entered)
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		return reply(req, `{"access_token":"recovered-token","expires_in":7200}`), nil
	})}
	cfg := Config{AppID: "wx0123456789abcdef", Secret: strings.Repeat("s", 32)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	leader := make(chan tokenWaitResult, 1)
	go func() { token, err := client.Token(ctx, cfg); leader <- tokenWaitResult{token, err} }()
	<-entered
	waiting := make(chan tokenWaitResult, 1)
	go func() { token, err := client.Token(t.Context(), cfg); waiting <- tokenWaitResult{token, err} }()
	cancel()
	result := <-leader
	if result.token != "" || !errors.Is(result.err, context.Canceled) {
		t.Error("refresh cancellation lost its context error", result.token, result.err)
	}
	select {
	case result = <-waiting:
		if result.token != "recovered-token" || result.err != nil || requests.Load() != 2 {
			t.Fatal("next caller could not recover", result, requests.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed refresh never released its waiters")
	}
}

func TestTokenCredentialChangeDuringRefreshDoesNotReuseOldToken(t *testing.T) {
	client := NewClient()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var requests atomic.Int32
	client.HTTP = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		var input struct {
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			return nil, err
		}
		if requests.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
		token := "old-token"
		if input.Secret == strings.Repeat("n", 32) {
			token = "rotated-token"
		}
		return reply(req, `{"access_token":"`+token+`","expires_in":7200}`), nil
	})}
	cfg := Config{AppID: "wx0123456789abcdef", Secret: strings.Repeat("s", 32)}
	next := cfg
	next.Secret = strings.Repeat("n", 32)
	leader, waiting := make(chan tokenWaitResult, 1), make(chan tokenWaitResult, 1)
	go func() { token, err := client.Token(t.Context(), cfg); leader <- tokenWaitResult{token, err} }()
	<-entered
	go func() { token, err := client.Token(t.Context(), next); waiting <- tokenWaitResult{token, err} }()
	unblock()
	if result := <-leader; result.token != "old-token" || result.err != nil {
		t.Fatal(result)
	}
	if result := <-waiting; result.token != "rotated-token" || result.err != nil || requests.Load() != 2 {
		t.Fatal("credential change consumed an old refresh", result, requests.Load())
	}
}

func TestTokenCanceledResponseIsNotCached(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	requests := 0
	client.HTTP = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			cancel()
		}
		// Simulate a response reaching the caller as its request is canceled.
		return reply(req, `{"access_token":"late-token","expires_in":7200}`), nil
	})}
	cfg := Config{AppID: "wx0123456789abcdef", Secret: strings.Repeat("s", 32)}
	if token, err := client.Token(ctx, cfg); token != "" || !errors.Is(err, context.Canceled) {
		t.Error("late response returned success", token, err)
	}
	if token, err := client.Token(t.Context(), cfg); token != "late-token" || err != nil || requests != 2 {
		t.Fatal("canceled refresh populated the cache", token, err, requests)
	}
}
