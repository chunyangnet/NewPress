package httpserver

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"html"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/coreui"
	"newpress.local/newpress/internal/update"
)

func TestUpdatesPageScriptScopes(t *testing.T) {
	s, handler, cookie, _, _ := themeAdminFixture(t, "")
	manager, err := update.New(t.TempDir(), "0.1.0", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	s.SetUpdates(manager)
	type measurement struct {
		Path     string `json:"path"`
		Requests int    `json:"requests"`
		CSS      int    `json:"css_gzip5"`
		JS       int    `json:"js_gzip5"`
	}
	measurements := []measurement{}
	pattern := regexp.MustCompile(`<(?:link rel="stylesheet" href|script src)="([^"]+)"`)
	noscript := regexp.MustCompile(`(?s)<noscript>.*?</noscript>`)
	scripts := []string{"chunnel-updates.js", "chunnel-extension-updates.js", "chunnel-system.js", "newpress-themes.js", "chunnel-announcements.js"}
	for _, page := range []struct {
		path    string
		scripts []string
	}{
		{"/admin/updates/", []string{scripts[0]}}, {"/admin/appearance/updates/", []string{scripts[1]}}, {"/admin/plugins/updates/", []string{scripts[1]}}, {"/admin/system/", []string{scripts[2]}}, {"/admin/themes/", []string{scripts[1], scripts[3]}}, {"/admin/announcements/", []string{scripts[4]}},
	} {
		t.Run(page.path, func(t *testing.T) {
			response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+page.path, cookie, nil)
			if response.Code != 200 {
				t.Fatalf("page %d", response.Code)
			}
			body := noscript.ReplaceAllString(response.Body.String(), "")
			for _, script := range scripts {
				want := 0
				for _, required := range page.scripts {
					if required == script {
						want = 1
					}
				}
				if count := strings.Count(body, "/js/"+script); count != want {
					t.Errorf("%s loaded %d, want %d", script, count, want)
				}
			}
			item := measurement{Path: page.path}
			seen := map[string]bool{}
			for _, match := range pattern.FindAllStringSubmatch(body, -1) {
				target := html.UnescapeString(match[1])
				if seen[target] {
					t.Fatal("duplicate resource", target)
				}
				seen[target] = true
				if strings.Contains(target, "analytics-app") || strings.Contains(target, "echarts") {
					t.Fatal("interactive analytics on native page", target)
				}
				asset := serveCookieRequest(handler, "GET", "https://example.test"+target, cookie, nil)
				if asset.Code != 200 {
					t.Fatal(target, asset.Code)
				}
				if strings.HasPrefix(target, "/core-assets/") && !strings.HasPrefix(target, "/core-assets/"+coreui.Revision()+"/") {
					t.Fatal("stale revision", target)
				}
				if !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") || asset.Header().Get("ETag") == "" {
					t.Fatal("missing resource caching", target)
				}
				var compressed bytes.Buffer
				writer, _ := gzip.NewWriterLevel(&compressed, 5)
				_, _ = writer.Write(asset.Body.Bytes())
				_ = writer.Close()
				size := compressed.Len()
				if asset.Body.Len() < 512 {
					size = asset.Body.Len()
				}
				if strings.Contains(asset.Header().Get("Content-Type"), "text/css") {
					item.CSS += size
				} else {
					item.JS += size
				}
				item.Requests++
			}
			t.Logf("%s: %d dependencies CSS=%d B JS=%d B gzip5", page.path, item.Requests, item.CSS, item.JS)
			measurements = append(measurements, item)
			if page.path == "/admin/themes/" && (item.CSS >= 110*1024 || item.JS >= 26*1024) {
				t.Fatal("existing theme marketplace resource budget exceeded")
			}
		})
	}
	if path := os.Getenv("NEWPRESS_UPDATES_RESOURCES_REPORT"); path != "" {
		data, e := json.MarshalIndent(measurements, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
