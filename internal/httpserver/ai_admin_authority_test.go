package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
)

const aiAdminAuthorityMarker = "PRIVATE-AI-ADMIN-RESULT"

func aiAdminAuthorityRequest(method, route string, cookie *http.Cookie, actor auth.Principal, ctx context.Context, input string) *http.Request {
	r := httptest.NewRequest(method, "https://example.test/api/v1/admin/ai/"+route, strings.NewReader(input)).WithContext(ctx)
	r.AddCookie(cookie)
	for key, value := range authenticatedAPIHeaders(actor) {
		r.Header.Set(key, value)
	}
	return r
}

func aiAdminAuthoritySettings(t *testing.T, s *Server) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ai-settings.json")
	store, err := aieditor.NewSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	input := aieditor.Settings{Revision: store.Snapshot().Revision, DefaultProvider: "authority", Providers: []aieditor.Provider{
		{ID: "authority", Name: aiAdminAuthorityMarker, BaseURL: "https://writer.example.test/v1", APIKey: "private-stored-credential", Models: []string{"model-a"}, DefaultModel: "model-a"},
	}}
	if _, err := store.Save(input); err != nil {
		t.Fatal(err)
	}
	s.SetAISettings(store)
	input = store.Snapshot()
	input.Providers[0].Name = "Updated " + aiAdminAuthorityMarker
	raw, _ := json.Marshal(input)
	return path, string(raw)
}

func TestAIAdminAuthorityPreparation(t *testing.T) {
	for _, boundary := range []string{"settings-body", "models-body", "prepare-body", "prepare-categories", "prompts-catalog"} {
		for _, change := range []string{"logout", "permission", "module-restarted", "outage", "cancel", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn, auth.CapabilitySettingsManage})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				path, settingsInput := aiAdminAuthoritySettings(t, s)
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var changed atomic.Bool
				after := func() {
					changed.Store(true)
					switch change {
					case "logout":
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Fatal(err)
						}
					case "permission":
						if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: []string{auth.CapabilityAdminAccess}}}); err != nil {
							t.Fatal(err)
						}
					case "module-restarted":
						accountAuthorityModule(t, s, "ai", false)
						accountAuthorityModule(t, s, "ai", true)
					case "outage":
						fault.fail.Store(true)
					case "cancel":
						cancel()
					}
				}
				method, route, input := http.MethodPost, "prepare", `{"title":"Title","markdown":"`+aiAdminAuthorityMarker+`"}`
				if boundary == "settings-body" {
					route, input = "settings", settingsInput
				} else if boundary == "models-body" {
					route, input = "models", `{"provider_id":"authority"}`
				} else if boundary == "prepare-categories" {
					s.repository = &editorAuthorityRepository{Repository: repository, afterTerms: after}
				} else if boundary == "prompts-catalog" {
					method, route, input = http.MethodGet, "prompts", ""
					store, _, _ := templateAuthorityFixture(t, s)
					store.afterRead = after
				}
				var calls atomic.Int32
				setAIResponse(s, 200, "application/json", `{"data":[{"id":"`+aiAdminAuthorityMarker+`"}]}`, func(*http.Request) { calls.Add(1) })
				r := aiAdminAuthorityRequest(method, route, cookie, actor, ctx, input)
				if strings.HasSuffix(boundary, "body") {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
				}
				w, aborted := commentAuthorityServe(s.routes(), r)
				wantCalls := int32(0)
				if boundary == "models-body" && change == "allowed" {
					wantCalls = 1
				}
				if !changed.Load() || calls.Load() != wantCalls {
					t.Errorf("preparation escaped authority boundary: changed=%v calls=%d/%d", changed.Load(), calls.Load(), wantCalls)
				}
				disk, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				mutated := string(disk) != string(before)
				if mutated != (boundary == "settings-body" && change == "allowed") {
					t.Error("unauthorized settings mutation", mutated)
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancelled request produced output", aborted, w.Code)
					}
					return
				}
				want := map[string]int{"logout": 401, "permission": 403, "module-restarted": 409, "outage": 503, "allowed": 200}[change]
				marker := aiAdminAuthorityMarker
				if boundary == "prompts-catalog" {
					marker = templateAuthorityMarker
				}
				if aborted || w.Code != want || strings.Contains(w.Body.String(), marker) != (change == "allowed") || strings.Contains(w.Body.String(), "private-stored-credential") {
					t.Fatal("AI administration authority response", aborted, w.Code, want)
				}
				if change == "outage" && (w.Header().Get("Retry-After") != "5" || w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "PRIVATE-SESSION-DETAIL")) {
					t.Fatal("session outage exposed details or discarded cookie", w.Header())
				}
			})
		}
	}
}

func TestAIAdminAuthorityProbePermission(t *testing.T) {
	for _, boundary := range []string{"body", "before-upstream", "upstream"} {
		for _, local := range []bool{false, true} {
			name := boundary + "/public"
			if local {
				name = boundary + "/explicit-approval"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				_, users, _, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn, auth.CapabilitySettingsManage})
				var changed atomic.Bool
				after := func() {
					changed.Store(true)
					if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn}}}); err != nil {
						t.Error(err)
					}
				}
				var calls atomic.Int32
				setAIResponse(s, 200, "application/json", `{"data":[{"id":"`+aiAdminAuthorityMarker+`"}]}`, func(*http.Request) {
					calls.Add(1)
					if boundary == "upstream" {
						after()
					}
				})
				input := `{"base_url":"https://writer.example.test/v1","api_key":"test-key"}`
				if local {
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						if boundary == "upstream" {
							after()
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"data":[{"id":"` + aiAdminAuthorityMarker + `"}]}`))
					}))
					defer upstream.Close()
					raw, _ := json.Marshal(map[string]any{"base_url": upstream.URL, "api_key": "test-key", "allow_insecure": true})
					input = string(raw)
				}
				r := aiAdminAuthorityRequest(http.MethodPost, "models", cookie, actor, t.Context(), input)
				w := &aiGenerationBoundaryWriter{ResponseRecorder: httptest.NewRecorder()}
				if boundary == "body" {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
				} else if boundary == "before-upstream" {
					w.deadline = after
				}
				aborted := serveAIGenerationBoundary(s.routes(), w, r)
				want, wantCalls := 200, int32(1)
				if local {
					want = 403
					if boundary != "upstream" {
						wantCalls = 0
					}
				}
				if !changed.Load() || aborted || w.Code != want || calls.Load() != wantCalls || strings.Contains(w.Body.String(), aiAdminAuthorityMarker) == local {
					t.Fatal("probe approval used stale role", changed.Load(), aborted, w.Code, want, calls.Load(), wantCalls)
				}
			})
		}
	}
}
