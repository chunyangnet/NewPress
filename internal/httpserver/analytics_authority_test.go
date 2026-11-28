package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/analytics"
	"newpress.local/newpress/internal/auth"
)

type analyticsAuthorityStore struct {
	analytics.Store
	after func()
	fail  bool
	wait  func(context.Context)
}

func (s *analyticsAuthorityStore) Snapshot(ctx context.Context, now time.Time) (analytics.State, error) {
	state, err := s.Store.Snapshot(ctx, now)
	if s.wait != nil {
		s.wait(ctx)
		if err := ctx.Err(); err != nil {
			return analytics.State{}, err
		}
	}
	runAutosaveBoundary(&s.after)
	if s.fail {
		return analytics.State{}, errors.New("PRIVATE-ANALYTICS-STORE-DETAIL")
	}
	return state, err
}

func analyticsAuthorityRequest(action string, cookie *http.Cookie, ctx context.Context) *http.Request {
	path := "/api/v1/admin/analytics?days=7"
	if action == "page" {
		path = "/admin/analytics/?days=7"
	}
	r := httptest.NewRequest("GET", "https://example.test"+path, nil).WithContext(ctx)
	r.AddCookie(cookie)
	return r
}

func TestAnalyticsAuthorityReadBoundaries(t *testing.T) {
	for _, boundary := range []string{"api", "page", "navigation", "recovery"} {
		for _, change := range []string{"logout", "fallback", "permission", "module-restarted", "outage", "cancel", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				_, id := workbenchSourceFixture(t, s)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var changed atomic.Bool
				after := func() {
					changed.Store(true)
					aiUsageChange(t, s, manager, users, fault, cookie, change, "analytics", cancel)
				}
				store := &analyticsAuthorityStore{Store: workbenchSourceAnalytics{id: id}}
				action := "page"
				if boundary == "api" {
					action = "api"
				}
				if boundary == "navigation" || boundary == "recovery" {
					s.navigation = &navigationAuthorityStore{Store: s.navigation, afterSnapshot: after}
					store.fail = boundary == "recovery"
				} else {
					store.after = after
				}
				s.SetAnalytics(analytics.NewCollector(store))
				r := analyticsAuthorityRequest(action, cookie, ctx)
				if change == "fallback" {
					token, _, err := manager.AuthenticateContext(t.Context(), "analytics-fallback", actor.Username, "Account password for "+actor.Username+" 2026")
					if err != nil {
						t.Fatal(err)
					}
					r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
				}
				w, aborted := commentAuthorityServe(s.routes(), r)
				if !changed.Load() {
					t.Fatal("read boundary missed")
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancelled analytics disclosed data", aborted, w.Code)
					}
					return
				}
				want := map[string]int{"logout": 401, "fallback": 401, "permission": 403, "module-restarted": 409, "outage": 503, "allowed": 200}[change]
				if action == "page" && want == 401 {
					want = 303
				}
				if boundary == "recovery" && change == "allowed" {
					want = 503
				}
				if aborted || w.Code != want || change != "allowed" && (strings.Contains(w.Body.String(), "SOURCE-ARTICLE") || strings.Contains(w.Body.String(), "data-feature-recovery")) || strings.Contains(w.Body.String(), "PRIVATE-") {
					t.Fatal("analytics authority bypass", aborted, w.Code, want)
				}
			})
		}
	}
}

func TestAnalyticsAuthorityFinalOutput(t *testing.T) {
	for _, action := range []string{"api", "page"} {
		for _, change := range []string{"logout", "module", "source"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, manager, _, cookie, _ := newPersistentAdminTestServer(t)
				repo, _ := workbenchSourceFixture(t, s)
				recorder := httptest.NewRecorder()
				w := &aiEditorAuthorityHeaderWriter{ResponseWriter: recorder, after: func() {
					switch change {
					case "logout":
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Error(err)
						}
					case "module":
						accountAuthorityModule(t, s, "analytics", false)
						accountAuthorityModule(t, s, "analytics", true)
					case "source":
						repo.change.Store(1)
					}
				}}
				aborted := serveAIGenerationBoundary(s.routes(), w, analyticsAuthorityRequest(action, cookie, t.Context()))
				if !aborted || recorder.Body.Len() != 0 {
					t.Fatal("private report sent after header revocation", aborted, recorder.Body.Len())
				}
			})
		}
	}
}

func TestAnalyticsAuthoritySilentWait(t *testing.T) {
	for _, change := range []string{"logout", "permission", "module-restarted", "outage", "cancel"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			manager, users, sessions, cookie, _ := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead})
			fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
			sessions.SessionStore = fault
			_, id := workbenchSourceFixture(t, s)
			entered, finished := make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var once sync.Once
			s.SetAnalytics(analytics.NewCollector(&analyticsAuthorityStore{Store: workbenchSourceAnalytics{id: id}, wait: func(ctx context.Context) { once.Do(func() { close(entered) }); <-ctx.Done() }}))
			var w *httptest.ResponseRecorder
			var aborted bool
			go func() {
				defer close(finished)
				w, aborted = commentAuthorityServe(s.routes(), analyticsAuthorityRequest("api", cookie, ctx))
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("snapshot did not start")
			}
			aiUsageChange(t, s, manager, users, fault, cookie, change, "analytics", cancel)
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("snapshot ignored cancellation")
			}
			if change == "cancel" {
				if !aborted || w.Body.Len() != 0 {
					t.Fatal("cancel output")
				}
				return
			}
			want := map[string]int{"logout": 401, "permission": 403, "module-restarted": 409, "outage": 503}[change]
			if aborted || w.Code != want || strings.Contains(w.Body.String(), "SOURCE-ARTICLE") {
				t.Fatal("wrong silent wait result", aborted, w.Code, want)
			}
		})
	}
}

func TestAnalyticsSourceResponse(t *testing.T) {
	for _, action := range []string{"api", "page"} {
		for _, change := range []int32{1, 2, 3, 4} {
			t.Run(action+"/"+jsonNumber(int64(change)), func(t *testing.T) {
				t.Parallel()
				s, _, _, _, cookie, _ := newPersistentAdminTestServer(t)
				repo, _ := workbenchSourceFixture(t, s)
				repo.afterRead = func() { repo.change.Store(change) }
				w, aborted := commentAuthorityServe(s.routes(), analyticsAuthorityRequest(action, cookie, t.Context()))
				want, code := 409, "analytics.source_changed"
				if change == 4 {
					want, code = 503, "analytics.source_unavailable"
				}
				if aborted || w.Code != want || strings.Contains(w.Body.String(), "SOURCE-ARTICLE") || strings.Contains(w.Body.String(), "PRIVATE-") {
					t.Fatal("stale source response", aborted, w.Code, want)
				}
				if action == "api" && !strings.Contains(w.Body.String(), code) {
					t.Fatal("wrong source error code", w.Body.String())
				}
			})
		}
	}
}

func TestAnalyticsAuthorityIndependentModule(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	_, _, _, cookie, _ := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead})
	workbenchSourceFixture(t, s)
	accountAuthorityModule(t, s, "ai", false)
	for _, action := range []string{"api", "page"} {
		w, aborted := commentAuthorityServe(s.routes(), analyticsAuthorityRequest(action, cookie, t.Context()))
		if aborted || w.Code != 200 {
			t.Fatal("analytics depends on AI or write access", aborted, w.Code)
		}
	}
}
