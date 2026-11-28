package httpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
)

func aiUsageBalanceSettings(t *testing.T, s *Server) {
	t.Helper()
	aiAdminAuthoritySettings(t, s)
	settings := s.aiSettings.Snapshot()
	config := aieditor.DefaultTelemetryConfig()
	config.Balances = true
	settings.Telemetry = &config
	settings.Providers[0].Balance = aieditor.BalanceConfig{Enabled: true, Format: "sub2api", CacheSeconds: 60}
	if _, err := s.aiSettings.Save(settings); err != nil {
		t.Fatal(err)
	}
}

func aiUsageTestRequest(action string, cookie *http.Cookie, actor auth.Principal, ctx context.Context) *http.Request {
	if action == "balance" {
		return aiAdminAuthorityRequest("POST", "balance", cookie, actor, ctx, `{"provider_id":"authority"}`)
	}
	if action == "usage" {
		return aiAdminAuthorityRequest("GET", "usage", cookie, actor, ctx, "")
	}
	path := "/admin/analytics/ai/"
	if action == "requests-page" {
		path += "requests/"
	}
	r := httptest.NewRequest("GET", "https://example.test"+path, nil).WithContext(ctx)
	r.AddCookie(cookie)
	return r
}

func aiUsageTestTelemetry(t *testing.T, s *Server, after func()) {
	t.Helper()
	var armed bool
	var once sync.Once
	store, err := aieditor.NewTelemetryStore("", func() aieditor.TelemetryConfig {
		if armed && after != nil {
			once.Do(after)
		}
		return aieditor.DefaultTelemetryConfig()
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	store.Record(aieditor.RequestRecord{At: now, Day: now.In(s.siteLocation()).Format("2006-01-02"), ProviderID: "authority", Provider: aiAdminAuthorityMarker, Model: aiAdminAuthorityMarker, Kind: "text", Status: "success"})
	s.SetAITelemetry(store)
	armed = true
}

func aiUsageChange(t *testing.T, s *Server, manager *auth.Manager, users *auth.FileUserStore, fault *aiGenerationFaultStore, cookie *http.Cookie, change, module string, cancel context.CancelFunc) {
	t.Helper()
	switch change {
	case "logout", "fallback":
		if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
			t.Error(err)
		}
	case "permission":
		if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: []string{auth.CapabilityAdminAccess}}}); err != nil {
			t.Error(err)
		}
	case "module-restarted":
		accountAuthorityModule(t, s, module, false)
		accountAuthorityModule(t, s, module, true)
	case "outage":
		fault.fail.Store(true)
	case "cancel":
		cancel()
	}
}

func TestAIUsageAuthorityReadAndBalance(t *testing.T) {
	for _, boundary := range []string{"report", "page-navigation", "page-summary", "balance-body", "balance-upstream", "balance-completion", "balance-cache"} {
		for _, change := range []string{"logout", "fallback", "permission", "module-restarted", "outage", "cancel", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead, auth.CapabilitySettingsManage})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				aiUsageBalanceSettings(t, s)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				module, action := "analytics", "usage"
				if strings.HasPrefix(boundary, "balance") {
					module, action = "ai", "balance"
				} else if strings.HasPrefix(boundary, "page") {
					action = "usage-page"
				}
				var changed atomic.Bool
				after := func() {
					changed.Store(true)
					aiUsageChange(t, s, manager, users, fault, cookie, change, module, cancel)
				}
				if boundary == "report" {
					aiUsageTestTelemetry(t, s, after)
				} else if boundary == "page-navigation" {
					s.navigation = &navigationAuthorityStore{Store: s.navigation, afterSnapshot: after}
				} else if boundary == "page-summary" {
					s.comments = &accountAuthorityComments{Store: s.comments, afterSnapshot: after}
				}
				var calls atomic.Int32
				s.aiBalances.HTTP = &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					if boundary == "balance-upstream" {
						after()
					}
					body := &aiGenerationTrackedBody{ReadCloser: io.NopCloser(strings.NewReader(`{"remaining":987654.25,"unit":"USD"}`))}
					if boundary == "balance-completion" {
						body.afterClose = after
					}
					return &http.Response{StatusCode: 200, Body: body}, nil
				})}
				if boundary == "balance-cache" {
					if _, err := s.aiBalances.Query(t.Context(), s.aiSettings, "authority"); err != nil {
						t.Fatal(err)
					}
				}
				r := aiUsageTestRequest(action, cookie, actor, ctx)
				if change == "fallback" {
					token, _, err := manager.AuthenticateContext(t.Context(), "usage-fallback", actor.Username, "Account password for "+actor.Username+" 2026")
					if err != nil {
						t.Fatal(err)
					}
					r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
				}
				if boundary == "balance-body" || boundary == "balance-cache" {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
				}
				w, aborted := commentAuthorityServe(s.routes(), r)
				if !changed.Load() {
					t.Fatal("authority boundary not exercised")
				}
				wantCalls := int32(0)
				if action == "balance" && (boundary != "balance-body" || change == "allowed") {
					wantCalls = 1
				}
				if calls.Load() != wantCalls {
					t.Error("unauthorized or repeated balance probe", calls.Load(), wantCalls)
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancelled request returned data", aborted, w.Code)
					}
					return
				}
				want := map[string]int{"logout": 401, "fallback": 401, "permission": 403, "module-restarted": 409, "outage": 503, "allowed": 200}[change]
				marker := aiAdminAuthorityMarker
				if action == "balance" {
					marker = "987654.25"
				} else if action == "usage-page" {
					marker = "data-ai-usage"
					if want == 401 {
						want = 303
					}
				}
				if aborted || w.Code != want || strings.Contains(w.Body.String(), marker) != (change == "allowed") || strings.Contains(w.Body.String(), "PRIVATE-SESSION-DETAIL") {
					t.Fatal("usage/balance result escaped authority", aborted, w.Code, want, w.Body.String()[:min(200, w.Body.Len())])
				}
			})
		}
	}
}

func TestAIUsageAuthorityFinalOutput(t *testing.T) {
	for _, action := range []string{"usage", "usage-page", "requests-page", "balance"} {
		for _, change := range []string{"logout", "module-restarted"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, manager, _, cookie, actor := newPersistentAdminTestServer(t)
				aiUsageBalanceSettings(t, s)
				aiUsageTestTelemetry(t, s, nil)
				s.aiBalances.HTTP = &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"remaining":987654.25,"unit":"USD"}`))}, nil
				})}
				changed := false
				recorder := httptest.NewRecorder()
				w := &aiEditorAuthorityHeaderWriter{ResponseWriter: recorder, after: func() {
					changed = true
					if change == "logout" {
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Fatal(err)
						}
					} else {
						module := "analytics"
						if action == "balance" {
							module = "ai"
						}
						accountAuthorityModule(t, s, module, false)
						accountAuthorityModule(t, s, module, true)
					}
				}}
				aborted := serveAIGenerationBoundary(s.routes(), w, aiUsageTestRequest(action, cookie, actor, t.Context()))
				if !changed || !aborted || recorder.Body.Len() != 0 {
					t.Fatal("revocation after headers disclosed usage/balance", changed, aborted, recorder.Body.Len())
				}
			})
		}
	}
}

func TestAIUsageAuthorityModuleAndCapabilities(t *testing.T) {
	for _, mode := range []string{"analytics-only", "ai-only", "system-reader", "settings-manager", "no-admin", "loopback"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
			aiUsageBalanceSettings(t, s)
			aiUsageTestTelemetry(t, s, nil)
			s.aiBalances.HTTP = &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"remaining":20,"unit":"USD"}`))}, nil
			})}
			switch mode {
			case "analytics-only":
				accountAuthorityModule(t, s, "ai", false)
			case "ai-only":
				accountAuthorityModule(t, s, "analytics", false)
			case "loopback":
				s.auth, _ = auth.New(auth.Config{})
			default:
				caps := []string{auth.CapabilityAdminAccess}
				if mode == "system-reader" {
					caps = append(caps, auth.CapabilitySystemRead)
				} else if mode == "settings-manager" {
					caps = append(caps, auth.CapabilitySettingsManage)
				} else {
					caps = []string{auth.CapabilitySystemRead, auth.CapabilitySettingsManage}
				}
				_, _, _, cookie, actor = dashboardAuthorityIdentity(t, s, caps)
			}
			for _, action := range []string{"usage", "usage-page", "requests-page", "balance"} {
				r := aiUsageTestRequest(action, cookie, actor, t.Context())
				if mode == "loopback" {
					r.RemoteAddr = "127.0.0.1:1234"
				}
				w, aborted := commentAuthorityServe(s.routes(), r)
				want := 200
				if mode == "no-admin" || mode == "settings-manager" && action != "balance" || (mode == "system-reader" || mode == "loopback") && action == "balance" {
					want = 403
				}
				if mode == "analytics-only" && action == "balance" || mode == "ai-only" && action != "balance" {
					want = 404
				}
				if aborted || w.Code != want {
					t.Fatal("module/capability behavior changed", action, aborted, w.Code, want)
				}
			}
		})
	}
}
