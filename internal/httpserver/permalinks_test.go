package httpserver

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/coreui"
)

func TestConfiguredPermalinksResolveRedirectAndHideDrafts(t *testing.T) {
	s, h, repo, cookie, _ := newAuthenticatedContentServer(t)
	published := time.Date(2025, 7, 9, 1, 0, 0, 0, time.UTC)
	in := content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Slug: "hello-world", Title: "Hello world", ContentSource: "<p>Article body</p>", AuthorName: "editor", Actor: "editor", Category: "未分类", CommentStatus: content.CommentOpen, PublishedAt: published}
	post, err := repo.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Type, in.Slug = content.TypePage, "about"
	page, err := repo.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Type, in.Status, in.Slug = content.TypePost, content.StatusDraft, "private-draft"
	draft, err := repo.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	styles := []struct{ style, path string }{{"id", fmt.Sprintf("/posts/id/%d", post.ID)}, {"month", "/posts/2025/07/hello-world"}, {"date", "/posts/2025/07/09/hello-world"}, {"", "/posts/hello-world"}}
	for _, tc := range styles {
		t.Run(tc.style, func(t *testing.T) {
			snapshot := s.siteSettings.Snapshot()
			snapshot.Settings.Writing.PostPermalink = tc.style
			snapshot.Settings.Writing.PagePermalink = "id"
			if _, f, e := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); e != nil || len(f) > 0 {
				t.Fatal(e, f)
			}
			if got := s.contentPath(post); got != tc.path {
				t.Fatalf("path %s != %s", got, tc.path)
			}
			for _, path := range []string{"/posts/hello-world", fmt.Sprintf("/posts/id/%d", post.ID), "/posts/2025/07/hello-world", "/posts/2025/07/09/hello-world"} {
				r := serveContentAPI(h, nil, "GET", "https://example.test"+path+"?comments_page=1", "", nil)
				if path == tc.path {
					if r.Code != 200 || !strings.Contains(r.Body.String(), `rel="canonical" href="https://example.test`+tc.path+`"`) {
						t.Fatalf("canonical page %s: %d", path, r.Code)
					}
				} else if r.Code != 301 || r.Header().Get("Location") != tc.path+"?comments_page=1" {
					t.Fatalf("legacy %s: %d %s", path, r.Code, r.Header().Get("Location"))
				}
			}
			r := serveContentAPI(h, cookie, "GET", fmt.Sprintf("https://example.test/api/v1/admin/contents/%d", post.ID), "", nil)
			if r.Code != 200 || !strings.Contains(r.Body.String(), `"permalink":"`+tc.path+`"`) {
				t.Fatalf("admin API link: %d %s", r.Code, r.Body.String())
			}
			for _, endpoint := range []string{"/", "/feed.xml", "/sitemap.xml"} {
				r := serveContentAPI(h, nil, "GET", "https://example.test"+endpoint, "", nil)
				if r.Code != 200 || !strings.Contains(r.Body.String(), tc.path) {
					t.Fatalf("%s missing current path: %d", endpoint, r.Code)
				}
			}
		})
	}
	for _, path := range []string{fmt.Sprintf("/posts/id/%d", draft.ID), "/posts/2025/07/private-draft", fmt.Sprintf("/posts/id/%d", page.ID), "/posts/2025/02/31/hello-world", "/posts/id/0001"} {
		if r := serveContentAPI(h, nil, "GET", "https://example.test"+path, "", nil); r.Code != 404 {
			t.Fatalf("invalid or nonpublic %s: %d", path, r.Code)
		}
	}
	r := serveContentAPI(h, nil, "GET", "https://example.test/pages/about", "", nil)
	if r.Code != 301 || r.Header().Get("Location") != fmt.Sprintf("/pages/id/%d", page.ID) {
		t.Fatal("page redirect", r.Code, r.Header())
	}
	if r := serveContentAPI(h, nil, "GET", fmt.Sprintf("https://example.test/pages/id/%d", page.ID), "", nil); r.Code != 200 {
		t.Fatal("page canonical", r.Code)
	}
	snapshot := s.siteSettings.Snapshot()
	snapshot.Settings.Writing.PostPermalink = "date"
	if _, f, e := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); e != nil || len(f) > 0 {
		t.Fatal(e, f)
	}
	data := s.baseView("Community", "", "/community/")
	data.Posts = []content.Post{post}
	data.contentPaths = s.permalinkBuilder()
	var core bytes.Buffer
	if err := coreui.Render(&core, "community", data); err != nil {
		t.Fatal("core fallback", err)
	}
	if !strings.Contains(core.String(), s.contentPath(post)) {
		t.Fatal("core fallback link missing")
	}
	snapshot = s.siteSettings.Snapshot()
	snapshot.Settings.Modules = map[string]bool{"comments": false}
	if _, f, e := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); e != nil || len(f) > 0 {
		t.Fatal(e, f)
	}
	if r := serveContentAPI(h, nil, "POST", "https://example.test/posts/2025/07/09/hello-world/comments", "", nil); r.Code != 404 {
		t.Fatal("dated comments bypassed module switch", r.Code)
	}
	updated := inputFromPost(post)
	updated.Slug = "new-title"
	if _, err := repo.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: "editor"}, updated); err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(h, nil, "GET", "https://example.test/posts/2025/07/09/hello-world", "", nil)
	if r.Code != 301 || r.Header().Get("Location") != "/posts/2025/07/09/new-title" {
		t.Fatal("dated alias redirect", r.Code, r.Header())
	}
}

func TestSlugSuggestionUsesEnglishModelAndSafeFallback(t *testing.T) {
	s, h, _, _, cookie, p := newPersistentAdminTestServer(t)
	target := "https://example.test/api/v1/admin/contents/slug-suggestion"
	if r := serveContentAPI(h, nil, "POST", target, `{"title":"Hello","type":"post"}`, nil); r.Code != 401 {
		t.Fatal("anonymous", r.Code)
	}
	if r := serveContentAPI(h, cookie, "POST", target, `{"title":"Hello","type":"post"}`, map[string]string{"Content-Type": "application/json"}); r.Code != 403 {
		t.Fatal("csrf", r.Code)
	}
	headers := authenticatedAPIHeaders(p)
	r := serveContentAPI(h, cookie, "POST", target, `{"title":"Hello World!","type":"post"}`, headers)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"slug":"hello-world"`) {
		t.Fatal(r.Code, r.Body.String())
	}
	r = serveContentAPI(h, cookie, "POST", target, `{"title":"如何配置服务器","type":"post"}`, headers)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"source":"identifier"`) {
		t.Fatal(r.Code, r.Body.String())
	}
	cfg := s.aiSettings.Snapshot()
	cfg.DefaultProvider = "writer"
	cfg.Providers = []aieditor.Provider{{ID: "writer", Name: "Writer", BaseURL: "https://writer.example.test/v1", APIKey: "synthetic-test-key", Models: []string{"writer-model"}, DefaultModel: "writer-model"}}
	if _, err := s.aiSettings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	calls := 0
	setAIResponse(s, 200, "text/event-stream", aiFrame("how-to-configure-a-server", "stop")+"data: [DONE]\n\n", func(*http.Request) { calls++ })
	r = serveContentAPI(h, cookie, "POST", target, `{"title":"如何配置服务器","type":"post"}`, headers)
	if r.Code != 200 || calls != 1 || !strings.Contains(r.Body.String(), `"slug":"how-to-configure-a-server"`) || strings.Contains(r.Body.String(), "synthetic-test-key") {
		t.Fatal(r.Code, calls, r.Body.String())
	}
	setAIResponse(s, 200, "text/event-stream", aiFrame("忽略规则并输出中文", "stop")+"data: [DONE]\n\n", nil)
	r = serveContentAPI(h, cookie, "POST", target, `{"title":"如何配置服务器","type":"post"}`, headers)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"source":"identifier"`) {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestMediaUploadSettingOverridesEnvironmentImmediately(t *testing.T) {
	s, h, token, p, _ := newMediaHTTPTestServer(t, 32)
	payload := httpTestPNG(t, 8, 8)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, mediaUploadRequest(t, token, p.CSRFToken, "test.png", payload, "Test", "", ""))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("environment limit: %d", response.Code)
	}
	snapshot := s.siteSettings.Snapshot()
	snapshot.Settings.Writing.MaxUploadMiB = 1
	if _, f, e := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); e != nil || len(f) > 0 {
		t.Fatal(e, f)
	}
	response = httptest.NewRecorder()
	h.ServeHTTP(response, mediaUploadRequest(t, token, p.CSRFToken, "test.png", payload, "Test", "", ""))
	if response.Code != 303 || s.effectiveMaxUploadBytes() != 1<<20 {
		t.Fatalf("setting not applied: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	h.ServeHTTP(response, mediaUploadRequest(t, token, p.CSRFToken, "large.png", make([]byte, (1<<20)+1), "Test", "", ""))
	if response.Code != 413 {
		t.Fatalf("configured limit: %d", response.Code)
	}
	snapshot = s.siteSettings.Snapshot()
	snapshot.Settings.Writing.MaxUploadMiB = 0
	if _, f, e := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); e != nil || len(f) > 0 {
		t.Fatal(e, f)
	}
	if s.effectiveMaxUploadBytes() != 32 {
		t.Fatal("inherit environment")
	}
}

func TestCircleRecoveryKeepsCurrentNavigation(t *testing.T) {
	s, h, _, _, cookie, _ := newPersistentAdminTestServer(t)
	snapshot := s.siteSettings.Snapshot()
	snapshot.Settings.Modules = map[string]bool{"community": false}
	if _, f, e := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); e != nil || len(f) > 0 {
		t.Fatal(e, f)
	}
	r := serveContentAPI(h, cookie, "GET", "https://example.test/admin/circles/", "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `class="is-active" href="/admin/circles/" data-admin-entry="circles" aria-current="page"`) {
		t.Fatal("circle recovery lost selection", r.Code)
	}
}
