package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
)

func TestDiagnosticAuthorityRetainsLoadedSections(t *testing.T) {
	for _, mode := range []string{"page-audit", "page-summary", "recovery"} {
		for _, change := range []string{"lost-section", "gained", "allowed"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				initial := []string{auth.CapabilityAdminAccess, auth.CapabilitySettingsManage, auth.CapabilitySystemRead}
				if mode == "recovery" {
					initial = append(initial, auth.CapabilityCommentsModerate)
				}
				_, users, _, cookie, _ := dashboardAuthorityIdentity(t, s, initial)
				path := "/admin/tools/"
				if mode == "recovery" {
					accountAuthorityModule(t, s, "wechat", false)
					path = "/admin/wechat/"
				}
				changed := false
				after := func() {
					changed = true
					caps := initial
					if change == "lost-section" {
						caps = []string{auth.CapabilityAdminAccess, auth.CapabilitySettingsManage}
						if mode == "recovery" {
							caps = append(caps, auth.CapabilityCommentsModerate)
						}
					} else if change == "gained" {
						caps = append(append([]string(nil), initial...), auth.CapabilityNavigationManage)
					}
					if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Changed role", Capabilities: caps}}); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "page-audit" {
					s.SetAuditReader(diagnosticAuditReader{after: after})
				} else {
					s.comments = &accountAuthorityComments{Store: s.comments, afterSnapshot: after}
				}
				w, aborted := commentAuthorityServe(s.routes(), diagnosticRequest(path, cookie, t.Context()))
				want := 200
				if change == "lost-section" {
					want = 403
				}
				if !changed || aborted || w.Code != want {
					t.Fatal("retained entry capability accepted stale section", changed, aborted, w.Code, want)
				}
				if change == "lost-section" && (strings.Contains(w.Body.String(), "PRIVATE-DIAGNOSTIC-OBJECT") || strings.Contains(w.Body.String(), "data-feature-status")) {
					t.Fatal("revoked section reached response")
				}
			})
		}
	}
}

func TestDiagnosticAuthorityPartialRoles(t *testing.T) {
	for _, capability := range []string{auth.CapabilitySystemRead, auth.CapabilitySettingsManage, auth.CapabilityAppearanceManage, auth.CapabilityNavigationManage} {
		t.Run(capability, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			_, _, _, cookie, _ := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, capability})
			reads := 0
			s.SetAuditReader(diagnosticAuditReader{after: func() { reads++ }})
			w, aborted := commentAuthorityServe(s.routes(), diagnosticRequest("/admin/tools/", cookie, t.Context()))
			if aborted || w.Code != 200 || (reads != 0) != (capability == auth.CapabilitySystemRead) {
				t.Fatal("tool capability alternative changed audit scope", aborted, w.Code, reads)
			}
		})
	}
}

func TestDiagnosticAuthorityEquivalentAndUnrelatedRoleChanges(t *testing.T) {
	for _, mode := range []string{"tools", "recovery"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			initial := []string{auth.CapabilityAdminAccess, auth.CapabilitySettingsManage, "system:*", auth.CapabilityProfileWriteOwn}
			if mode == "recovery" {
				initial = append(initial, auth.CapabilityCommentsModerate)
			}
			_, users, _, cookie, _ := dashboardAuthorityIdentity(t, s, initial)
			path := "/admin/tools/"
			if mode == "recovery" {
				accountAuthorityModule(t, s, "wechat", false)
				path = "/admin/wechat/"
			}
			changed := false
			s.comments = &accountAuthorityComments{Store: s.comments, afterSnapshot: func() {
				changed = true
				caps := []string{auth.CapabilityAdminAccess, auth.CapabilitySettingsManage, auth.CapabilitySystemRead, auth.CapabilitySystemUpdate}
				if mode == "recovery" {
					caps = append(caps, auth.CapabilityCommentsModerate)
				}
				if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Equivalent diagnostic role", Capabilities: caps}}); err != nil {
					t.Fatal(err)
				}
			}}
			w, aborted := commentAuthorityServe(s.routes(), diagnosticRequest(path, cookie, t.Context()))
			if !changed || aborted || w.Code != 200 {
				t.Fatal("unrelated role change blocked authorized diagnostics", changed, aborted, w.Code)
			}
		})
	}
}

func TestDiagnosticAuthorityRecoveryPreviewAndRouteScope(t *testing.T) {
	for _, kind := range []string{"paused", "missing", "questions", "plugin"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			path, success, _ := diagnosticRecoveryFixture(t, s, kind)
			var err error
			s.auth, err = auth.New(auth.Config{})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "http://localhost"+path, nil)
			r.RemoteAddr = "127.0.0.1:1234"
			w, aborted := commentAuthorityServe(s.routes(), r)
			if aborted || w.Code != success || !strings.Contains(w.Body.String(), "data-feature-status") {
				t.Fatal("local preview lost diagnostics", aborted, w.Code, success)
			}
			r = httptest.NewRequest("GET", "http://example.test"+path, nil)
			r.RemoteAddr = "198.51.100.10:1234"
			w, aborted = commentAuthorityServe(s.routes(), r)
			if aborted || w.Code != 404 || strings.Contains(w.Body.String(), "data-feature-status") {
				t.Fatal("remote preview exposed diagnostics", aborted, w.Code)
			}
			if kind != "paused" {
				return
			}
			for _, row := range []struct{ method, path string }{{"POST", path}, {"GET", path + "123/"}, {"GET", "/api/v1/admin/wechat/config"}} {
				r = httptest.NewRequest(row.method, "http://localhost"+row.path, nil)
				r.RemoteAddr = "127.0.0.1:1234"
				w, aborted = commentAuthorityServe(s.routes(), r)
				if aborted || w.Code < 400 || strings.Contains(w.Body.String(), "data-feature-status") {
					t.Fatal("paused recovery widened route scope", row, aborted, w.Code)
				}
			}
		})
	}
}

type diagnosticDeadlineReader struct{ budget time.Duration }

func (r *diagnosticDeadlineReader) List(ctx context.Context, _ audit.Query) (audit.Page, error) {
	deadline, ok := ctx.Deadline()
	if ok {
		r.budget = time.Until(deadline)
		<-ctx.Done()
	}
	// Model a repository that completes with a result as its deadline expires.
	return audit.Page{Total: 1, Events: []audit.Entry{{ObjectID: "PRIVATE-DIAGNOSTIC-DEADLINE"}}}, nil
}

func TestDiagnosticAuthorityAuditReadDeadline(t *testing.T) {
	for _, mode := range []string{"api", "export", "page"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, cookie, _ := newPersistentAdminTestServer(t)
			reader := &diagnosticDeadlineReader{}
			s.SetAuditReader(reader)
			path := map[string]string{"api": "/api/v1/admin/audit-events", "export": "/admin/tools/audit-events.json", "page": "/admin/tools/"}[mode]
			w, aborted := commentAuthorityServe(s.routes(), diagnosticRequest(path, cookie, t.Context()))
			want := http.StatusServiceUnavailable
			if mode == "page" {
				want = http.StatusOK
			}
			if reader.budget <= 0 || reader.budget > 2*time.Second || aborted || w.Code != want || strings.Contains(w.Body.String(), "PRIVATE-DIAGNOSTIC-DEADLINE") {
				t.Fatal("expired audit read exposed events", reader.budget, aborted, w.Code, want)
			}
		})
	}
}
