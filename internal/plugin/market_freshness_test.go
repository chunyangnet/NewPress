package plugin

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCancelledMarketRefreshDoesNotCacheFailure(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "first load", true: "verified catalog"}[cached], func(t *testing.T) {
			m, catalog, key, _ := mirrorFixture(t)
			signed := signCatalog(t, catalog, key)
			m.client = &http.Client{Transport: marketTransport(func(*http.Request) (*http.Response, error) {
				return catalogResponse(signed), nil
			})}
			if cached {
				if err := m.Refresh(t.Context()); err != nil {
					t.Fatal(err)
				}
				m.view.Checked = time.Now().Add(-time.Minute)
			}
			before := m.View()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started := make(chan struct{}, 2)
			m.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
				started <- struct{}{}
				<-r.Context().Done()
				return nil, r.Context().Err()
			})}
			done := make(chan error, 1)
			go func() { done <- m.RefreshIfStale(ctx, 30*time.Second) }()
			<-started
			<-started
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled refresh: %v", err)
			}
			if !reflect.DeepEqual(before, m.View()) {
				t.Fatal("cancelled browser request changed the shared market snapshot")
			}
			var calls atomic.Int32
			m.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.URL.String() == githubCatalog {
					return nil, errors.New("GitHub unavailable")
				}
				return catalogResponse(signed), nil
			})}
			if err := m.RefreshIfStale(t.Context(), 30*time.Second); err != nil {
				t.Fatalf("Gitee recovery was blocked by a cancelled check: %v", err)
			}
			if calls.Load() != 2 || m.View().Source != giteeCatalog || m.View().Error != "" {
				t.Fatalf("did not immediately retry both mirrors: calls=%d view=%+v", calls.Load(), m.View())
			}
		})
	}
}

func TestAutomaticMarketRefreshCoalescesAndManualRefreshBypassesAge(t *testing.T) {
	public, key, _ := ed25519.GenerateKey(rand.Reader)
	m, err := NewMarket(NewManager(t.TempDir()), MarketSource{[]string{githubCatalog}, base64.StdEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var sequence atomic.Int64
	sequence.Store(1)
	m.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("Cache-Control") != "no-cache" {
			t.Error("catalog request did not revalidate the repository cache")
		}
		catalog := Catalog{Schema: 2, Sequence: sequence.Load(), Expires: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}
		return catalogResponse(signCatalog(t, catalog, key)), nil
	})}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.RefreshIfStale(t.Context(), 30*time.Second); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || m.View().Sequence != 1 {
		t.Fatalf("concurrent automatic checks were not coalesced: %d calls", calls.Load())
	}
	sequence.Store(2)
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || m.View().Sequence != 2 {
		t.Fatal("manual refresh did not fetch a newly published catalog")
	}
	m.mu.Lock()
	m.view.Checked = time.Now().Add(-31 * time.Second)
	m.mu.Unlock()
	sequence.Store(3)
	if err := m.RefreshIfStale(t.Context(), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || m.View().Sequence != 3 {
		t.Fatal("stale automatic check did not fetch a newly published catalog")
	}
}

func TestAutomaticMarketRefreshRetainsVerifiedContentAndThrottlesFailure(t *testing.T) {
	m, catalog, key, _ := mirrorFixture(t)
	signed := signCatalog(t, catalog, key)
	m.client = &http.Client{Transport: marketTransport(func(*http.Request) (*http.Response, error) {
		return catalogResponse(signed), nil
	})}
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	m.client = &http.Client{Transport: marketTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("offline")
	})}
	if err := m.Refresh(t.Context()); err == nil {
		t.Fatal("failed refresh claimed success")
	}
	if err := m.RefreshIfStale(t.Context(), 30*time.Second); err == nil {
		t.Fatal("recent failure was silently presented as fresh content")
	}
	view := m.View()
	if calls.Load() != 2 || len(view.Plugins) != 1 || view.Sequence != catalog.Sequence || view.Error == "" {
		t.Fatalf("failure retry or trusted content loss: calls=%d view=%+v", calls.Load(), view)
	}
}

func TestAutomaticMarketRefreshChecksAnExpiredCatalogImmediately(t *testing.T) {
	m, catalog, key, _ := mirrorFixture(t)
	signed := signCatalog(t, catalog, key)
	var calls atomic.Int32
	m.client = &http.Client{Transport: marketTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return catalogResponse(signed), nil
	})}
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.view.Expires = time.Now().Add(-time.Second)
	m.mu.Unlock()
	if err := m.RefreshIfStale(t.Context(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 || !m.View().Expires.After(time.Now()) {
		t.Fatal("an expired catalog was served from the automatic-check cache")
	}
}
