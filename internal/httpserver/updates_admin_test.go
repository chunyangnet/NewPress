package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/update"
)

func TestUpdatePolicyAPI(t *testing.T) {
	s, handler, users, _, admin, owner := newPersistentAdminTestServer(t)
	m, err := update.New(t.TempDir(), "0.1.0", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s.SetUpdates(m)
	_, editor, principal := createAndLoginTestUser(t, users, owner, "policy-editor", "policy-editor@example.test", "Editor", "editor")
	const origin = "https://example.test"
	const url = origin + "/api/v1/admin/updates/policy"
	get := func() update.PolicySnapshot {
		t.Helper()
		res := serveCookieRequest(handler, "GET", origin+"/api/v1/admin/updates", admin, nil)
		var result update.Snapshot
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &result) != nil || result.Policy.Revision == "" {
			t.Fatalf("status %d: %s", res.Code, res.Body.String())
		}
		return result.Policy
	}
	defaults := get()
	rev := fmt.Sprintf(`"revision":%q`, defaults.Revision)
	const fields = `"auto_check":false,"check_interval_minutes":15,"observation_seconds":45`
	body := "{" + rev + "," + fields + "}"
	for _, tc := range []struct {
		name               string
		cookie             *http.Cookie
		csrf, origin, body string
		want               int
	}{
		{"anonymous", nil, "", origin, body, 401},
		{"capability", editor, principal.CSRFToken, origin, body, 403},
		{"csrf", admin, "", origin, body, 403},
		{"origin", admin, owner.CSRFToken, "https://other.test", body, 403},
		{"revision required", admin, owner.CSRFToken, origin, "{" + fields + "}", 400},
		{"revision syntax", admin, owner.CSRFToken, origin, `{"revision":"` + strings.Repeat("z", 64) + `","use_defaults":true}`, 400},
		{"requires all settings", admin, owner.CSRFToken, origin, "{" + rev + `,"auto_check":true}`, 400},
		{"unknown", admin, owner.CSRFToken, origin, "{" + rev + "," + fields + `,"force":true}`, 400},
		{"duplicate", admin, owner.CSRFToken, origin, "{" + rev + "," + fields + `,"auto_check":true}`, 400},
		{"mixed reset", admin, owner.CSRFToken, origin, "{" + rev + `,"use_defaults":true,"auto_check":null}`, 400},
		{"wrong type", admin, owner.CSRFToken, origin, strings.Replace(body, "false", "null", 1), 400},
		{"out of range", admin, owner.CSRFToken, origin, "{" + rev + `,"auto_check":false,"check_interval_minutes":0,"observation_seconds":45}`, 422},
		{"payload limit", admin, owner.CSRFToken, origin, body + strings.Repeat(" ", 1024), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("PUT", url, strings.NewReader(tc.body))
			if tc.cookie != nil {
				r.AddCookie(tc.cookie)
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-CSRF-Token", tc.csrf)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
		})
	}
	put := func(body string) *httptest.ResponseRecorder {
		return serveAdminCommentAPI(handler, admin, owner.CSRFToken, "PUT", url, body, "", origin)
	}
	res := put(body)
	var saved update.PolicySnapshot
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &saved) != nil || !saved.Customized || saved.AutoCheck || saved.CheckIntervalMinutes != 15 || saved.ObservationSeconds != 45 || saved != get() {
		t.Fatalf("save %d: %s", res.Code, res.Body.String())
	}
	res = put("{" + rev + `,"use_defaults":true}`)
	if res.Code != 409 || !strings.Contains(res.Body.String(), "updates.policy_changed") || get() != saved {
		t.Fatalf("stale reset %d: %s", res.Code, res.Body.String())
	}
	res = put(fmt.Sprintf(`{"revision":%q,"use_defaults":true}`, saved.Revision))
	if res.Code != 200 || get() != defaults {
		t.Fatalf("reset %d: %s", res.Code, res.Body.String())
	}
}

func TestUpdatesAdminSecurityAndSecretProjection(t *testing.T) {
	server, handler, authManager, _, cookie, owner := newPersistentAdminTestServer(t)
	manager, err := update.New(t.TempDir(), "0.1.0", filepath.Join("..", "..", "themes"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	server.SetUpdates(manager)
	_, editorCookie, _ := createAndLoginTestUser(t, authManager, owner, "updates-editor", "updates-editor@example.test", "Editor", "editor")
	for _, path := range []string{"/admin/updates/", "/api/v1/admin/updates"} {
		r := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, editorCookie, nil)
		if r.Code != 403 {
			t.Fatalf("editor access to %s=%d", path, r.Code)
		}
	}
	page := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/updates/", cookie, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "data-update-center") {
		t.Fatalf("page=%d %s", page.Code, page.Body.String())
	}
	const valid = `{"owner":"team","repo":"private","ref":"master","manifest":"updates/stable.json","token":"private-token-not-for-clients","clear_token":false}`
	for _, tc := range []struct {
		name, body, origin, csrf, contentType string
		code                                  int
	}{
		{"valid", valid, "https://example.test", owner.CSRFToken, "application/json", 200},
		{"official public", `{"official":true}`, "https://example.test", owner.CSRFToken, "application/json", 200},
		{"csrf", valid, "https://example.test", "", "application/json", 403},
		{"origin", valid, "https://evil.test", owner.CSRFToken, "application/json", 403},
		{"type", valid, "https://example.test", owner.CSRFToken, "text/plain", 415},
		{"duplicate", `{"owner":"a","owner":"b"}`, "https://example.test", owner.CSRFToken, "application/json", 400},
		{"unknown", `{"command":"rm"}`, "https://example.test", owner.CSRFToken, "application/json", 400},
		{"too large", `{"token":"` + strings.Repeat("x", 4096) + `"}`, "https://example.test", owner.CSRFToken, "application/json", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("PUT", "https://example.test/api/v1/admin/updates/source", strings.NewReader(tc.body))
			r.AddCookie(cookie)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-CSRF-Token", tc.csrf)
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
		})
	}
	for _, path := range []string{"/admin/updates/", "/api/v1/admin/updates"} {
		r := serveCookieRequest(handler, "GET", "https://example.test"+path, cookie, nil)
		if r.Code != 200 || strings.Contains(r.Body.String(), "private-token-not-for-clients") {
			t.Fatalf("secret projection: %d %s", r.Code, r.Body.String())
		}
	}
	r := serveCookieRequest(handler, "GET", "https://example.test/api/v1/admin/updates?token=x", cookie, nil)
	if r.Code != 400 {
		t.Fatal("unknown query accepted")
	}
	r = serveCookieRequest(handler, "GET", "https://example.test/api/v1/admin/updates/progress", nil, nil)
	if r.Code != 401 {
		t.Fatalf("unscoped progress=%d", r.Code)
	}
	r = serveCookieRequest(handler, "GET", "https://example.test/api/v1/admin/updates", nil, nil)
	if r.Code != 401 {
		t.Fatalf("anonymous status=%d", r.Code)
	}
}
