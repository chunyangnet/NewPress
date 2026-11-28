package httpserver

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Record initial CSS and the additional dialog stylesheet so moving rules out
// of the first response does not hide the cost of opening an interaction.
func TestHomepageOverlayResourceContract(t *testing.T) {
	server, handler, _, _, _, _ := newPersistentAdminTestServer(t)
	links := regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)"`)
	noscript := regexp.MustCompile(`(?s)<noscript>.*?</noscript>`)
	type assetReport struct {
		URL    string `json:"url"`
		Gzip   int    `json:"gzip_body_bytes"`
		SHA256 string `json:"sha256"`
		ETag   string `json:"etag"`
	}
	type styleReport struct {
		Style       string        `json:"style"`
		Initial     int           `json:"initial_css"`
		Native      int           `json:"nojs_css"`
		Consent     int           `json:"consent_css"`
		WithDialogs int           `json:"css_with_dialogs"`
		Requests    int           `json:"initial_css_requests"`
		Overlay     assetReport   `json:"overlay"`
		Assets      []assetReport `json:"initial_assets"`
	}
	measure := func(target string) assetReport {
		t.Helper()
		target = html.UnescapeString(target)
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+target, nil, nil)
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "text/css") {
			t.Fatalf("CSS %s status %d", target, response.Code)
		}
		data := response.Body.Bytes()
		size := len(data)
		if size >= 512 {
			var compressed bytes.Buffer
			writer, err := gzip.NewWriterLevel(&compressed, 5)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			size = compressed.Len()
		}
		hash := sha256.Sum256(data)
		return assetReport{target, size, hex.EncodeToString(hash[:]), response.Header().Get("ETag")}
	}
	var reports []styleReport
	for _, style := range []string{"classic", "liquid-glass"} {
		values := server.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		if _, fields, err := server.themes.SaveSettings(values); err != nil || len(fields) != 0 {
			t.Fatalf("set style: %v %v", err, fields)
		}
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test/", nil, nil)
		if response.Code != http.StatusOK {
			t.Fatal(response.Code)
		}
		body := response.Body.String()
		if strings.Contains(body, "chunnel-home-overlays") {
			t.Fatal("dialog styles must wait for explicit interaction")
		}
		report := styleReport{Style: style}
		seen := make(map[string]bool)
		for _, link := range links.FindAllStringSubmatch(noscript.ReplaceAllString(body, ""), -1) {
			asset := measure(link[1])
			if seen[asset.URL] {
				t.Fatal("duplicate stylesheet: " + asset.URL)
			}
			seen[asset.URL] = true
			report.Assets = append(report.Assets, asset)
			report.Requests++
			if strings.Contains(asset.URL, "/cookie-consent.css") {
				report.Consent += asset.Gzip
			} else {
				report.Initial += asset.Gzip
			}
			if strings.Contains(asset.URL, "/chunnel-home.css") || strings.Contains(asset.URL, "/chunnel-home-glass.css") {
				url := strings.Replace(asset.URL, "chunnel-home", "chunnel-home-overlays", 1)
				report.Overlay = measure(url)
				cached := serveCookieRequest(handler, http.MethodGet, "https://example.test"+url, nil, nil)
				if report.Overlay.ETag == "" || !strings.Contains(cached.Header().Get("Cache-Control"), "immutable") {
					t.Fatal("overlay CSS must have versioned immutable caching")
				}
			}
		}
		for _, fallback := range noscript.FindAllString(body, -1) {
			for _, link := range links.FindAllStringSubmatch(fallback, -1) {
				report.Native += measure(link[1]).Gzip
			}
		}
		report.Native += report.Initial
		report.WithDialogs = report.Initial + report.Overlay.Gzip
		if report.Overlay.Gzip == 0 || report.Initial >= 27648 || report.Native >= 27648 {
			t.Fatalf("original CSS budget or missing overlay: %+v", report)
		}
		t.Logf("%s: initial CSS=%d B; no JS=%d B; optional dialogs=%d B; with dialogs=%d B; consent=%d B separately", style, report.Initial, report.Native, report.Overlay.Gzip, report.WithDialogs, report.Consent)
		reports = append(reports, report)
	}
	if filename := os.Getenv("NEWPRESS_HOME_OVERLAY_RESOURCES_REPORT"); filename != "" {
		if !filepath.IsAbs(filename) {
			t.Fatal("report path must be absolute")
		}
		file, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		encodeErr := json.NewEncoder(file).Encode(reports)
		closeErr := file.Close()
		if encodeErr != nil || closeErr != nil {
			t.Fatalf("resource report: %v / %v", encodeErr, closeErr)
		}
	}
}
