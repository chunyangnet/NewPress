package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"newpress.local/newpress/internal/coreui"
	"regexp"
	"strings"
	"testing"
)

func TestThemePaletteFollowsAdministratorSettings(t *testing.T) {
	t.Parallel()
	server, handler, _, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	defaults := map[string]string{"accent": "#2563eb", "accent_strong": "#1d4ed8", "focus_color": "#2563eb", "paper_color": "#f7f9fc", "ink_color": "#17253d", "highlight_color": "#3b82f6"}
	for key, want := range defaults {
		if got := server.themes.SettingsSnapshot().Values[key]; got != want {
			t.Fatalf("default %s = %v, want %s", key, got, want)
		}
	}
	for _, style := range []string{"classic", "liquid-glass"} {
		form := url.Values{"action": {"save"}, "csrf_token": {owner.CSRFToken}, "revision": {server.themes.SettingsSnapshot().Revision}}
		for key, value := range server.themes.SettingsSnapshot().Values {
			form.Set(key, fmt.Sprint(value))
		}
		form.Set("visual_style", style)
		palette := map[string]string{"accent": "#0f766e", "accent_strong": "#115e59", "focus_color": "#7c3aed", "paper_color": "#f4f8fb", "ink_color": "#162a38", "highlight_color": "#0ea5e9"}
		for key, value := range palette {
			form.Set(key, value)
		}
		saved := serveContentForm(handler, ownerCookie, "https://example.test/admin/appearance/", "https://example.test", form)
		if saved.Code != http.StatusSeeOther {
			t.Fatalf("save palette for %s: HTTP %d", style, saved.Code)
		}
		if err := server.themes.Load(); err != nil {
			t.Fatal(err)
		}
		snapshot := server.themes.SettingsSnapshot()
		for key, want := range palette {
			if got := snapshot.Values[key]; got != want {
				t.Fatalf("reloaded %s = %v, want %s", key, got, want)
			}
		}
		cssPath := "/theme-settings.css?v=" + server.themes.SettingsCSSSnapshot().Revision
		cssResponse := httptest.NewRecorder()
		handler.ServeHTTP(cssResponse, httptest.NewRequest(http.MethodGet, cssPath, nil))
		for _, declaration := range []string{"--cyan:#0f766e;", "--cyan-dark:#115e59;", "--focus:#7c3aed;", "--paper:#f4f8fb;", "--ink:#162a38;", "--orange:#0ea5e9;"} {
			if cssResponse.Code != http.StatusOK || !strings.Contains(cssResponse.Body.String(), declaration) {
				t.Fatalf("%s settings CSS missing %q (HTTP %d)", style, declaration, cssResponse.Code)
			}
		}
		for _, target := range []string{"/", "/explore/", "/admin/login/", "/admin/", "/admin/appearance/"} {
			request := httptest.NewRequest(http.MethodGet, "https://example.test"+target, nil)
			if target == "/admin/" || target == "/admin/appearance/" {
				request.AddCookie(ownerCookie)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			pageCSS := cssPath
			if strings.HasPrefix(target, "/admin/") {
				pageCSS = coreui.AssetURL("css/core-defaults.css")
				if strings.Contains(body, cssPath) {
					t.Fatal("admin inherits active theme CSS")
				}
			}
			settingsLink := `<link rel="stylesheet" href="` + pageCSS + `">`
			if response.Code != http.StatusOK || strings.Count(body, settingsLink) != 1 {
				t.Fatalf("%s must load saved %s palette exactly once (HTTP %d)", target, style, response.Code)
			}
			stylesheets := regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)">`).FindAllStringSubmatch(body, -1)
			paletteIndex := -1
			for index, link := range stylesheets {
				if link[0] == settingsLink {
					paletteIndex = index
					continue
				}
				isColorMode := strings.Contains(link[1], "/css/chunnel-color-mode.css?")
				if paletteIndex >= 0 && !isColorMode {
					t.Fatalf("%s must load %s palette after theme styles", target, style)
				}
				if isColorMode && paletteIndex < 0 {
					t.Fatalf("%s must apply color mode after the saved palette", target)
				}
			}
			if target == "/admin/appearance/" {
				for key, value := range palette {
					if !strings.Contains(body, `type="color" name="`+key+`" value="`+value+`"`) {
						t.Fatalf("appearance form missing saved %s color", key)
					}
				}
			}
		}
	}
	reset := serveContentForm(handler, ownerCookie, "https://example.test/admin/appearance/", "https://example.test", url.Values{
		"action": {"reset"}, "csrf_token": {owner.CSRFToken},
	})
	if reset.Code != http.StatusSeeOther {
		t.Fatalf("reset palette: HTTP %d", reset.Code)
	}
	for key, want := range defaults {
		if got := server.themes.SettingsSnapshot().Values[key]; got != want {
			t.Fatalf("reset %s = %v, want %s", key, got, want)
		}
	}
}
