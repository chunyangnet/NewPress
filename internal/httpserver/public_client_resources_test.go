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

func TestPublicPageClientLoadsOnceBeforeRecognition(t *testing.T) {
	clientScript := regexp.MustCompile(`<script src="[^"]*/js/newpress-client\.js[^\"]*"`)
	for _, slug := range []string{"chunnel", "starter"} {
		t.Run(slug, func(t *testing.T) {
			s, _, _, repo, cookie, owner := newPersistentAdminTestServer(t)
			commerceHTTPStore(t, s)
			s.themes = theme.NewManager(filepath.Join("..", "..", "themes"), slug, theme.WithSettingsDir(t.TempDir()))
			if err := s.themes.LoadExplicit(slug); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.Create(t.Context(), content.PostInput{Title: "公开资源回归", Slug: "public-client-once", AuthorID: owner.UserID, AuthorName: owner.DisplayName, Status: content.StatusPublish}); err != nil {
				t.Fatal(err)
			}
			paths := []string{"/account/", "/billing/"}
			if slug == "chunnel" {
				paths = append(paths, "/posts/public-client-once", fmt.Sprintf("/authors/%d/", owner.UserID))
			}
			handler := s.routes()
			for _, path := range paths {
				response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, cookie, nil)
				if response.Code != http.StatusOK {
					t.Fatalf("%s: HTTP %d", path, response.Code)
				}
				body := response.Body.String()
				matches := clientScript.FindAllStringIndex(body, -1)
				if len(matches) != 1 {
					t.Fatalf("%s loads the public client %d times", path, len(matches))
				}
				if path != "/billing/" {
					recognition := strings.Index(body, "/js/newpress-recognition.js")
					if recognition < matches[0][1] {
						t.Fatalf("%s must initialize the client before recognition", path)
					}
				}
			}
		})
	}
}
