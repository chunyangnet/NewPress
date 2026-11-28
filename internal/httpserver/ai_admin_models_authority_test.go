package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
)

func TestAIAdminAuthorityModelPagination(t *testing.T) {
	for _, protocol := range []string{aieditor.ProtocolAnthropic, aieditor.ProtocolGeminiText} {
		for _, change := range []string{"logout", "permission", "allowed"} {
			t.Run(protocol+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				manager, users, _, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySettingsManage})
				var calls atomic.Int32
				after := func() {
					if change == "logout" {
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Error(err)
						}
					} else if change == "permission" {
						if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: []string{auth.CapabilityAdminAccess}}}); err != nil {
							t.Error(err)
						}
					}
				}
				s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
					page := calls.Add(1)
					payload := `{"data":[{"id":"first-model"}],"has_more":true,"last_id":"first-model"}`
					if page == 2 {
						payload = `{"data":[{"id":"` + aiAdminAuthorityMarker + `"}]}`
					}
					if protocol == aieditor.ProtocolGeminiText {
						payload = `{"models":[{"name":"models/first-model"}],"nextPageToken":"next-page"}`
						if page == 2 {
							payload = `{"models":[{"name":"models/` + aiAdminAuthorityMarker + `"}]}`
						}
					}
					body := &aiGenerationTrackedBody{ReadCloser: io.NopCloser(strings.NewReader(payload))}
					if page == 1 {
						body.afterClose = after
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
				})}}
				raw, _ := json.Marshal(map[string]string{"protocol": protocol, "base_url": "https://writer.example.test", "api_key": "test-key"})
				w, aborted := commentAuthorityServe(s.routes(), aiAdminAuthorityRequest("POST", "models", cookie, actor, t.Context(), string(raw)))
				want, wantCalls := 200, int32(2)
				if change == "logout" {
					want, wantCalls = 401, 1
				} else if change == "permission" {
					want, wantCalls = 403, 1
				}
				if aborted || w.Code != want || calls.Load() != wantCalls || strings.Contains(w.Body.String(), aiAdminAuthorityMarker) != (change == "allowed") {
					t.Fatal("model pagination passed revocation", aborted, w.Code, calls.Load(), wantCalls)
				}
			})
		}
	}
}

func TestAIAdminAuthorityModelsCompletion(t *testing.T) {
	for _, boundary := range []string{"upstream", "completion"} {
		for _, change := range []string{"logout", "permission", "module-restarted", "outage", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySettingsManage})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				var changed atomic.Bool
				after := func() {
					changed.Store(true)
					switch change {
					case "logout":
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Error(err)
						}
					case "permission":
						if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: []string{auth.CapabilityAdminAccess}}}); err != nil {
							t.Error(err)
						}
					case "module-restarted":
						accountAuthorityModule(t, s, "ai", false)
						accountAuthorityModule(t, s, "ai", true)
					case "outage":
						fault.fail.Store(true)
					}
				}
				body := &aiGenerationTrackedBody{ReadCloser: io.NopCloser(strings.NewReader(`{"data":[{"id":"` + aiAdminAuthorityMarker + `"}]}`))}
				if boundary == "completion" {
					body.afterClose = after
				}
				s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
					if boundary == "upstream" {
						after()
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
				})}}
				w, aborted := commentAuthorityServe(s.routes(), aiAdminAuthorityRequest("POST", "models", cookie, actor, t.Context(), `{"base_url":"https://writer.example.test","api_key":"test-key"}`))
				want := map[string]int{"logout": 401, "permission": 403, "module-restarted": 409, "outage": 503, "allowed": 200}[change]
				if !changed.Load() || !body.closed.Load() || aborted || w.Code != want || strings.Contains(w.Body.String(), aiAdminAuthorityMarker) != (change == "allowed") || strings.Contains(w.Body.String(), "PRIVATE-SESSION-DETAIL") {
					t.Fatal("model result escaped authority check", changed.Load(), body.closed.Load(), aborted, w.Code, want)
				}
			})
		}
	}
}

func TestAIAdminAuthoritySilentModels(t *testing.T) {
	for _, boundary := range []string{"headers", "body"} {
		for _, change := range []string{"logout", "permission", "module-restarted", "outage"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySettingsManage})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				entered, cancelled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var body *aiGenerationTrackedBody
				s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
					if boundary == "headers" {
						close(entered)
						<-r.Context().Done()
						close(cancelled)
						return nil, r.Context().Err()
					}
					reader, writer := io.Pipe()
					body = &aiGenerationTrackedBody{ReadCloser: reader}
					go func() {
						_, _ = io.WriteString(writer, `{"data":[`)
						close(entered)
						<-r.Context().Done()
						_ = writer.CloseWithError(r.Context().Err())
						close(cancelled)
					}()
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
				})}}
				ctx, cancel := context.WithTimeout(t.Context(), 9*time.Second)
				defer cancel()
				w := httptest.NewRecorder()
				var aborted bool
				go func() {
					defer close(finished)
					aborted = serveAIGenerationBoundary(s.routes(), w, aiAdminAuthorityRequest("POST", "models", cookie, actor, ctx, `{"base_url":"https://writer.example.test","api_key":"test-key"}`))
				}()
				t.Cleanup(func() {
					cancel()
					waitAIGenerationSignal(t, finished, 5*time.Second, "model discovery handler cleanup timed out")
				})
				waitAIGenerationSignal(t, entered, 4*time.Second, "model discovery did not reach waiting boundary")
				want := 401
				switch change {
				case "logout":
					if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Fatal(err)
					}
				case "permission":
					want = 403
					if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: []string{auth.CapabilityAdminAccess}}}); err != nil {
						t.Fatal(err)
					}
				case "module-restarted":
					want = 409
					accountAuthorityModule(t, s, "ai", false)
					accountAuthorityModule(t, s, "ai", true)
				case "outage":
					want = 503
					fault.fail.Store(true)
				}
				waitAIGenerationSignal(t, cancelled, 4*time.Second, "silent model discovery continued after revocation")
				waitAIGenerationSignal(t, finished, 2*time.Second, "model discovery did not finish after cancellation")
				if aborted || w.Code != want || boundary == "body" && (body == nil || !body.closed.Load()) || strings.Contains(w.Body.String(), "PRIVATE-") {
					t.Fatal("silent model authority response", aborted, w.Code, want)
				}
			})
		}
	}
}
