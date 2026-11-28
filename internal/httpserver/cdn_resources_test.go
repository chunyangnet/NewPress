package httpserver

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"html"
	"os"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/coreui"
)

func TestCDNPageScriptScopes(t *testing.T) {
	_, handler, cookie, _ := newCDNHTTP(t)
	pattern := regexp.MustCompile(`<(?:link rel="stylesheet" href|script src)="([^"]+)"`)
	noscript := regexp.MustCompile(`(?s)<noscript>.*?</noscript>`)
	results := []map[string]any{}
	for _, path := range []string{"/admin/plugins/cdn/", "/admin/plugins/"} {
		t.Run(path, func(t *testing.T) {
			response := serveCookieRequest(handler, "GET", "https://example.test"+path, cookie, nil)
			if response.Code != 200 {
				t.Fatal(response.Code)
			}
			body := noscript.ReplaceAllString(response.Body.String(), "")
			want := 0
			if path == "/admin/plugins/cdn/" {
				want = 1
			}
			for _, script := range []string{"chunnel-cdn.js", "chunnel-cdn-routing.js"} {
				if strings.Count(body, "/js/"+script) != want {
					t.Fatal("wrong CDN script scope", script)
				}
			}
			css, js := 0, 0
			seen := map[string]bool{}
			for _, match := range pattern.FindAllStringSubmatch(body, -1) {
				target := html.UnescapeString(match[1])
				if seen[target] {
					t.Fatal("duplicate asset", target)
				}
				seen[target] = true
				if strings.Contains(target, "analytics-app") || strings.Contains(target, "echarts") {
					t.Fatal("unexpected interactive analytics", target)
				}
				asset := serveCookieRequest(handler, "GET", "https://example.test"+target, cookie, nil)
				if asset.Code != 200 {
					t.Fatal(target, asset.Code)
				}
				if strings.HasPrefix(target, "/core-assets/") && !strings.HasPrefix(target, "/core-assets/"+coreui.Revision()+"/") {
					t.Fatal("stale resource revision", target)
				}
				if !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") || asset.Header().Get("ETag") == "" {
					t.Fatal("missing asset cache contract", target)
				}
				var buffer bytes.Buffer
				writer, _ := gzip.NewWriterLevel(&buffer, 5)
				_, _ = writer.Write(asset.Body.Bytes())
				_ = writer.Close()
				size := buffer.Len()
				if asset.Body.Len() < 512 {
					size = asset.Body.Len()
				}
				if strings.Contains(asset.Header().Get("Content-Type"), "text/css") {
					css += size
				} else {
					js += size
				}
			}
			t.Logf("%s dependencies=%d CSS=%d B JS=%d B gzip5", path, len(seen), css, js)
			results = append(results, map[string]any{"path": path, "requests": len(seen), "css_gzip5": css, "js_gzip5": js})
		})
	}
	if path := os.Getenv("NEWPRESS_CDN_RESOURCES_REPORT"); path != "" {
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
