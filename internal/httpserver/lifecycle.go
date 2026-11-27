package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// requestLifetime makes application shutdown a write barrier. Closing network
// connections alone does not wait for handlers that are finishing a disk write.
type requestLifetime struct {
	mu                sync.Mutex
	stopping          bool
	active            sync.WaitGroup
	backgroundContext context.Context
	cancelBackground  context.CancelFunc
}

func (lifetime *requestLifetime) track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		lifetime.mu.Lock()
		if lifetime.stopping {
			lifetime.mu.Unlock()
			writer.Header().Set("Cache-Control", "no-store")
			http.Error(writer, "server is shutting down", http.StatusServiceUnavailable)
			return
		}
		lifetime.active.Add(1)
		lifetime.mu.Unlock()
		defer lifetime.active.Done()
		next.ServeHTTP(writer, request)
	})
}

// background admits bounded refresh work under the same shutdown barrier as requests.
func (lifetime *requestLifetime) background(work func(context.Context)) bool {
	lifetime.mu.Lock()
	defer lifetime.mu.Unlock()
	if lifetime.stopping {
		return false
	}
	if lifetime.backgroundContext == nil {
		lifetime.backgroundContext, lifetime.cancelBackground = context.WithCancel(context.Background())
	}
	ctx := lifetime.backgroundContext
	lifetime.active.Add(1)
	go func() { defer lifetime.active.Done(); work(ctx) }()
	return true
}

func (lifetime *requestLifetime) stop() {
	lifetime.mu.Lock()
	lifetime.stopping = true
	if lifetime.cancelBackground != nil {
		lifetime.cancelBackground()
	}
	lifetime.mu.Unlock()
}

func (s *Server) shutdown() error {
	s.requests.stop()
	timeout := s.config.ShutdownTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := s.http.Shutdown(ctx)
	if err != nil {
		// Force-close slow connections to cancel request contexts and unblock
		// body reads, then wait for already-admitted handlers before App.Close
		// can release the process directory locks.
		err = errors.Join(fmt.Errorf("graceful shutdown: %w", err), s.http.Close())
	}
	s.requests.active.Wait()
	return err
}
