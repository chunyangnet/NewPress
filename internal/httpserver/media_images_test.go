package httpserver

import (
	"bytes"
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
)

func TestMediaImageHTTPProcessingCacheAndReferences(t *testing.T) {
	s, handler, token, principal, _ := newMediaHTTPTestServer(t, 1<<20)
	original := httpTestPNG(t, 80, 40)
	item, err := s.media.Upload(t.Context(), bytes.NewReader(original), media.UploadInput{Filename: "封面.png"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	process := func(body, etag, csrf string) *httptest.ResponseRecorder {
		r := mediaLifecycleRequest("POST", "/api/v1/admin/media/1/process", body, token, csrf, etag)
		r.Header.Set("Content-Type", "application/json")
		return serve(r)
	}
	for _, tc := range []struct {
		body, etag, csrf string
		status           int
	}{
		{`{"focal_x":75,"focal_y":25}`, "", principal.CSRFToken, 428},
		{`{"focal_x":75,"focal_y":25}`, mediaAPIETag(item), "invalid", 403},
		{`{"focal_x":null,"focal_y":25}`, mediaAPIETag(item), principal.CSRFToken, 400},
		{`{"focal_x":50,"focal_y":25,"extra":true}`, mediaAPIETag(item), principal.CSRFToken, 400},
		{`{"focal_x":50,"focal_x":70,"focal_y":25}`, mediaAPIETag(item), principal.CSRFToken, 400},
		{`{"focal_x":101,"focal_y":25}`, mediaAPIETag(item), principal.CSRFToken, 422},
	} {
		if response := process(tc.body, tc.etag, tc.csrf); response.Code != tc.status {
			t.Fatalf("%s = %d %s", tc.body, response.Code, response.Body)
		}
	}
	response := process(`{"focal_x":75,"focal_y":25}`, mediaAPIETag(item), principal.CSRFToken)
	if response.Code != 200 {
		t.Fatalf("process = %d %s", response.Code, response.Body)
	}
	var result adminMediaAPIItem
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Version != 2 || len(result.Image.Variants) != 6 || !result.Image.Processed || result.Image.FocalX != 75 {
		t.Fatalf("result = %+v", result)
	}
	view := pageView{Post: content.Post{CoverURL: "media/1/封面.png", ContentHTML: `<img src="media/1/封面.png">`}}
	s.prepareResponsiveImages(&view)
	if len(view.Images) != 0 || len(view.BodyImages) != 0 {
		t.Fatal("relative media meaning changed")
	}
	for _, private := range []string{"object_key", "checksum_sha256", "objects/derived"} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatal("private path leaked", private)
		}
	}
	if stale := process(`{"focal_x":50,"focal_y":50}`, mediaAPIETag(item), principal.CSRFToken); stale.Code != 412 {
		t.Fatal("stale process accepted")
	}
	asset := func(path string) *httptest.ResponseRecorder {
		return serve(httptest.NewRequest("GET", "https://example.test"+path, nil))
	}
	variantURL := result.Image.Variants[0].WebPURL
	variant := asset(variantURL)
	if variant.Code != 200 || variant.Header().Get("Content-Type") != "image/webp" || variant.Header().Get("Cache-Control") != "public, no-cache" || variant.Header().Get("Last-Modified") != "" {
		t.Fatalf("variant = %d %v", variant.Code, variant.Header())
	}
	decoded, _, err := image.Decode(bytes.NewReader(variant.Body.Bytes()))
	if err != nil || decoded.Bounds().Dx() != 40 || decoded.Bounds().Dy() != 40 {
		t.Fatal("square response", err)
	}
	if raw := asset(item.PublicURL); !bytes.Equal(raw.Body.Bytes(), original) {
		t.Fatal("original changed")
	}
	r := httptest.NewRequest("GET", "https://example.test"+variantURL, nil)
	r.Header.Set("If-None-Match", variant.Header().Get("ETag"))
	if serve(r).Code != 304 {
		t.Fatal("missing derivative ETag validation")
	}
	r = httptest.NewRequest("GET", "https://example.test"+variantURL, nil)
	r.Header.Set("Range", "bytes=0-11")
	if got := serve(r); got.Code != 206 || !bytes.Equal(got.Body.Bytes(), variant.Body.Bytes()[:12]) {
		t.Fatal("variant range")
	}
	for _, q := range []string{"?variant=square&variant=large", "?format=webp", "?variant=square&width=900", "?variant=square&format=svg"} {
		if got := asset(item.PublicURL + q); got.Code != 400 {
			t.Fatalf("invalid %s = %d", q, got.Code)
		}
	}
	if asset(item.PublicURL+"?variant=arbitrary").Code != 404 {
		t.Fatal("arbitrary variant")
	}
	if asset("/media/1/wrong.png?variant=square").Code != 404 {
		t.Fatal("wrong public path accepted")
	}
	post, err := s.repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Slug: "image-display", Title: "图片展示", Excerpt: "响应式图片", CoverURL: item.PublicURL, ContentSource: `<p>图片</p><img src="` + item.PublicURL + `" alt="保留替代文字">`})
	if err != nil {
		t.Fatal(err)
	}
	public := asset("/posts/" + post.Slug)
	if public.Code != 200 || !strings.Contains(public.Body.String(), "srcset=") || !strings.Contains(public.Body.String(), "variant=large") || !strings.Contains(public.Body.String(), "保留替代文字") {
		t.Fatalf("responsive page %d %s", public.Code, public.Body)
	}
	stored, err := s.repository.GetByID(t.Context(), post.ID)
	if err != nil || strings.Contains(stored.ContentHTML, "srcset") {
		t.Fatal("render rewrote article", err)
	}
	// The media identity guard also recognizes explicit derivative URLs.
	if _, err := s.repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "派生引用", ContentSource: `<img src="` + variantURL + `">`}); err != nil {
		t.Fatal(err)
	}
	trash := serve(mediaLifecycleRequest("POST", "/api/v1/admin/media/1/trash", "", token, principal.CSRFToken, result.ETag))
	if trash.Code != 409 || !strings.Contains(trash.Body.String(), "media.in_use") {
		t.Fatal("derivative reference did not protect media", trash.Body)
	}
	// Direct domain state change here isolates public/management visibility.
	item, _ = s.media.Get(item.ID)
	item, err = s.media.Trash(item.ID, item.Version)
	if err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "https://example.test"+variantURL, nil)
	r.Header.Set("If-None-Match", variant.Header().Get("ETag"))
	if serve(r).Code != 404 {
		t.Fatal("cached trash variant remains public")
	}
	preview := serve(authenticatedMediaRequest("GET", "https://example.test/admin/media/1/preview/?variant=square&format=webp", nil, token))
	if preview.Code != 200 || preview.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("management variant preview", preview.Code, preview.Header())
	}
}

func TestMediaImageFormPreservesInputOnConflict(t *testing.T) {
	s, handler, token, principal, _ := newMediaHTTPTestServer(t, 1<<20)
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 40, 30)), media.UploadInput{Filename: "test.png"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.media.Update(item.ID, item.Version, media.UpdateInput{Title: "Changed elsewhere"}); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {principal.CSRFToken}, "version": {"1"}, "focal_x": {"81"}, "focal_y": {"19"}}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedMediaFormRequest("https://example.test/admin/media/1/process/", form, token))
	if response.Code != 409 || !strings.Contains(response.Body.String(), `value="81"`) || !strings.Contains(response.Body.String(), `value="19"`) || !strings.Contains(response.Body.String(), `name="version" value="2"`) {
		t.Fatalf("failed form = %d %s", response.Code, response.Body)
	}
	form.Set("version", "2")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedMediaFormRequest("https://example.test/admin/media/1/process/", form, token))
	if response.Code != 303 {
		t.Fatalf("form = %d %s", response.Code, response.Body)
	}
	item, _ = s.media.Get(item.ID)
	if item.Image.FocalX != 81 || item.Image.FocalY != 19 {
		t.Fatal("focus not saved")
	}
}

func TestMediaImageProcessingRequiresCapability(t *testing.T) {
	s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
	_, authorCookie, author := createAndLoginTestUser(t, manager, owner, "image-author", "image-author@example.test", "Image author", "author")
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 8, 4)), media.UploadInput{Filename: "image.png"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	r := mediaLifecycleRequest("POST", "/api/v1/admin/media/1/process", `{"focal_x":50,"focal_y":50}`, authorCookie.Value, author.CSRFToken, mediaAPIETag(item))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assertMediaWriteRejected(t, s, w, item, http.StatusForbidden)
	form := url.Values{"csrf_token": {author.CSRFToken}, "version": {"1"}, "focal_x": {"50"}, "focal_y": {"50"}}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, authenticatedMediaFormRequest("https://example.test/admin/media/1/process/", form, authorCookie.Value))
	if w.Code != http.StatusForbidden {
		t.Fatalf("form capability = %d", w.Code)
	}
}
