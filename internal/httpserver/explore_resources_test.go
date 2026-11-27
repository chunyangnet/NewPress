package httpserver

import (
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestExploreRouteResourceBoundaries(t *testing.T) {
	for _, variant := range []string{"chunnel", "core"} {
		t.Run(variant, func(t *testing.T) {
			s, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
			if variant == "core" {
				s.themes = boundaryTheme(t, "independent", "body{color:red}", "independent")
			}
			for _, route := range []struct{ path, entry string }{{"/explore/", "chunnel-explore.js"}, {"/explore/?sort=updated", "chunnel-explore.js"}, {"/community/", "chunnel-experience.js"}} {
				response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+route.path, cookie, nil)
				if response.Code != 200 {
					t.Fatal(route.path, response.Code)
				}
				body := response.Body.String()
				pattern := regexp.MustCompile(`<script src="([^"]+/(chunnel-explore|chunnel-experience)\.js[^\"]*)"`)
				matches := pattern.FindAllStringSubmatch(body, -1)
				if len(matches) != 1 || !strings.Contains(matches[0][1], "/"+route.entry) {
					t.Fatalf("%s: route entry = %v", route.path, matches)
				}
				url := html.UnescapeString(matches[0][1])
				asset := serveCookieRequest(handler, http.MethodGet, "https://example.test"+url, nil, nil)
				if asset.Code != 200 || !strings.Contains(asset.Header().Get("Content-Type"), "javascript") {
					t.Fatal(url, asset.Code)
				}
				if variant == "core" && !strings.HasPrefix(url, "/core-assets/") {
					t.Fatal("core fallback must own its route asset", url)
				}
			}
		})
	}
}
