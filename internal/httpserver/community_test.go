package httpserver

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

func TestCommunityPublicPagination(t *testing.T) {
	t.Parallel()
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	repository := content.NewMemoryRepository(false)
	server := New(config.Config{
		SiteTitle: "NewPress", PublicURL: "https://example.test", ActiveTheme: "chunnel",
	}, repository, themes, plugin.NewManager(t.TempDir()))
	handler := server.routes()
	get := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil))
		return response
	}
	postIDs := regexp.MustCompile(`data-post-id="([0-9]+)"`)
	empty := get("/community/")
	if empty.Code != http.StatusOK || postIDs.MatchString(empty.Body.String()) || strings.Contains(empty.Body.String(), "data-community-page href=") {
		t.Fatalf("empty community status = %d, body = %s", empty.Code, empty.Body)
	}
	if response := get("/community/?page=2"); response.Code != http.StatusFound || response.Header().Get("Location") != "/community/#questions" {
		t.Fatalf("empty page redirect = %d, %q", response.Code, response.Header().Get("Location"))
	}
	var ids []string
	publishedAt := time.Now().Add(-time.Hour)
	for index := 0; index < 17; index++ {
		post, err := repository.Create(context.Background(), content.PostInput{
			Type: content.TypePost, Status: content.StatusPublish, Slug: fmt.Sprintf("community-%d", index),
			Title: fmt.Sprintf("Discussion %d", index), ContentSource: "Public discussion", PublishedAt: publishedAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, strconv.FormatInt(post.ID, 10))
	}
	slices.Reverse(ids)
	for _, status := range []string{content.StatusDraft, content.StatusPrivate, content.StatusFuture} {
		if _, err := repository.Create(context.Background(), content.PostInput{
			Type: content.TypePost, Status: status, Slug: "hidden-" + status, Title: "hidden-" + status,
			ContentSource: "Hidden content", PublishedAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name, path string
		ids        []string
		links      []string
	}{
		{"first", "/community/", ids[:8], []string{"/community/?page=2#questions"}},
		{"second with filters", "/community/?page=2&planet=design&discussion=liked&discussion_sort=oldest", ids[8:16], []string{
			"/community/?discussion=liked&discussion_sort=oldest&planet=design#questions",
			"/community/?discussion=liked&discussion_sort=oldest&page=3&planet=design#questions",
		}},
		{"last", "/community/?page=3", ids[16:], []string{"/community/?page=2#questions"}},
		{"legacy directory link", "/community/?view=all", ids[:8], []string{"/community/?page=2#questions"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := get(test.path)
			body := response.Body.String()
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, body)
			}
			var actual []string
			for _, match := range postIDs.FindAllStringSubmatch(body, -1) {
				actual = append(actual, match[1])
			}
			if !slices.Equal(actual, test.ids) {
				t.Fatalf("page IDs = %v, want %v", actual, test.ids)
			}
			if got := strings.Count(body, "data-community-page href="); got != len(test.links) {
				t.Fatalf("pagination links = %d, want %d", got, len(test.links))
			}
			for _, link := range test.links {
				if !strings.Contains(body, `data-community-page href="`+html.EscapeString(link)+`"`) {
					t.Errorf("missing pagination link %q", link)
				}
			}
		})
	}
	pageTwo := get("/community/?page=2&discussion=mine")
	if !strings.Contains(pageTwo.Body.String(), `<link rel="canonical" href="https://example.test/community/?page=2">`) {
		t.Error("community history page does not have its own canonical URL")
	}
	response := get("/community/?page=100000&planet=design&discussion=mine&discussion_sort=oldest")
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/community/?discussion=mine&discussion_sort=oldest&page=3&planet=design#questions" {
		t.Fatalf("last page redirect = %d, %q", response.Code, response.Header().Get("Location"))
	}
	for _, query := range []string{"page=0", "page=-1", "page=1.5", "page=100001", "page=abc", "page=1&page=2", "planet=design&planet=local", "discussion=all&discussion=mine", "unknown=value", "page=%ZZ", "page=1;planet=design"} {
		if response := get("/community/?" + query); response.Code != http.StatusBadRequest {
			t.Errorf("invalid query %q returned %d", query, response.Code)
		}
	}
}
