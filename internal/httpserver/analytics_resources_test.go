package httpserver

import (
	"bytes"
	"compress/gzip"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// Include the full optional graph, even though the default page does not fetch it.
// The original 240 KiB eager JS trial target failed; only an explicit opt-in mode
// is accepted at the separately documented 304 KiB total interaction ceiling.
func TestAnalyticsCompleteInteractionResourceBudget(t *testing.T) {
	_, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	page := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/analytics/", cookie, nil)
	if page.Code != 200 {
		t.Fatal(page.Code)
	}
	initial := regexp.MustCompile(`<(?:link rel="stylesheet" href|script src)="([^"]+)"`)
	deferred := regexp.MustCompile(`data-analytics-(?:script|style)="([^"]+)"`)
	body := regexp.MustCompile(`(?s)<noscript>.*?</noscript>`).ReplaceAllString(page.Body.String(), "")
	assets := append(initial.FindAllStringSubmatch(body, -1), deferred.FindAllStringSubmatch(body, -1)...)
	if len(deferred.FindAllStringSubmatch(body, -1)) != 2 {
		t.Fatal("trial dependencies must be explicit")
	}
	for _, match := range initial.FindAllStringSubmatch(body, -1) {
		if strings.Contains(match[1], "/newpress-analytics.js") || strings.Contains(match[1], "/newpress-analytics.css") {
			t.Fatal("trial graph became an eager dependency")
		}
	}
	totals, seen := map[string]int{}, map[string]bool{}
	for _, match := range assets {
		path := html.UnescapeString(match[1])
		if seen[path] {
			t.Fatal("duplicate analytics dependency", path)
		}
		seen[path] = true
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, cookie, nil)
		if response.Code != 200 || !strings.Contains(response.Header().Get("Cache-Control"), "immutable") || response.Header().Get("ETag") == "" {
			t.Fatal("invalid versioned report asset", path, response.Code)
		}
		kind := "JS"
		if strings.Contains(response.Header().Get("Content-Type"), "text/css") {
			kind = "CSS"
		}
		size := response.Body.Len()
		if size >= 512 {
			var compressed bytes.Buffer
			writer, _ := gzip.NewWriterLevel(&compressed, 5)
			_, _ = writer.Write(response.Body.Bytes())
			_ = writer.Close()
			size = compressed.Len()
		}
		totals[kind] += size
	}
	t.Logf("Complete analytics interaction: CSS=%d JS=%d requests=%d", totals["CSS"], totals["JS"], len(assets))
	if totals["CSS"] >= 106*1024 || totals["JS"] >= 304*1024 || len(assets) > 22 {
		t.Fatal("complete analytics interaction budget exceeded", totals, len(assets))
	}
}
