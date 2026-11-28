package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

type statusAuthorityRepository struct {
	content.Repository
	ready func(context.Context) error
}

func (r *statusAuthorityRepository) Ready(ctx context.Context) error { return r.ready(ctx) }

func statusAuthorityRequest(action string, cookie *http.Cookie, ctx context.Context) *http.Request {
	path := "/api/v1/admin/system-status"
	if action == "page" {
		path = "/admin/system/"
	}
	r := httptest.NewRequest("GET", "https://example.test"+path, nil).WithContext(ctx)
	r.AddCookie(cookie)
	return r
}

func TestSystemStatusAuthorityReadBoundaries(t *testing.T) {
	for _, boundary := range []string{"probe-api", "probe-page", "navigation", "summary"} {
		for _, change := range []string{"logout", "fallback", "permission", "outage", "cancel", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				s.config.Address = "PRIVATE-SYSTEM-LISTEN"
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var changed atomic.Bool
				after := func() { changed.Store(true); aiUsageChange(t, s, manager, users, fault, cookie, change, "", cancel) }
				action := "page"
				if boundary == "probe-api" {
					action = "api"
				}
				switch boundary {
				case "navigation":
					s.navigation = &navigationAuthorityStore{Store: s.navigation, afterSnapshot: after}
				case "summary":
					s.comments = &accountAuthorityComments{Store: s.comments, afterSnapshot: after}
				default:
					s.repository = &statusAuthorityRepository{Repository: s.repository, ready: func(context.Context) error { after(); return nil }}
				}
				r := statusAuthorityRequest(action, cookie, ctx)
				if change == "fallback" {
					token, _, err := manager.AuthenticateContext(t.Context(), "status-fallback", actor.Username, "Account password for "+actor.Username+" 2026")
					if err != nil {
						t.Fatal(err)
					}
					r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
				}
				w, aborted := commentAuthorityServe(s.routes(), r)
				if !changed.Load() {
					t.Fatal("boundary not reached")
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancelled status disclosed data", aborted, w.Code)
					}
					return
				}
				want := map[string]int{"logout": 401, "fallback": 401, "permission": 403, "outage": 503, "allowed": 200}[change]
				if action == "page" && want == 401 {
					want = 303
				}
				if aborted || w.Code != want || strings.Contains(w.Body.String(), "PRIVATE-SYSTEM-LISTEN") != (change == "allowed") || strings.Contains(w.Body.String(), "PRIVATE-SESSION-DETAIL") {
					t.Fatal("system status escaped authority", aborted, w.Code, want)
				}
			})
		}
	}
}

func TestSystemStatusAuthorityCachedOutput(t *testing.T) {
	for _, action := range []string{"api", "page"} {
		for _, cache := range []bool{false, true} {
			t.Run(action+"/cache="+map[bool]string{true: "yes", false: "no"}[cache], func(t *testing.T) {
				t.Parallel()
				s, _, manager, _, cookie, _ := newPersistentAdminTestServer(t)
				if cache {
					s.systemStatusReport(t.Context())
				}
				recorder := httptest.NewRecorder()
				w := &aiEditorAuthorityHeaderWriter{ResponseWriter: recorder, after: func() {
					if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Error(err)
					}
				}}
				aborted := serveAIGenerationBoundary(s.routes(), w, statusAuthorityRequest(action, cookie, t.Context()))
				if !aborted || recorder.Body.Len() != 0 {
					t.Fatal("cached/fresh status escaped final check", aborted, recorder.Body.Len())
				}
			})
		}
	}
}

func TestSystemStatusAuthoritySilentWait(t *testing.T) {
	for _, change := range []string{"logout", "permission", "outage", "cancel"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			manager, users, sessions, cookie, _ := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead})
			fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
			sessions.SessionStore = fault
			entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s.repository = &statusAuthorityRepository{Repository: s.repository, ready: func(context.Context) error { close(entered); <-release; return nil }}
			defer func() { close(release); <-finished }()
			var w *httptest.ResponseRecorder
			var aborted bool
			go func() {
				defer close(finished)
				w, aborted = commentAuthorityServe(s.routes(), statusAuthorityRequest("api", cookie, ctx))
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("probe not started")
			}
			aiUsageChange(t, s, manager, users, fault, cookie, change, "", cancel)
			select {
			case <-finished:
			case <-time.After(1500 * time.Millisecond):
				t.Error("caller stuck behind native probe")
				return
			}
			if change == "cancel" {
				if !aborted || w.Body.Len() != 0 {
					t.Fatal("cancel output")
				}
				return
			}
			want := map[string]int{"logout": 401, "permission": 403, "outage": 503}[change]
			if aborted || w.Code != want {
				t.Fatal("silent revocation response", aborted, w.Code, want)
			}
		})
	}
}

func TestSystemStatusAuthorityIndependentModules(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	_, _, _, cookie, _ := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead})
	accountAuthorityModule(t, s, "ai", false)
	accountAuthorityModule(t, s, "analytics", false)
	for _, action := range []string{"api", "page"} {
		w, aborted := commentAuthorityServe(s.routes(), statusAuthorityRequest(action, cookie, t.Context()))
		if aborted || w.Code != 200 {
			t.Fatal("system diagnostics require unrelated module", aborted, w.Code)
		}
	}
}

func TestSystemStatusPageProbeBudget(t *testing.T) {
	s, _, _, _, cookie, _ := newPersistentAdminTestServer(t)
	var calls atomic.Int32
	s.repository = &statusAuthorityRepository{Repository: s.repository, ready: func(context.Context) error { calls.Add(1); return nil }}
	for range 2 {
		w, aborted := commentAuthorityServe(s.routes(), statusAuthorityRequest("page", cookie, t.Context()))
		if aborted || w.Code != 200 {
			t.Fatal("page failed", aborted, w.Code)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("page summary bypassed shared probe cache", calls.Load())
	}
}

func TestSystemStatusCollectionTimeoutResponse(t *testing.T) {
	for _, action := range []string{"api", "page"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, cookie, _ := newPersistentAdminTestServer(t)
			release := make(chan struct{})
			s.repository = &statusAuthorityRepository{Repository: s.repository, ready: func(context.Context) error { <-release; return nil }}
			defer func() { close(release); waitStatusPending(t, s, -1) }()
			w, aborted := commentAuthorityServe(s.routes(), statusAuthorityRequest(action, cookie, t.Context()))
			if aborted || w.Code != 503 || strings.Contains(w.Body.String(), "heap_alloc_bytes") {
				t.Fatal("collection timeout exposed partial report", aborted, w.Code)
			}
			if action == "api" && !strings.Contains(w.Body.String(), "system_status.unavailable") {
				t.Fatal("wrong timeout code", w.Body.String())
			}
		})
	}
}

func TestSystemStatusWorkbenchModuleWait(t *testing.T) {
	s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.repository = &statusAuthorityRepository{Repository: s.repository, ready: func(context.Context) error { close(entered); <-release; return nil }}
	defer func() { close(release); <-finished; waitStatusPending(t, s, -1) }()
	var w *httptest.ResponseRecorder
	var aborted bool
	go func() {
		defer close(finished)
		w, aborted = commentAuthorityServe(s.routes(), workbenchAuthorityRequest("preview", cookie, actor, t.Context(), `{"task":"operations","days":7}`))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("operations did not enter monitor")
	}
	accountAuthorityModule(t, s, "ai", false)
	accountAuthorityModule(t, s, "ai", true)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("operations preview waited behind cancelled monitor")
	}
	if aborted || w.Code != 409 || !strings.Contains(w.Body.String(), "ai.inactive") {
		t.Fatal("operations ignored module lease", aborted, w.Code)
	}
}
