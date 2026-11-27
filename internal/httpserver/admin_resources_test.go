package httpserver

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/coreui"
)

// Measure actual HTTP response bodies, not source files. The page budget and
// cache/version checks include conditionally rendered dependencies. Browser
// downloads after initialization or interaction are recorded separately.
func TestAdminResourcePageBudgets(t *testing.T) {
	_, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	resources := regexp.MustCompile(`<(?:link rel="stylesheet" href|script src)="([^"]+)"`)
	noscript := regexp.MustCompile(`(?s)<noscript>.*?</noscript>`)
	type assetMeasurement struct {
		URL      string `json:"url"`
		Kind     string `json:"kind"`
		Raw      int    `json:"raw_bytes"`
		GzipBody int    `json:"gzip_body_bytes"`
	}
	type pageMeasurement struct {
		Path       string             `json:"path"`
		Requests   int                `json:"requests"`
		CSS        int                `json:"css_gzip_body_bytes"`
		JS         int                `json:"js_gzip_body_bytes"`
		CSSLimit   int                `json:"css_limit_exclusive"`
		JSLimit    int                `json:"js_limit_exclusive"`
		MaxRequest int                `json:"max_requests"`
		Assets     []assetMeasurement `json:"assets"`
	}
	var measurements []pageMeasurement
	for _, page := range []struct {
		path, scope       string
		css, js, requests int
	}{
		{"/admin/", "", 92 * 1024, 20 * 1024, 18},
		// The continuous document editor bundles ProseMirror locally (~90 KiB gzip).
		// Its budget is page-specific; ordinary admin routes retain their caps.
		{"/admin/content/new/", "", 136 * 1024, 192 * 1024, 37},
		{"/admin/content/", "", 92 * 1024, 22 * 1024, 20},
		{"/admin/taxonomy/", "directory", 96 * 1024, 22 * 1024, 20},
		{"/admin/users/", "directory", 100 * 1024, 40 * 1024, 26},
		{"/admin/sessions/", "sessions", 94 * 1024, 20 * 1024, 19},
		{"/admin/navigation/", "", 94 * 1024, 25 * 1024, 22},
		{"/admin/plugins/", "", 96 * 1024, 36 * 1024, 24},
		{"/admin/login/", "login", 34 * 1024, 7 * 1024, 14},
		// Default report remains light; a separate test measures the full trial graph.
		{"/admin/analytics/", "", 106 * 1024, 26 * 1024, 20},
	} {
		t.Run(page.path, func(t *testing.T) {
			session := cookie
			if page.scope == "login" {
				session = nil
			}
			response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+page.path, session, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("page status = %d", response.Code)
			}
			body := noscript.ReplaceAllString(response.Body.String(), "")
			for _, scope := range []string{"directory", "sessions"} {
				present := strings.Contains(body, "chunnel-admin-"+scope+".css")
				if present != (page.scope == scope) {
					t.Errorf("%s styles present = %t", scope, present)
				}
			}
			if strings.Contains(body, "chunnel-admin.css") {
				t.Error("full legacy admin stylesheet must not load alongside the split styles")
			}
			measurement := pageMeasurement{Path: page.path, CSSLimit: page.css, JSLimit: page.js, MaxRequest: page.requests}
			seen := map[string]bool{}
			for _, match := range resources.FindAllStringSubmatch(body, -1) {
				target := html.UnescapeString(match[1])
				if seen[target] {
					t.Errorf("duplicate initial dependency: %s", target)
					continue
				}
				seen[target] = true
				asset := serveCookieRequest(handler, http.MethodGet, "https://example.test"+target, session, nil)
				if asset.Code != http.StatusOK {
					t.Fatalf("asset %s: %d", target, asset.Code)
				}
				kind := "JS"
				if strings.Contains(asset.Header().Get("Content-Type"), "text/css") {
					kind = "CSS"
					if strings.Contains(asset.Body.String(), "@import") {
						t.Error("CSS imports would escape the initial dependency budget: " + target)
					}
				}
				if strings.HasPrefix(target, "/core-assets/") && !strings.HasPrefix(target, "/core-assets/"+coreui.Revision()+"/") {
					t.Error("admin resource did not use the current core revision: " + target)
				}
				if !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") || asset.Header().Get("ETag") == "" {
					t.Error("initial asset must retain immutable versioned caching: " + target)
				}
				var compressed bytes.Buffer
				writer, err := gzip.NewWriterLevel(&compressed, 5)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(asset.Body.Bytes()); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				bodyBytes := compressed.Len()
				if asset.Body.Len() < 512 {
					bodyBytes = asset.Body.Len()
				}
				measurement.Assets = append(measurement.Assets, assetMeasurement{URL: target, Kind: kind, Raw: asset.Body.Len(), GzipBody: bodyBytes})
				measurement.Requests++
				if kind == "CSS" {
					measurement.CSS += bodyBytes
				} else {
					measurement.JS += bodyBytes
				}
			}
			t.Logf("%s: %d initial dependencies; CSS=%d B; JS=%d B gzip level 5, minimum 512 B", page.path, measurement.Requests, measurement.CSS, measurement.JS)
			if measurement.CSS >= page.css || measurement.JS >= page.js || measurement.Requests > page.requests {
				t.Errorf("initial dependency budget exceeded: CSS <%d, JS <%d, requests <=%d", page.css, page.js, page.requests)
			}
			measurements = append(measurements, measurement)
		})
	}
	if report := os.Getenv("NEWPRESS_ADMIN_RESOURCES_REPORT"); report != "" {
		if !filepath.IsAbs(report) {
			t.Fatal("resource report path must be absolute")
		}
		file, err := os.OpenFile(report, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		encodeErr := json.NewEncoder(file).Encode(measurements)
		closeErr := file.Close()
		if encodeErr != nil || closeErr != nil {
			t.Fatalf("write report: %v / %v", encodeErr, closeErr)
		}
	}
}

func TestAdminHomepageEditorStylesOnlyLoadOnOwningPages(t *testing.T) {
	_, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	for _, page := range []struct {
		path   string
		layout bool
	}{
		{"/admin/appearance/", true}, {"/admin/appearance/layout/", true},
		{"/admin/", false}, {"/admin/users/", false}, {"/admin/plugins/", false}, {"/admin/content/new/", false},
	} {
		t.Run(page.path, func(t *testing.T) {
			response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+page.path, cookie, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("page status = %d", response.Code)
			}
			body := response.Body.String()
			if strings.Contains(body, "css/chunnel-admin-layout-workspace.css") != page.layout {
				t.Error("homepage editor bundle does not match page ownership")
			}
			if strings.Contains(body, "css/chunnel-admin-workspace.css") == page.layout {
				t.Error("page must load exactly one workspace bundle")
			}
		})
	}
}
