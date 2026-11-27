package httpserver

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
)

// Measure actual rendered dependencies, including dynamic navigation/theme CSS.
// Budgets use deployment gzip level 5; they are not claims about real-user LCP.
func TestPrelaunchPageResourceBudgets(t *testing.T) {
	_, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	pattern := regexp.MustCompile(`<(?:link rel="stylesheet" href|script src)="([^"]+)"`)
	noscript := regexp.MustCompile(`(?s)<noscript>.*?</noscript>`)
	for _, page := range []struct {
		path    string
		css, js int
	}{
		{"/", 27 * 1024, 7 * 1024},
		{"/explore/", 96 * 1024, 65 * 1024},
		{"/admin/", 104 * 1024, 20 * 1024},
		// Includes the locally bundled rich-document engine, only on this route.
		{"/admin/content/new/", 136 * 1024, 192 * 1024},
		{"/admin/navigation/", 106 * 1024, 25 * 1024},
		{"/admin/themes/", 110 * 1024, 26 * 1024},
	} {
		t.Run(page.path, func(t *testing.T) {
			response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+page.path, cookie, nil)
			if response.Code != 200 {
				t.Fatalf("page status = %d", response.Code)
			}
			body := noscript.ReplaceAllString(response.Body.String(), "")
			sizes := map[string]int{}
			consentSizes := map[string]int{}
			raw := map[string]int{}
			count := 0
			seen := map[string]bool{}
			for _, match := range pattern.FindAllStringSubmatch(body, -1) {
				target := html.UnescapeString(match[1])
				if seen[target] {
					t.Errorf("duplicate eager resource %s", target)
					continue
				}
				seen[target] = true
				asset := serveCookieRequest(handler, http.MethodGet, "https://example.test"+target, cookie, nil)
				if asset.Code != 200 {
					t.Fatalf("asset %s: %d", target, asset.Code)
				}
				kind := "JS"
				if strings.Contains(asset.Header().Get("Content-Type"), "text/css") {
					kind = "CSS"
				}
				var compressed bytes.Buffer
				writer, _ := gzip.NewWriterLevel(&compressed, 5)
				_, _ = writer.Write(asset.Body.Bytes())
				_ = writer.Close()
				sizes[kind] += compressed.Len()
				// Match TestHomepageResourceBudget: the optional consent UI has
				// its own 2 KiB allowance, while the shell keeps its existing cap.
				if page.path == "/" && (strings.Contains(target, "/css/cookie-consent.css?") || strings.Contains(target, "/js/cookie-consent.js?")) {
					consentSizes[kind] += compressed.Len()
				}
				raw[kind] += asset.Body.Len()
				count++
			}
			t.Logf("%s: %d requests; CSS raw=%d gzip=%d; JS raw=%d gzip=%d", page.path, count, raw["CSS"], sizes["CSS"], raw["JS"], sizes["JS"])
			if page.path == "/" {
				for _, kind := range []string{"CSS", "JS"} {
					t.Logf("%s: shell=%d B; consent=%d B", kind, sizes[kind]-consentSizes[kind], consentSizes[kind])
					if consentSizes[kind] == 0 || consentSizes[kind] >= 2*1024 {
						t.Errorf("%s consent resources missing or exceed their 2 KiB budget: %d", kind, consentSizes[kind])
					}
				}
			}
			if sizes["CSS"]-consentSizes["CSS"] >= page.css || sizes["JS"]-consentSizes["JS"] >= page.js {
				t.Errorf("resource budget exceeded (CSS < %d; JS < %d)", page.css, page.js)
			}
			// Optional local HTML snapshots are safe fixtures without account secrets.
			if directory := os.Getenv("NEWPRESS_AUDIT_OUTPUT"); directory != "" {
				name := strings.Trim(strings.ReplaceAll(page.path, "/", "-"), "-")
				if name == "" {
					name = "home"
				}
				if err := os.MkdirAll(directory, 0750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, name+".html"), response.Body.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
				for _, target := range []string{"/theme-settings.css", "/navigation.css"} {
					asset := serveCookieRequest(handler, http.MethodGet, "https://example.test"+target, nil, nil)
					if err := os.WriteFile(filepath.Join(directory, strings.TrimPrefix(target, "/")), asset.Body.Bytes(), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestTemporaryPasswordCapacityHasShortRetry(t *testing.T) {
	server, _, _, _, _, principal := newPersistentAdminTestServer(t)
	request := httptest.NewRequest("POST", "https://example.test/admin/users/", nil)
	response := httptest.NewRecorder()
	server.handleUserMutationError(response, request, principal, "create", adminUserForm{}, auth.ErrPasswordBusy)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "1" {
		t.Fatal("unexpected capacity response", response.Code, response.Header())
	}
	for _, tc := range []struct {
		err   error
		retry string
	}{{auth.ErrPasswordBusy, "1"}, {auth.ErrRateLimited, "60"}} {
		t.Run(fmt.Sprint(tc.err), func(t *testing.T) {
			response := httptest.NewRecorder()
			server.accessError(response, request, tc.err)
			if response.Code != 429 || response.Header().Get("Retry-After") != tc.retry {
				t.Fatal("unexpected access retry", response.Code, response.Header())
			}
		})
	}
}
