package httpserver

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type chinaProfileTransport func(*http.Request) (*http.Response, error)

func (f chinaProfileTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGithubStaleRefreshPreservesDataAndBacksOff(t *testing.T) {
	old := componentHTTP
	defer func() { componentHTTP = old }()
	started, release := make(chan struct{}), make(chan struct{})
	componentHTTP = &http.Client{Transport: chinaProfileTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return nil, errors.New("offline")
	})}
	s := &Server{}
	previous := componentProfileEntry{profile: componentProfile{Name: "cached"}, avatar: []byte("avatar"), expires: time.Now().Add(-time.Minute), staleUntil: time.Now().Add(time.Hour)}
	s.componentProfiles.entries = map[string]componentProfileEntry{"user": previous}
	s.componentProfiles.loading = map[string]bool{}
	got := s.cachedGithub(context.Background(), "user")
	if got.profile.Name != "cached" {
		t.Fatal("lost stale profile")
	}
	<-started
	if s.cachedGithub(context.Background(), "user").profile.Name != "cached" {
		t.Fatal("in-flight cache unavailable")
	}
	close(release)
	s.requests.active.Wait()
	got = s.cachedGithub(context.Background(), "user")
	if got.profile.Name != "cached" || string(got.avatar) != "avatar" || !time.Now().Before(got.retryAfter) {
		t.Fatal("failed refresh replaced cache or omitted backoff")
	}
	s.requests.stop()
}

func TestBackgroundRefreshCancelledOnShutdown(t *testing.T) {
	var life requestLifetime
	done := make(chan struct{})
	if !life.background(func(ctx context.Context) { <-ctx.Done(); close(done) }) {
		t.Fatal("not admitted")
	}
	life.stop()
	life.active.Wait()
	select {
	case <-done:
	default:
		t.Fatal("not cancelled")
	}
	if life.background(func(context.Context) {}) {
		t.Fatal("admitted after shutdown")
	}
}
