package httpserver

import (
	"testing"

	"newpress.local/newpress/internal/content"
)

func TestFlashRedirectHTTPVisibilityAndModuleGate(t *testing.T) {
	s, handler, _, repo, _, _ := newPersistentAdminTestServer(t)
	input := content.PostInput{Type: content.TypeFlash, Title: "Flash redirect", Slug: "flash-before", ContentSource: "Flash body", Status: content.StatusPublish}
	post, err := repo.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveRedirect(t.Context(), 0, content.Mutation{}, content.RedirectInput{Path: "/old-news-entry/", PostID: post.ID}); err != nil {
		t.Fatal(err)
	}
	input.Slug = "flash-after"
	post, err = repo.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		for _, path := range []string{"/flash/flash-before/", "/flash/flash-before/?secret=discarded", "/old-news-entry/"} {
			r := serveContentAPI(handler, nil, method, "https://example.test"+path, "", nil)
			if r.Code != 301 || r.Header().Get("Location") != content.PublicPath(post) || r.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("old flash link %s %s: status=%d location=%q cache=%q", method, path, r.Code, r.Header().Get("Location"), r.Header().Get("Cache-Control"))
			}
		}
	}
	if r := serveContentAPI(handler, nil, "GET", "https://example.test"+content.PublicPath(post)+"?unexpected=1", "", nil); r.Code != 400 {
		t.Errorf("canonical flash accepted an unknown query: %d", r.Code)
	}
	for _, path := range []string{"/flash/flash-before/comments", "/api/v1/flash/flash-before/comments"} {
		r := serveContentAPI(handler, nil, "POST", "https://example.test"+path, "", nil)
		if r.Code == 301 || r.Header().Get("Location") != "" {
			t.Errorf("write route redirected: %s %d %v", path, r.Code, r.Header())
		}
	}
	r := serveContentAPI(handler, nil, "GET", "https://example.test/flash%2fflash-before/", "", nil)
	if r.Code != 404 || r.Header().Get("Location") != "" {
		t.Errorf("encoded separator redirected: %d %v", r.Code, r.Header())
	}
	for _, enabled := range []bool{false, true} {
		snapshot := s.siteSettings.Snapshot()
		if snapshot.Settings.Modules == nil {
			snapshot.Settings.Modules = map[string]bool{}
		}
		snapshot.Settings.Modules["flash"] = enabled
		if _, fields, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) > 0 {
			t.Fatal(fields, err)
		}
		for _, path := range []string{"/flash/flash-before/", "/old-news-entry/"} {
			r := serveContentAPI(handler, nil, "GET", "https://example.test"+path, "", nil)
			if enabled {
				if r.Code != 301 || r.Header().Get("Location") != content.PublicPath(post) {
					t.Errorf("reenabling flash lost redirect: %s %d %v", path, r.Code, r.Header())
				}
			} else if r.Code != 404 || r.Header().Get("Location") != "" {
				t.Errorf("disabled flash exposed redirect: %s %d %v", path, r.Code, r.Header())
			}
		}
	}
	input.Slug, input.Status = "flash-before", content.StatusPrivate
	if _, err := repo.Create(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(handler, nil, "GET", "https://example.test/flash/flash-before/", "", nil)
	if r.Code != 404 || r.Header().Get("Location") != "" {
		t.Errorf("private canonical path exposed redirect: %d %v", r.Code, r.Header())
	}
	if _, err := repo.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusPrivate); err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(handler, nil, "GET", "https://example.test/old-news-entry/", "", nil)
	if r.Code != 404 || r.Header().Get("Location") != "" {
		t.Errorf("private target exposed redirect: %d %v", r.Code, r.Header())
	}
}
