package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"newpress.local/newpress/internal/media"
)

func mediaAPIUploadRequest(t *testing.T, token, csrf, filename string, payload []byte) *http.Request {
	t.Helper()
	request := mediaUploadRequest(t, token, csrf, filename, payload, "API image", "Alt & quoted text", "Image caption")
	request.URL.Path = "/api/v1/admin/media"
	request.RequestURI = request.URL.RequestURI()
	request.Header.Set("X-CSRF-Token", csrf)
	return request
}

func TestMediaAPIUploadDeduplicationProjectionAndPagination(t *testing.T) {
	server, handler, token, principal, _ := newMediaHTTPTestServer(t, 4096)
	serve := func(request *http.Request) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	get := func(path string) *httptest.ResponseRecorder {
		return serve(authenticatedMediaRequest(http.MethodGet, "https://example.test"+path, nil, token))
	}
	empty := get("/api/v1/admin/media?kind=image")
	var listing adminMediaListAPIResponse
	if err := json.Unmarshal(empty.Body.Bytes(), &listing); err != nil || empty.Code != http.StatusOK || listing.Items == nil || len(listing.Items) != 0 || listing.Pagination.Total != 0 || listing.Pagination.Pages != 1 || !listing.CanUpload || listing.MaxUploadBytes != 4096 {
		t.Fatalf("empty library = %d %#v, %v", empty.Code, listing, err)
	}
	payload := httpTestPNG(t, 4, 3)
	upload := serve(mediaAPIUploadRequest(t, token, principal.CSRFToken, "source.html", payload))
	var uploaded struct {
		Item      adminMediaAPIItem `json:"item"`
		Duplicate bool              `json:"duplicate"`
	}
	if err := json.Unmarshal(upload.Body.Bytes(), &uploaded); err != nil || upload.Code != http.StatusCreated || uploaded.Duplicate || uploaded.Item.ID != 1 || uploaded.Item.Filename != "source.png" || uploaded.Item.Width != 4 || uploaded.Item.Height != 3 {
		t.Fatalf("upload = %d %#v, %v", upload.Code, uploaded, err)
	}
	if upload.Header().Get("Location") != "/api/v1/admin/media/1" || !strings.Contains(upload.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("upload headers = %#v", upload.Header())
	}
	for _, forbidden := range []string{"object_key", "storage", "checksum_sha256", "uploaded_by"} {
		if strings.Contains(upload.Body.String(), `"`+forbidden+`":`) {
			t.Errorf("upload response exposed %s", forbidden)
		}
	}
	duplicate := serve(mediaAPIUploadRequest(t, token, principal.CSRFToken, "copy.png", payload))
	if err := json.Unmarshal(duplicate.Body.Bytes(), &uploaded); err != nil || duplicate.Code != http.StatusOK || !uploaded.Duplicate || uploaded.Item.ID != 1 || server.media.Snapshot().Count != 1 {
		t.Fatalf("duplicate upload = %d %#v, %v", duplicate.Code, uploaded, err)
	}
	item := get("/api/v1/admin/media/1")
	if item.Code != http.StatusOK || !strings.Contains(item.Body.String(), `"alt_text":"Alt \u0026 quoted text"`) {
		t.Fatalf("media lookup = %d %s", item.Code, item.Body)
	}
	asset := serve(httptest.NewRequest(http.MethodGet, "https://example.test"+uploaded.Item.PublicURL, nil))
	if asset.Code != http.StatusOK || !bytes.Equal(asset.Body.Bytes(), payload) {
		t.Fatalf("uploaded asset = %d, %d bytes", asset.Code, asset.Body.Len())
	}
	for index := 0; index < 25; index++ {
		_, err := server.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 20+index, 3)), media.UploadInput{
			Filename: fmt.Sprintf("library-%02d.png", index), Title: fmt.Sprintf("Library %02d", index),
		}, 4096)
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[int64]bool)
	for page, count := range []int{24, 2} {
		response := get(fmt.Sprintf("/api/v1/admin/media?kind=image&page=%d", page+1))
		if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || response.Code != http.StatusOK || len(listing.Items) != count || listing.Pagination.Total != 26 || listing.Pagination.Pages != 2 {
			t.Fatalf("page %d = %d %#v, %v", page+1, response.Code, listing, err)
		}
		for _, item := range listing.Items {
			if seen[item.ID] {
				t.Fatalf("duplicate item across pages: %d", item.ID)
			}
			seen[item.ID] = true
		}
	}
	filtered := get("/api/v1/admin/media?kind=image&q=library")
	if err := json.Unmarshal(filtered.Body.Bytes(), &listing); err != nil || listing.Pagination.Total != 25 || listing.Pagination.Next != "/api/v1/admin/media?kind=image&page=2&q=library" {
		t.Fatalf("filtered listing = %#v, %v", listing, err)
	}
	for _, path := range []string{"/api/v1/admin/media?q=missing", "/api/v1/admin/media?page=999"} {
		response := get(path)
		if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || response.Code != http.StatusOK || listing.Items == nil || len(listing.Items) != 0 {
			t.Fatalf("empty result at %q = %d %#v, %v", path, response.Code, listing, err)
		}
	}
	for _, query := range []string{"kind=svg", "page=0", "page=1&page=2", "q=a&q=b", "page=%ff", "q=%ff", "page=%QQ", "unknown=1", "q=" + strings.Repeat("x", 101)} {
		if response := get("/api/v1/admin/media?" + query); response.Code != http.StatusBadRequest {
			t.Errorf("invalid query %q = %d", query, response.Code)
		}
	}
	if response := get("/api/v1/admin/media/999"); response.Code != http.StatusNotFound {
		t.Fatalf("missing image status = %d", response.Code)
	}
	trashed, err := server.media.Trash(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.media.Delete(trashed.ID, trashed.Version); err != nil {
		t.Fatal(err)
	}
	if response := get("/api/v1/admin/media/1"); response.Code != http.StatusNotFound {
		t.Fatalf("deleted image remained available: %d", response.Code)
	}
}

func TestMediaAPIRejectsUnauthorizedAndInvalidUploads(t *testing.T) {
	server, handler, token, principal, _ := newMediaHTTPTestServer(t, 4096)
	for _, test := range []struct {
		name   string
		status int
		body   []byte
		change func(*http.Request)
	}{
		{"anonymous", http.StatusUnauthorized, nil, func(r *http.Request) { r.Header.Del("Cookie") }},
		{"cross origin", http.StatusForbidden, nil, func(r *http.Request) { r.Header.Set("Origin", "https://other.test") }},
		{"missing CSRF", http.StatusForbidden, nil, func(r *http.Request) { r.Header.Del("X-CSRF-Token") }},
		{"duplicate CSRF", http.StatusForbidden, nil, func(r *http.Request) { r.Header.Add("X-CSRF-Token", principal.CSRFToken) }},
		{"wrong content type", http.StatusUnsupportedMediaType, nil, func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }},
		{"unknown query", http.StatusBadRequest, nil, func(r *http.Request) { r.URL.RawQuery = "extra=1" }},
		{"oversized file", http.StatusRequestEntityTooLarge, bytes.Repeat([]byte("x"), 4097), nil},
		{"unsupported file", http.StatusUnprocessableEntity, []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := test.body
			if payload == nil {
				payload = httpTestPNG(t, 4, 3)
			}
			request := mediaAPIUploadRequest(t, token, principal.CSRFToken, "image.png", payload)
			if test.change != nil {
				test.change(request)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || server.media.Snapshot().Count != 0 {
				t.Fatalf("rejected upload = %d %s, count=%d", response.Code, response.Body, server.media.Snapshot().Count)
			}
			var failure apiErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || failure.Error.Code == "" {
				t.Fatalf("error envelope = %#v, %v", failure, err)
			}
		})
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/admin/media", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous listing = %d", response.Code)
	}
}

func TestMediaAPIAuthorReadAndUploadCapabilities(t *testing.T) {
	server, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
	_, authorCookie, author := createAndLoginTestUser(t, manager, owner, "image-author", "image-author@example.test", "Image author", "author")
	_, subscriberCookie, _ := createAndLoginTestUser(t, manager, owner, "image-reader", "image-reader@example.test", "Image reader", "subscriber")
	read := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/media", authorCookie, nil)
	var listing adminMediaListAPIResponse
	if err := json.Unmarshal(read.Body.Bytes(), &listing); err != nil || read.Code != http.StatusOK || listing.CanUpload {
		t.Fatalf("author media read = %d %#v, %v", read.Code, listing, err)
	}
	request := mediaAPIUploadRequest(t, authorCookie.Value, author.CSRFToken, "image.png", httpTestPNG(t, 4, 3))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || server.media.Snapshot().Count != 0 {
		t.Fatalf("author upload = %d %s", response.Code, response.Body)
	}
	if response := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/media", subscriberCookie, nil); response.Code != http.StatusForbidden {
		t.Fatalf("subscriber listing = %d", response.Code)
	}
	editor := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/new/", authorCookie, nil)
	if editor.Code != http.StatusOK || !strings.Contains(editor.Body.String(), `data-editor-media-open="body"`) || strings.Contains(editor.Body.String(), "data-editor-media-upload") {
		t.Fatalf("author editor controls = %d %s", editor.Code, editor.Body)
	}
}
