package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"newpress.local/newpress/internal/auth"
)

func aiAdminAuthorityPayload(action, settings string) (method, route, input string) {
	switch action {
	case "settings-read":
		return "GET", "settings", ""
	case "settings-save":
		return "POST", "settings", settings
	case "connections", "prompts":
		return "GET", action, ""
	case "models":
		return "POST", action, `{"provider_id":"authority"}`
	case "prepare":
		return "POST", action, `{"title":"Title","markdown":"` + aiAdminAuthorityMarker + `"}`
	default:
		panic("unexpected AI admin test action")
	}
}

func TestAIAdminAuthorityFinalOutput(t *testing.T) {
	for _, action := range []string{"settings-read", "settings-save", "connections", "prompts", "models", "prepare"} {
		for _, change := range []string{"logout", "module-restarted"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, manager, _, cookie, actor := newPersistentAdminTestServer(t)
				_, settings := aiAdminAuthoritySettings(t, s)
				templateAuthorityFixture(t, s)
				setAIResponse(s, 200, "application/json", `{"data":[{"id":"`+aiAdminAuthorityMarker+`"}]}`, nil)
				changed := false
				recorder := httptest.NewRecorder()
				w := &aiEditorAuthorityHeaderWriter{ResponseWriter: recorder, after: func() {
					changed = true
					if change == "logout" {
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Fatal(err)
						}
					} else {
						accountAuthorityModule(t, s, "ai", false)
						accountAuthorityModule(t, s, "ai", true)
					}
				}}
				method, route, input := aiAdminAuthorityPayload(action, settings)
				aborted := serveAIGenerationBoundary(s.routes(), w, aiAdminAuthorityRequest(method, route, cookie, actor, t.Context(), input))
				if !changed || !aborted || recorder.Body.Len() != 0 {
					t.Fatal("revoked final JSON disclosed result", changed, aborted, recorder.Code, recorder.Body.Len())
				}
				if action == "settings-save" && !strings.HasPrefix(s.aiSettings.Snapshot().Providers[0].Name, "Updated ") {
					t.Fatal("output revocation rolled back a committed update")
				}
			})
		}
	}
}

func TestAIAdminAuthorityLargeModelsOutput(t *testing.T) {
	s, _, manager, _, cookie, actor := newPersistentAdminTestServer(t)
	items := make([]map[string]string, 1000)
	for i := range items {
		items[i] = map[string]string{"id": strings.Repeat("&", 190) + fmt.Sprint(i)}
	}
	raw, _ := json.Marshal(map[string]any{"data": items})
	setAIResponse(s, 200, "application/json", string(raw), nil)
	w := &aiGenerationBoundaryWriter{ResponseRecorder: httptest.NewRecorder()}
	changed := false
	w.afterWrite = func([]byte) {
		if !changed {
			changed = true
			if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
				t.Fatal(err)
			}
		}
	}
	r := aiAdminAuthorityRequest("POST", "models", cookie, actor, t.Context(), `{"base_url":"https://writer.example.test","api_key":"test-key"}`)
	aborted := serveAIGenerationBoundary(s.routes(), w, r)
	if !changed || !aborted || w.Body.Len() > privateResponseCheckBytes || strings.Contains(w.Body.String(), `"error"`) {
		t.Fatal("large models result escaped bounded output checks", changed, aborted, w.Body.Len())
	}
}

func TestAIAdminAuthorityCapabilityAlternatives(t *testing.T) {
	for _, role := range []string{"writer", "settings", "no-admin"} {
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			caps := []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn}
			if role == "settings" {
				caps[1] = auth.CapabilitySettingsManage
			} else if role == "no-admin" {
				caps = []string{auth.CapabilityContentWriteOwn, auth.CapabilitySettingsManage}
			}
			_, _, _, cookie, actor := dashboardAuthorityIdentity(t, s, caps)
			_, settings := aiAdminAuthoritySettings(t, s)
			templateAuthorityFixture(t, s)
			contentAuthorityDisable(t, s, false)
			var calls atomic.Int32
			setAIResponse(s, 200, "application/json", `{"data":[{"id":"`+aiAdminAuthorityMarker+`"}]}`, func(*http.Request) { calls.Add(1) })
			for _, action := range []string{"settings-read", "connections", "prompts", "models", "prepare", "settings-save"} {
				t.Run(action, func(t *testing.T) {
					method, route, input := aiAdminAuthorityPayload(action, settings)
					w, aborted := commentAuthorityServe(s.routes(), aiAdminAuthorityRequest(method, route, cookie, actor, t.Context(), input))
					allowed := role != "no-admin"
					if strings.HasPrefix(action, "settings-") {
						allowed = role == "settings"
					} else if action == "connections" || action == "prepare" {
						allowed = role == "writer"
					}
					want := 403
					if allowed {
						want = 200
					}
					if aborted || w.Code != want || strings.Contains(w.Body.String(), "private-stored-credential") {
						t.Fatal("capability alternatives changed", aborted, w.Code, want)
					}
				})
			}
			wantCalls := int32(1)
			if role == "no-admin" {
				wantCalls = 0
			}
			if calls.Load() != wantCalls {
				t.Fatal("unauthorized role invoked model", calls.Load())
			}
		})
	}
}

func TestAIAdminAuthorityLoopbackReadOnly(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	_, settings := aiAdminAuthoritySettings(t, s)
	manager, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s.auth = manager
	for _, action := range []string{"settings-read", "connections", "prompts", "settings-save", "models", "prepare"} {
		t.Run(action, func(t *testing.T) {
			method, route, input := aiAdminAuthorityPayload(action, settings)
			r := httptest.NewRequest(method, "http://localhost/api/v1/admin/ai/"+route, strings.NewReader(input))
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("Content-Type", "application/json")
			w, aborted := commentAuthorityServe(s.routes(), r)
			want := 200
			if method == "POST" {
				want = 403
			}
			if aborted || w.Code != want {
				t.Fatal("loopback read-only behavior changed", aborted, w.Code, want)
			}
		})
	}
}

func TestAIAdminAuthoritySameAccountFallback(t *testing.T) {
	for _, action := range []string{"settings-save", "models", "prepare", "prompts"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			manager, _, _, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn, auth.CapabilitySettingsManage})
			_, settings := aiAdminAuthoritySettings(t, s)
			store, _, _ := templateAuthorityFixture(t, s)
			token, _, err := manager.AuthenticateContext(t.Context(), "ai-admin-fallback", actor.Username, "Account password for "+actor.Username+" 2026")
			if err != nil {
				t.Fatal(err)
			}
			after := func() {
				if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
					t.Fatal(err)
				}
			}
			method, route, input := aiAdminAuthorityPayload(action, settings)
			r := aiAdminAuthorityRequest(method, route, cookie, actor, t.Context(), input)
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
			if action == "prompts" {
				store.afterRead = after
			} else {
				r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
			}
			var calls atomic.Int32
			setAIResponse(s, 200, "application/json", `{"data":[{"id":"`+aiAdminAuthorityMarker+`"}]}`, func(*http.Request) { calls.Add(1) })
			w, aborted := commentAuthorityServe(s.routes(), r)
			if aborted || w.Code != 401 || calls.Load() != 0 || strings.Contains(w.Body.String(), "PRIVATE-") {
				t.Fatal("fallback session took over request", aborted, w.Code, calls.Load())
			}
			if p, err := manager.ResolveSession(t.Context(), token); err != nil || p.UserID != actor.UserID {
				t.Fatal("fallback was not independently valid", err)
			}
		})
	}
}

func TestAIAdminAuthorityRequestProofs(t *testing.T) {
	for _, action := range []string{"settings-save", "models", "prepare"} {
		for _, change := range []string{"origin", "csrf", "duplicate-csrf"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
				_, settings := aiAdminAuthoritySettings(t, s)
				before := s.aiSettings.Snapshot().Revision
				method, route, input := aiAdminAuthorityPayload(action, settings)
				r := aiAdminAuthorityRequest(method, route, cookie, actor, t.Context(), input)
				r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: func() {
					switch change {
					case "origin":
						r.Header.Set("Origin", "https://other.example.test")
					case "csrf":
						r.Header.Set("X-CSRF-Token", "invalid")
					case "duplicate-csrf":
						r.Header.Add("X-CSRF-Token", actor.CSRFToken)
					}
				}}
				var calls atomic.Int32
				setAIResponse(s, 200, "application/json", `{"data":[{"id":"`+aiAdminAuthorityMarker+`"}]}`, func(*http.Request) { calls.Add(1) })
				w, aborted := commentAuthorityServe(s.routes(), r)
				if aborted || w.Code != 403 || calls.Load() != 0 || s.aiSettings.Snapshot().Revision != before || strings.Contains(w.Body.String(), aiAdminAuthorityMarker) {
					t.Fatal("changed request proof accepted", aborted, w.Code, calls.Load())
				}
			})
		}
	}
}
