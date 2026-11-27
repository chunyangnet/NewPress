package httpserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"newpress.local/newpress/internal/config"
)

func TestShutdownWaitsForAdmittedWritesAfterConnectionTimeout(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	server := &Server{config: config.Config{ShutdownTimeout: 10 * time.Millisecond}}
	handler := server.requests.track(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(entered)
		<-request.Context().Done()
		close(cancelled)
		<-release // Represents a file commit already in progress.
	}))
	endpoint := httptest.NewUnstartedServer(handler)
	server.http = endpoint.Config
	endpoint.Start()
	defer endpoint.Close()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, err := endpoint.Client().Get(endpoint.URL)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}()
	<-entered
	finished := make(chan error, 1)
	go func() { finished <- server.shutdown() }()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("timed-out shutdown did not cancel the connection")
	}
	select {
	case err := <-finished:
		close(release)
		t.Fatalf("shutdown returned while a write was active: %v", err)
	default:
	}
	lateRequest := httptest.NewRecorder()
	handler.ServeHTTP(lateRequest, httptest.NewRequest(http.MethodPost, "/", nil))
	if lateRequest.Code != http.StatusServiceUnavailable {
		close(release)
		t.Fatal("shutdown admitted a new request")
	}
	close(release)
	if err := <-finished; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown did not retain timeout reason: %v", err)
	}
	<-requestDone
}
