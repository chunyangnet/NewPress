package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
)

type diagnosticAuditReader struct {
	after func()
	err   error
}

// Recovery pages deliberately use 404/503 as well as 200. The hook must cover
// every selected response, including a diagnostic response to a failed service.
type diagnosticHeaderWriter struct {
	http.ResponseWriter
	after func()
}

func (w *diagnosticHeaderWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	runAutosaveBoundary(&w.after)
}

func (r diagnosticAuditReader) List(context.Context, audit.Query) (audit.Page, error) {
	if r.after != nil {
		r.after()
	}
	return audit.Page{Total: 1, Events: []audit.Entry{{ID: 1, CreatedAt: time.Now().UTC(), Action: "comment.edit", ObjectType: "comment", ObjectID: "PRIVATE-DIAGNOSTIC-OBJECT", Actor: "operator"}}}, r.err
}

func diagnosticRequest(path string, cookie *http.Cookie, ctx context.Context) *http.Request {
	r := httptest.NewRequest("GET", "https://example.test"+path, nil).WithContext(ctx)
	r.AddCookie(cookie)
	return r
}

func diagnosticRecoveryFixture(t *testing.T, s *Server, kind string) (path string, status int, capability string) {
	t.Helper()
	switch kind {
	case "paused":
		accountAuthorityModule(t, s, "wechat", false)
		return "/admin/wechat/", 200, auth.CapabilitySettingsManage
	case "missing":
		s.currency, s.currencyCatalog = nil, nil
		return "/admin/currency/", 503, auth.CapabilitySettingsManage
	case "questions":
		s.questions = nil
		return "/admin/questions/", 503, auth.CapabilityCommentsModerate
	case "plugin":
		return "/admin/plugins/ai-pro-max/", 404, auth.CapabilityPluginsManage
	}
	t.Fatal("unknown fixture", kind)
	return
}

func TestDiagnosticAuthorityAuditRead(t *testing.T) {
	for _, mode := range []string{"api", "export", "page"} {
		for _, failure := range []bool{false, true} {
			for _, change := range []string{"logout", "fallback", "permission", "outage", "cancel", "allowed"} {
				name := mode + "/success/" + change
				if failure {
					name = mode + "/failure/" + change
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					s, _, _, _, _, _ := newPersistentAdminTestServer(t)
					manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead})
					fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
					sessions.SessionStore = fault
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					changed := false
					reader := diagnosticAuditReader{after: func() {
						changed = true
						aiUsageChange(t, s, manager, users, fault, cookie, change, "", cancel)
					}}
					if failure {
						reader.err = errors.New("PRIVATE-AUDIT-STORAGE-DETAIL")
					}
					s.SetAuditReader(reader)
					path := map[string]string{"api": "/api/v1/admin/audit-events", "export": "/admin/tools/audit-events.json", "page": "/admin/tools/"}[mode]
					r := diagnosticRequest(path, cookie, ctx)
					if change == "fallback" {
						token, _, err := manager.AuthenticateContext(t.Context(), "audit-fallback", actor.Username, "Account password for "+actor.Username+" 2026")
						if err != nil {
							t.Fatal(err)
						}
						r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
					}
					w, aborted := commentAuthorityServe(s.routes(), r)
					if !changed {
						t.Fatal("audit read not reached")
					}
					if change == "cancel" {
						if !aborted || w.Body.Len() != 0 {
							t.Fatal("cancelled diagnostics wrote output", aborted, w.Code)
						}
						return
					}
					want := map[string]int{"logout": 401, "fallback": 401, "permission": 403, "outage": 503, "allowed": 200}[change]
					if mode != "api" && want == 401 {
						want = 303
					}
					if change == "allowed" && failure && mode != "page" {
						want = 503
					}
					if aborted || w.Code != want || strings.Contains(w.Body.String(), "PRIVATE-DIAGNOSTIC-OBJECT") != (change == "allowed" && !failure) || strings.Contains(w.Body.String(), "PRIVATE-AUDIT-STORAGE-DETAIL") || strings.Contains(w.Body.String(), "PRIVATE-SESSION-DETAIL") {
						t.Fatal("audit response escaped authority", aborted, w.Code, want)
					}
				})
			}
		}
	}
}

func TestDiagnosticAuthorityRecoveryRead(t *testing.T) {
	for _, boundary := range []string{"badge", "navigation", "config", "missing-badge", "questions-badge", "plugin-badge"} {
		for _, change := range []string{"logout", "fallback", "permission", "outage", "cancel", "module-restarted", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				kind := "paused"
				if strings.HasSuffix(boundary, "-badge") {
					kind = strings.TrimSuffix(boundary, "-badge")
				}
				path, success, capability := diagnosticRecoveryFixture(t, s, kind)
				capabilities := []string{auth.CapabilityAdminAccess, capability}
				if strings.HasSuffix(boundary, "badge") && capability != auth.CapabilityCommentsModerate {
					capabilities = append(capabilities, auth.CapabilityCommentsModerate)
				}
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, capabilities)
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				changed := false
				after := func() { changed = true; aiUsageChange(t, s, manager, users, fault, cookie, change, "wechat", cancel) }
				switch boundary {
				case "config":
					s.wechat.Store = &featureAuthorityConfigStore{Store: s.wechat.Store, after: after}
				case "navigation":
					s.navigation = &navigationAuthorityStore{Store: s.navigation, afterSnapshot: after}
				default:
					s.comments = &accountAuthorityComments{Store: s.comments, afterSnapshot: after}
				}
				r := diagnosticRequest(path, cookie, ctx)
				if change == "fallback" {
					token, _, err := manager.AuthenticateContext(t.Context(), "recovery-fallback", actor.Username, "Account password for "+actor.Username+" 2026")
					if err != nil {
						t.Fatal(err)
					}
					r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
				}
				w, aborted := commentAuthorityServe(s.routes(), r)
				if !changed {
					t.Fatal("recovery read boundary not reached", w.Code)
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancelled recovery wrote output", aborted, w.Code)
					}
					return
				}
				want := map[string]int{"logout": 303, "fallback": 303, "permission": 403, "outage": 503, "module-restarted": success, "allowed": success}[change]
				if aborted || w.Code != want || strings.Contains(w.Body.String(), "data-feature-status") != (change == "allowed" || change == "module-restarted") {
					t.Fatal("recovery response escaped authority", aborted, w.Code, want)
				}
			})
		}
	}
}

func TestDiagnosticAuthorityFinalOutput(t *testing.T) {
	for _, kind := range []string{"api", "page", "paused", "missing", "questions", "plugin"} {
		for _, change := range []string{"logout", "permission", "outage", "cancel"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				path, capability := "/api/v1/admin/audit-events", auth.CapabilitySystemRead
				if kind == "page" {
					path = "/admin/tools/"
				} else if kind != "api" {
					path, _, capability = diagnosticRecoveryFixture(t, s, kind)
				}
				manager, users, sessions, cookie, _ := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, capability})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				s.SetAuditReader(diagnosticAuditReader{})
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				recorder := httptest.NewRecorder()
				writer := &diagnosticHeaderWriter{ResponseWriter: recorder, after: func() {
					aiUsageChange(t, s, manager, users, fault, cookie, change, "", cancel)
				}}
				aborted := serveAIGenerationBoundary(s.routes(), writer, diagnosticRequest(path, cookie, ctx))
				if !aborted || recorder.Body.Len() != 0 {
					t.Fatal("diagnostic body sent after final revocation", aborted, recorder.Code, recorder.Body.Len())
				}
			})
		}
	}
}
