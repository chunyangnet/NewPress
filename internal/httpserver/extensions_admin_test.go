package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/extensionupdate"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/update"
)

type httpExtensionMarket struct {
	view        plugin.MarketView
	check       func(context.Context) error
	updateTheme func(context.Context, *theme.Manager, string, string, string, string, extension.Progress) error
}

func (m *httpExtensionMarket) View() plugin.MarketView { return m.view }
func (m *httpExtensionMarket) Refresh(ctx context.Context) error {
	if m.check != nil {
		return m.check(ctx)
	}
	return nil
}
func (m *httpExtensionMarket) InstallWithProgress(context.Context, string, string, extension.Progress) error {
	return errors.New("fixture install unavailable")
}
func (m *httpExtensionMarket) Update(context.Context, string, string, string, string, extension.Progress) error {
	return errors.New("fixture update unavailable")
}
func (m *httpExtensionMarket) InstallTheme(context.Context, *theme.Manager, string, string, extension.Progress) error {
	return errors.New("fixture install unavailable")
}
func (m *httpExtensionMarket) UpdateTheme(ctx context.Context, tm *theme.Manager, slug, digest, expected, revision string, p extension.Progress) error {
	if m.updateTheme != nil {
		return m.updateTheme(ctx, tm, slug, digest, expected, revision, p)
	}
	return errors.New("fixture update unavailable")
}

func TestExtensionUpdatesAuthorizationProtocolAndSystemExclusion(t *testing.T) {
	s, h, authManager, _, cookie, owner := newPersistentAdminTestServer(t)
	next := s.themes.Active()
	next.Version = "0.99.0"
	market := &httpExtensionMarket{view: plugin.MarketView{Configured: true, Schema: 2, Expires: time.Now().Add(time.Hour), Themes: []theme.Release{{Manifest: next, Path: "packages/theme.zip", SHA256: strings.Repeat("a", 64), Size: 100, Notes: "<script>release-notes-xss</script>", Release: &extension.Metadata{Channel: "stable", PublishedAt: time.Now().UTC(), DataCompatible: true}}}}}
	service, err := extensionupdate.New(filepath.Join(t.TempDir(), "state.json"), market, s.plugins, s.themes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	s.SetExtensionUpdates(service)
	coreRoot := t.TempDir()
	core, err := update.New(coreRoot, "0.1.0", s.config.ThemesDir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	s.SetUpdates(core)
	_, editorCookie, _ := createAndLoginTestUser(t, authManager, owner, "extension-editor", "extension-editor@example.test", "Editor", "editor")
	for _, path := range []string{"/admin/themes/", "/admin/plugins/updates/", "/admin/appearance/updates/", "/api/v1/admin/extensions/plugin", "/api/v1/admin/extensions/theme", "/api/v1/admin/extensions/plugin/job", "/api/v1/admin/extensions/theme/job"} {
		if response := serveCookieRequest(h, "GET", "https://example.test"+path, editorCookie, nil); response.Code != 403 {
			t.Fatalf("editor %s=%d", path, response.Code)
		}
		response := serveCookieRequest(h, "GET", "https://example.test"+path, cookie, nil)
		if response.Code != 200 {
			t.Fatalf("owner %s=%d %s", path, response.Code, response.Body.String())
		}
		if strings.HasPrefix(path, "/admin/") && !strings.Contains(response.Body.String(), "data-extension-workspace") {
			t.Fatal("missing update workspace")
		}
		if strings.HasPrefix(path, "/admin/") && strings.Contains(response.Body.String(), "<script>release-notes-xss</script>") {
			t.Fatal("release notes were not escaped")
		}
	}
	if response := serveCookieRequest(h, "GET", "https://example.test/api/v1/admin/extensions/theme/job", nil, nil); response.Code != 401 {
		t.Fatal("anonymous progress", response.Code)
	}
	for _, path := range []string{"/api/v1/admin/extensions/theme?channel=stable&channel=preview", "/api/v1/admin/extensions/theme?channel=nightly", "/api/v1/admin/extensions/theme/job?token=x"} {
		if response := serveCookieRequest(h, "GET", "https://example.test"+path, cookie, nil); response.Code != 400 {
			t.Fatal(path, response.Code)
		}
	}
	body, _ := json.Marshal(map[string]any{"channel": "stable", "expected_user_id": owner.UserID})
	for _, test := range []struct {
		name, body, origin, csrf, contentType string
		code                                  int
	}{
		{"csrf", string(body), "https://example.test", "", "application/json", 403},
		{"origin", string(body), "https://evil.test", owner.CSRFToken, "application/json", 403},
		{"type", string(body), "https://example.test", owner.CSRFToken, "text/plain", 415},
		{"duplicate", `{"channel":"stable","channel":"preview"}`, "https://example.test", owner.CSRFToken, "application/json", 400},
		{"unknown", `{"download_url":"https://evil.test/package.zip"}`, "https://example.test", owner.CSRFToken, "application/json", 400},
		{"identity", `{"expected_user_id":999999}`, "https://example.test", owner.CSRFToken, "application/json", 409},
		{"large", `{"version":"` + strings.Repeat("a", 4096) + `"}`, "https://example.test", owner.CSRFToken, "application/json", 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://example.test/api/v1/admin/extensions/theme/check", strings.NewReader(test.body))
			r.AddCookie(cookie)
			r.Header.Set("Origin", test.origin)
			r.Header.Set("X-CSRF-Token", test.csrf)
			r.Header.Set("Content-Type", test.contentType)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.code {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	started := make(chan struct{})
	market.check = func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }
	response := serveAdminCommentAPI(h, cookie, owner.CSRFToken, "POST", "https://example.test/api/v1/admin/extensions/theme/check", string(body), "", "https://example.test")
	if response.Code != 202 {
		t.Fatal(response.Code, response.Body.String())
	}
	<-started
	response = serveAdminCommentAPI(h, cookie, owner.CSRFToken, "POST", "https://example.test/api/v1/admin/updates/start", `{"version":"1.0.0","backup_confirmed":true}`, "", "https://example.test")
	if response.Code != 409 {
		t.Fatal("core update overlapped extension job", response.Code, response.Body.String())
	}
	job := service.Job("theme")
	cancel, _ := json.Marshal(map[string]any{"id": job.ID, "expected_user_id": owner.UserID})
	response = serveAdminCommentAPI(h, cookie, owner.CSRFToken, "POST", "https://example.test/api/v1/admin/extensions/theme/cancel", string(cancel), "", "https://example.test")
	if response.Code != 202 {
		t.Fatal(response.Code, response.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for service.Busy() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if service.Busy() {
		t.Fatal("cancel did not finish")
	}
	for _, state := range []update.State{
		{Phase: "downloading"},
		{Phase: "completed", ProtectedUntil: time.Now().Add(time.Minute)},
	} {
		coreState, _ := json.Marshal(state)
		if err := os.WriteFile(filepath.Join(coreRoot, "state.json"), coreState, 0600); err != nil {
			t.Fatal(err)
		}
		response = serveAdminCommentAPI(h, cookie, owner.CSRFToken, "POST", "https://example.test/api/v1/admin/extensions/theme/check", string(body), "", "https://example.test")
		if response.Code != 409 {
			t.Fatal("extension job overlapped core update", response.Code, response.Body.String())
		}
		legacyInstall, _ := json.Marshal(map[string]any{"action": "install", "slug": plugin.CommentOrigin, "expected_user_id": owner.UserID})
		response = serveAdminCommentAPI(h, cookie, owner.CSRFToken, "POST", "https://example.test/api/v1/admin/plugins", string(legacyInstall), "", "https://example.test")
		if response.Code != 409 {
			t.Fatal("legacy plugin installation overlapped core update", response.Code, response.Body.String())
		}
	}
}
