package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/mediausage"
)

func TestMediaReferencesHTTPBlocksHistoricalDeletionAndLinksToRevision(t *testing.T) {
	s, handler, token, principal, _ := newMediaHTTPTestServer(t, 8192)
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 3, 2)), media.UploadInput{Filename: "referenced.png"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	input := content.PostInput{Title: "Referenced page", Slug: "referenced-page", Type: content.TypePage, CoverURL: item.PublicURL}
	post, err := s.repository.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.CoverURL = ""
	post, err = s.repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authenticatedMediaRequest(http.MethodGet, "https://example.test"+path, nil, token))
		return response
	}
	response := get("/api/v1/admin/media/1/references")
	var result struct {
		Items     []adminMediaReference `json:"items"`
		Total     int                   `json:"total"`
		InUse     bool                  `json:"in_use"`
		CanDelete bool                  `json:"can_delete"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || result.Total != 1 || !result.InUse || result.CanDelete || len(result.Items) != 1 || result.Items[0].Source != content.MediaSourceRevision {
		t.Fatalf("references = %d %#v %v", response.Code, result, err)
	}
	if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("cache = %q", response.Header().Get("Cache-Control"))
	}
	if comparison := get(result.Items[0].URL); comparison.Code != http.StatusOK {
		t.Fatalf("comparison link = %d %s", comparison.Code, comparison.Body.String())
	}
	inspector := get("/admin/media/1/edit/")
	if inspector.Code != http.StatusOK || !strings.Contains(inspector.Body.String(), "已被引用，暂不可移除") || !strings.Contains(inspector.Body.String(), `class="admin-media-delete" type="submit" disabled`) || !strings.Contains(inspector.Body.String(), "Referenced page") {
		t.Fatalf("inspector = %d %s", inspector.Code, inspector.Body.String())
	}
	form := url.Values{"csrf_token": {principal.CSRFToken}, "version": {strconv.FormatInt(item.Version, 10)}, "action": {"trash"}, "title": {item.Title}, "alt_text": {""}, "caption": {""}}
	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, authenticatedMediaFormRequest("https://example.test/admin/media/1/edit/", form, token))
	if deleted.Code != http.StatusConflict || !strings.Contains(deleted.Body.String(), "媒体仍被内容或设置引用") {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
	if public := get(item.PublicURL); public.Code != http.StatusOK {
		t.Fatalf("media lost = %d", public.Code)
	}
	for _, query := range []string{"?page=0", "?page=-1", "?page=1&page=2", "?owner=user:1"} {
		if bad := get("/api/v1/admin/media/1/references" + query); bad.Code != http.StatusBadRequest {
			t.Fatalf("query %s = %d", query, bad.Code)
		}
	}
	if missing := get("/api/v1/admin/media/999/references"); missing.Code != http.StatusNotFound {
		t.Fatal(missing.Code)
	}
}

func TestMediaReferencesHTTPRedactsOtherAuthorsAndAutosaveOwners(t *testing.T) {
	s, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	one, oneCookie, _ := createAndLoginTestUser(t, manager, owner, "media-author", "media-author@example.test", "Media author", "author")
	two, _, _ := createAndLoginTestUser(t, manager, owner, "other-author", "other-author@example.test", "Other author", "author")
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 4, 2)), media.UploadInput{Filename: "private.png"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		id          int64
		title, slug string
	}{{one.ID, "Own title", "own-title"}, {two.ID, "Hidden canonical marker", "other-title"}} {
		post, err := s.repository.Create(t.Context(), content.PostInput{AuthorID: fixture.id, Title: fixture.title, Slug: fixture.slug, CoverURL: item.PublicURL})
		if err != nil {
			t.Fatal(err)
		}
		snapshot := post
		snapshot.Title = "Autosave private marker " + fixture.slug
		if _, err := s.repository.SaveAutosave(t.Context(), content.Autosave{PostID: post.ID, OwnerKey: "user:" + strconv.FormatInt(fixture.id, 10), SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/api/v1/admin/media/1/references", "/admin/media/1/edit/"} {
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, oneCookie, nil)
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, "Own title") || !strings.Contains(body, "Autosave private marker own-title") || strings.Contains(body, "Hidden canonical marker") || strings.Contains(body, "Autosave private marker other-title") || strings.Contains(body, "owner_key") {
			t.Fatalf("author references = %d %s", response.Code, body)
		}
		admin := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, ownerCookie, nil)
		if admin.Code != http.StatusOK || !strings.Contains(admin.Body.String(), "Hidden canonical marker") || strings.Contains(admin.Body.String(), "Autosave private marker") {
			t.Fatalf("administrator references = %d %s", admin.Code, admin.Body.String())
		}
	}
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/admin/media/1/references", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatal(anonymous.Code)
	}
}

type unavailableMediaSnapshots struct{ content.Repository }

func (r unavailableMediaSnapshots) WalkMediaSnapshots(context.Context, func(content.MediaSnapshot) error) error {
	return errors.New("offline fixture")
}

func TestMediaReferenceHTTPFailsClosed(t *testing.T) {
	s, _, token, principal, _ := newMediaHTTPTestServer(t, 8192)
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 1, 1)), media.UploadInput{Filename: "protected.png"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	s.mediaUsage = mediausage.New(s.media, unavailableMediaSnapshots{s.repository}, s.navigation, s.siteSettings, s.themes, s.config.PublicURL)
	handler := s.routes()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedMediaRequest(http.MethodGet, "https://example.test/api/v1/admin/media/1/references", nil, token))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "media.references_unavailable") {
		t.Fatalf("unavailable = %d %s", response.Code, response.Body.String())
	}
	form := url.Values{"csrf_token": {principal.CSRFToken}, "version": {"1"}, "action": {"trash"}, "title": {item.Title}, "alt_text": {""}, "caption": {""}}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedMediaFormRequest("https://example.test/admin/media/1/edit/", form, token))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `class="admin-media-delete" type="submit" disabled`) {
		t.Fatalf("unavailable delete = %d %s", response.Code, response.Body.String())
	}
	if _, err := s.media.Get(item.ID); err != nil {
		t.Fatal(err)
	}
}
