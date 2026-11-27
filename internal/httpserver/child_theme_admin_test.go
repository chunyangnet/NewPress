package httpserver

import (
	"newpress.local/newpress/internal/extensionupdate"
	"newpress.local/newpress/internal/plugin"
	"path/filepath"
	"strings"
	"testing"
)

func TestChildThemeAdminProvenance(t *testing.T) {
	for _, parent := range []string{"starter", "chunnel"} {
		t.Run(parent, func(t *testing.T) {
			s, h, _, _, cookie, _ := newPersistentAdminTestServer(t)
			s.themes, _ = isolatedChildThemes(t, parent)
			service, err := extensionupdate.New(filepath.Join(t.TempDir(), "jobs.json"), &httpExtensionMarket{view: plugin.MarketView{}}, s.plugins, s.themes)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(service.Close)
			s.SetExtensionUpdates(service)
			paths := []string{"/admin/appearance/", "/admin/themes/", "/admin/appearance/updates/"}
			if parent == "chunnel" {
				paths = append(paths, "/admin/appearance/layout/")
			}
			for _, path := range paths {
				w := serveCookieRequest(h, "GET", "https://example.test"+path, cookie, nil)
				if w.Code != 200 {
					t.Fatalf("%s: %d %.300s", path, w.Code, w.Body.String())
				}
				body := w.Body.String()
				want := "data-theme-settings-identity"
				if path == "/admin/themes/" {
					want = `id="theme-` + parent + `"`
				}
				if path == "/admin/appearance/updates/" {
					want = "data-dependency-impact"
				}
				if !strings.Contains(body, want) {
					t.Fatalf("%s missing %s", path, want)
				}
				if path == "/admin/appearance/" && (!strings.Contains(body, "data-setting-source") || !strings.Contains(body, "父主题设置不会改变")) {
					t.Fatal("settings provenance/reset missing")
				}
			}
		})
	}
}
