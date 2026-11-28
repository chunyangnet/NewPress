package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/navigation"
)

type navigationReadContextKey struct{}

type navigationReadStore struct {
	navigation.Store
	snapshot navigation.Snapshot
	ctx      context.Context
	calls    int
	budget   time.Duration
	after    func(context.Context)
	err      error
}

func (s *navigationReadStore) read(ctx context.Context) {
	s.calls++
	s.ctx = ctx
	if deadline, ok := ctx.Deadline(); ok {
		s.budget = time.Until(deadline)
	}
	if s.after != nil {
		s.after(ctx)
	}
}

func (s *navigationReadStore) Snapshot(ctx context.Context) (navigation.Snapshot, error) {
	s.read(ctx)
	return s.snapshot, s.err
}

func (s *navigationReadStore) GetLocation(ctx context.Context, _ string) (navigation.Menu, error) {
	s.read(ctx)
	return s.snapshot.Menus[0], s.err
}

func navigationReadSnapshot() navigation.Snapshot {
	return navigation.Snapshot{
		Menus: []navigation.Menu{{ID: 41, Version: 2, Name: "Read fixture", Slug: "read-fixture", Locations: []string{navigation.LocationPrimary},
			UpdatedAt: time.Unix(1700000000, 0).UTC(), Items: []navigation.Item{{ID: 51, MenuID: 41, Label: "NAV-READ-MARKER", URL: "/explore/?category=Alias", Accent: "#123456"}}}},
		Locations: map[string]int64{navigation.LocationPrimary: 41}, MenuCount: 1, ItemCount: 1,
	}
}

type navigationTermsRepository struct {
	content.Repository
	calls int
	ctx   context.Context
	after func(context.Context)
	err   error
	empty bool
}

func (r *navigationTermsRepository) ListTerms(ctx context.Context, _ content.TermQuery) (content.TermList, error) {
	r.calls++
	r.ctx = ctx
	if r.after != nil {
		r.after(ctx)
	}
	if r.empty {
		return content.TermList{}, r.err
	}
	return content.TermList{Items: []content.Term{{ID: 1, Taxonomy: content.TaxonomyCategory, Slug: "canonical", Name: "Alias", Count: 1}}}, r.err
}

func TestPublicNavigationReadContext(t *testing.T) {
	for _, change := range []string{"allowed", "cancel-before", "cancel-snapshot", "cancel-terms", "deadline-snapshot", "deadline-terms", "snapshot-error", "terms-error"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, _, backing, _, _ := newNavigationHTTPTestServer(t)
			store := &navigationReadStore{Store: backing, snapshot: navigationReadSnapshot()}
			terms := &navigationTermsRepository{Repository: s.repository}
			s.navigation, s.repository = store, terms
			ctx, cancel := context.WithCancel(context.WithValue(t.Context(), navigationReadContextKey{}, "request-value"))
			defer cancel()
			switch change {
			case "cancel-before":
				cancel()
			case "cancel-snapshot":
				store.after = func(context.Context) { cancel() }
			case "cancel-terms":
				terms.after = func(context.Context) { cancel() }
			case "deadline-snapshot", "deadline-terms":
				late := func(ctx context.Context) { <-ctx.Done() }
				if change == "deadline-snapshot" {
					store.after = late
				} else {
					terms.after = late
				}
			case "snapshot-error":
				store.err = errors.New("PRIVATE-NAV-READ-DETAIL")
			case "terms-error":
				terms.err = errors.New("PRIVATE-TERMS-READ-DETAIL")
			}
			view := s.baseViewContext(ctx, "Navigation", "", "/explore/?category=canonical").Navigation
			if change == "cancel-before" {
				if store.calls != 0 || terms.calls != 0 {
					t.Fatal("cancelled view started reads", store.calls, terms.calls)
				}
				return
			}
			if store.calls != 1 || store.ctx.Value(navigationReadContextKey{}) != "request-value" || store.budget <= 0 || store.budget > 350*time.Millisecond {
				t.Error("navigation lost request scope or budget", store.calls, store.budget, store.ctx.Value(navigationReadContextKey{}))
			}
			wantTerms := 1
			if change == "cancel-snapshot" || change == "deadline-snapshot" || change == "snapshot-error" {
				wantTerms = 0
			}
			if terms.calls != wantTerms || terms.calls > 0 && terms.ctx.Value(navigationReadContextKey{}) != "request-value" {
				t.Error("invalid extra category read", terms.calls, wantTerms)
			}
			visible := change == "allowed" || change == "terms-error"
			if (len(view.Primary) == 1) != visible || !visible && view.CSSURL != "" {
				t.Error("failed or expired read reached navigation", view.Primary, view.CSSURL)
			}
			if change == "allowed" && (len(view.Primary) != 1 || !view.Primary[0].Current) {
				t.Fatal("category alias lost current menu selection")
			}
		})
	}
}

func TestPublicNavigationReadHonorsShortDeadline(t *testing.T) {
	s, _, backing, _, _ := newNavigationHTTPTestServer(t)
	store := &navigationReadStore{Store: backing, snapshot: navigationReadSnapshot()}
	s.navigation = store
	deadline := time.Now().Add(150 * time.Millisecond)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	store.after = func(ctx context.Context) { <-ctx.Done() }
	view := s.baseViewContext(ctx, "Navigation", "", "/").Navigation
	if store.calls == 0 {
		if time.Now().Before(deadline) || len(view.Primary) != 0 {
			t.Fatal("live navigation skipped its read")
		}
		return
	}
	got, ok := store.ctx.Deadline()
	if !ok || got.After(deadline) || len(view.Primary) != 0 {
		t.Fatal("navigation extended request deadline or retained late data", got, deadline, len(view.Primary))
	}
}

func TestPublicNavigationReadCancellationInterruptsStore(t *testing.T) {
	s, _, backing, _, _ := newNavigationHTTPTestServer(t)
	store := &navigationReadStore{Store: backing, snapshot: navigationReadSnapshot()}
	s.navigation = store
	ctx, cancel := context.WithCancel(t.Context())
	started, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	store.after = func(ctx context.Context) {
		close(started)
		select {
		case <-ctx.Done():
		case <-stop:
		}
	}
	var view navigationView
	go func() {
		defer close(done)
		view = s.baseViewContext(ctx, "Navigation", "", "/").Navigation
	}()
	defer func() { cancel(); close(stop); <-done }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("navigation read did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("navigation read did not stop")
	}
	if !errors.Is(store.ctx.Err(), context.Canceled) || len(view.Primary) != 0 {
		t.Fatal("store waited for its own deadline instead of request cancellation", store.ctx.Err(), len(view.Primary))
	}
}

func TestPublicNavigationCategoryReadsOnlyWhenUsed(t *testing.T) {
	for _, kind := range []string{"home", "search", "empty", "unassigned", "no-category-link", "external", "fragment", "blocked-parent", "category"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, _, backing, _, _ := newNavigationHTTPTestServer(t)
			snapshot := navigationReadSnapshot()
			path := "/explore/?category=canonical"
			switch kind {
			case "home":
				path = "/"
			case "search":
				path = "/explore/?q=canonical"
			case "empty":
				snapshot = navigation.Snapshot{}
			case "unassigned":
				snapshot.Locations = nil
				snapshot.Menus[0].Locations = nil
			case "no-category-link":
				snapshot.Menus[0].Items[0].URL = "/explore/"
			case "external":
				snapshot.Menus[0].Items[0].URL = "https://external.example/explore/?category=Alias"
			case "fragment":
				snapshot.Menus[0].Items[0].URL += "#section"
			case "blocked-parent":
				snapshot.Menus[0].Items[0].ParentID = 52
				snapshot.Menus[0].Items = append(snapshot.Menus[0].Items, navigation.Item{ID: 52, URL: "/admin/", Label: "private parent"})
			}
			s.navigation = &navigationReadStore{Store: backing, snapshot: snapshot}
			terms := &navigationTermsRepository{Repository: s.repository}
			s.repository = terms
			view := s.baseViewContext(t.Context(), "Navigation", "", path).Navigation
			want := 0
			if kind == "category" {
				want = 1
			}
			if terms.calls != want {
				t.Fatal("unused category lookup", terms.calls, want)
			}
			if kind == "category" && (len(view.Primary) != 1 || !view.Primary[0].Current) {
				t.Fatal("needed alias was not resolved")
			}
		})
	}
}

func TestPublicNavigationExploreReusesCategories(t *testing.T) {
	s, _, backing, _, _ := newNavigationHTTPTestServer(t)
	s.navigation = &navigationReadStore{Store: backing, snapshot: navigationReadSnapshot()}
	terms := &navigationTermsRepository{Repository: s.repository}
	s.repository = terms
	for _, row := range []struct {
		category       string
		empty, current bool
	}{{"canonical", false, true}, {"canonical", true, false}, {"Alias", false, true}} {
		terms.calls = 0
		terms.empty = row.empty
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/explore/?category="+url.QueryEscape(row.category), nil)
		s.routes().ServeHTTP(w, r)
		if w.Code != 200 || terms.calls != 1 || !strings.Contains(w.Body.String(), "NAV-READ-MARKER") || strings.Contains(w.Body.String(), `href="/explore/?category=Alias" aria-current="page"`) != row.current {
			t.Fatal("explore duplicated or retained category snapshot, or lost alias selection", row, w.Code, terms.calls)
		}
	}
}

func TestPublicNavigationEndpointReadBoundaries(t *testing.T) {
	for _, surface := range []string{"api", "css"} {
		for _, change := range []string{"allowed", "cached", "error", "missing", "cancel-before", "cancel-after", "cancel-error", "deadline-before", "read-deadline", "deadline-error"} {
			t.Run(surface+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, backing, _, _ := newNavigationHTTPTestServer(t)
				store := &navigationReadStore{Store: backing, snapshot: navigationReadSnapshot()}
				s.navigation = store
				ctx, cancel := context.WithCancel(context.WithValue(t.Context(), navigationReadContextKey{}, "request-value"))
				defer cancel()
				switch change {
				case "cancel-before":
					cancel()
				case "deadline-before":
					var end context.CancelFunc
					ctx, end = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer end()
				case "cancel-after", "cancel-error":
					store.after = func(context.Context) { cancel() }
					if change == "cancel-error" {
						store.err = navigation.ErrNotFound
					}
				case "error":
					store.err = errors.New("PRIVATE-NAV-READ-DETAIL")
				case "missing":
					store.err = navigation.ErrNotFound
				case "read-deadline", "deadline-error":
					store.after = func(ctx context.Context) {
						if store.budget > 0 && store.budget <= time.Second {
							<-ctx.Done()
						}
					}
					if change == "deadline-error" {
						store.err = navigation.ErrNotFound
					}
				}
				path, handle := "/navigation.css", s.navigationCSS
				etag := `"navigation-` + navigationSnapshotRevision(store.snapshot) + `"`
				if surface == "api" {
					path, handle = "/api/v1/navigation/primary", s.publicNavigationLocationAPI
					etag = navigationLocationAPIETag(navigation.LocationPrimary, store.snapshot.Menus[0])
				}
				r := httptest.NewRequest("GET", path, nil).WithContext(ctx)
				r.SetPathValue("location", navigation.LocationPrimary)
				if change == "cached" || change == "cancel-after" || change == "read-deadline" {
					r.Header.Set("If-None-Match", etag)
				}
				w, aborted := commentAuthorityServe(http.HandlerFunc(handle), r)
				if ctx.Err() != nil {
					want := 0
					if change == "cancel-after" || change == "cancel-error" {
						want = 1
					}
					if !aborted || w.Body.Len() != 0 || store.calls != want || w.Header().Get("ETag") != "" {
						t.Fatal("cancelled endpoint read or emitted navigation", aborted, w.Code, store.calls, want)
					}
					return
				}
				want := 200
				if change == "cached" {
					want = 304
				} else if change == "read-deadline" || change == "deadline-error" || change == "error" || change == "missing" {
					want = 503
					if surface == "api" && change == "error" {
						want = 500
					} else if surface == "api" && change == "missing" {
						want = 404
					}
				}
				if aborted || w.Code != want || store.calls != 1 || store.ctx.Value(navigationReadContextKey{}) != "request-value" || store.budget <= 0 || store.budget > 350*time.Millisecond {
					t.Fatal("endpoint read lost budget or response semantics", aborted, w.Code, want, store.calls, store.budget)
				}
				if want >= 400 && (w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "NAV-READ-MARKER") || strings.Contains(w.Body.String(), "#123456") || strings.Contains(w.Body.String(), "PRIVATE-NAV-READ-DETAIL")) {
					t.Fatal("failed read emitted cacheable navigation or storage details", w.Header(), w.Body.String())
				}
				if want == 304 && w.Body.Len() != 0 {
					t.Fatal("conditional read emitted a body")
				}
			})
		}
	}
}

func TestPublicNavigationProjectionPreservesSource(t *testing.T) {
	s, _, backing, _, _ := newNavigationHTTPTestServer(t)
	snapshot := navigationReadSnapshot()
	snapshot.Menus[0].Items = append(snapshot.Menus[0].Items, navigation.Item{ID: 52, URL: "/admin/", Label: "PRIVATE-NAV-ENTRY"})
	want := append([]navigation.Item(nil), snapshot.Menus[0].Items...)
	s.navigation = &navigationReadStore{Store: backing, snapshot: snapshot}
	view := s.baseViewContext(t.Context(), "Navigation", "", "/").Navigation
	if len(view.Primary) != 1 || !reflect.DeepEqual(snapshot.Menus[0].Items, want) {
		t.Fatal("public projection mutated source or retained private entry")
	}
}
