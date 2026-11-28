package httpserver

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/theme"
)

// An unfiltered public/v1 listing must show the same collection as an explicit
// sort. The Chunnel directory and absent-template core fallback have separate
// coverage in TestExploreDirectoryConfigurationAndEmptyMenu and resource tests.
func TestPublicThemeDiscoveryIncludesUnfilteredPosts(t *testing.T) {
	for _, slug := range []string{"docshub", "nexora"} {
		t.Run(slug, func(t *testing.T) {
			s, _, _, repo, _, owner := newPersistentAdminTestServer(t)
			s.themes = theme.NewManager(filepath.Join("..", "..", "themes"), slug, theme.WithSettingsDir(t.TempDir()))
			if err := s.themes.LoadExplicit(slug); err != nil {
				t.Fatal(err)
			}
			if s.themes.Active().Slug != slug {
				t.Fatal("wrong active test theme")
			}
			limit := s.siteSettings.Snapshot().Settings.Reading.ExplorePosts
			for i := 0; i < limit+1; i++ {
				if _, err := repo.Create(t.Context(), content.PostInput{Title: fmt.Sprintf("公开主题文档 %d", i), Slug: fmt.Sprintf("public-theme-discovery-%d", i), AuthorID: owner.UserID, Status: content.StatusPublish, Category: "阅读"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := repo.Create(t.Context(), content.PostInput{Title: "private-theme-discovery-marker", Slug: "hidden-theme-discovery", Status: content.StatusPrivate}); err != nil {
				t.Fatal(err)
			}
			handler := s.routes()
			articleLinks := regexp.MustCompile(`href="/posts/public-theme-discovery-[0-9]+"`)
			for _, tc := range []struct {
				path  string
				count int
			}{{"/explore/", limit}, {"/explore/?sort=updated", limit}, {"/explore/?page=2", 1}, {"/explore/?q=private-theme-discovery-marker", 0}} {
				response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+tc.path, nil, nil)
				if response.Code != http.StatusOK {
					t.Fatalf("%s: HTTP %d", tc.path, response.Code)
				}
				body := response.Body.String()
				found := map[string]bool{}
				for _, link := range articleLinks.FindAllString(body, -1) {
					found[link] = true
				}
				if len(found) != tc.count {
					t.Errorf("%s: public articles=%d, want %d", tc.path, len(found), tc.count)
				}
				if strings.Contains(body, `href="/posts/hidden-theme-discovery"`) {
					t.Fatal("private article leaked into listing")
				}
				if tc.path == "/explore/" && !strings.Contains(body, "page=2") {
					t.Fatal("unfiltered list has no second-page link")
				}
			}
		})
	}
}
