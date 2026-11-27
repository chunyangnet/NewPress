package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/mediausage"
)

func mediaLifecycleRequest(method, path, body, token, csrf, etag string) *http.Request {
	request := authenticatedMediaRequest(method, "https://example.test"+path, strings.NewReader(body), token)
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("If-Match", etag)
	if method == http.MethodPatch {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func TestMediaLifecycleAPIAndHTMLRecovery(t *testing.T) {
	s, handler, token, principal, _ := newMediaHTTPTestServer(t, 8192)
	payload := httpTestPNG(t, 5, 3)
	item, err := s.media.Upload(t.Context(), bytes.NewReader(payload), media.UploadInput{UploadSource: "media-api", Filename: "lifecycle.png", Title: "Original", AltText: "Preserved alt"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(request *http.Request) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	get := func(path string) *httptest.ResponseRecorder {
		return serve(authenticatedMediaRequest(http.MethodGet, "https://example.test"+path, nil, token))
	}
	write := func(method, path, body, etag string) *httptest.ResponseRecorder {
		return serve(mediaLifecycleRequest(method, path, body, token, principal.CSRFToken, etag))
	}
	readItem := func(response *httptest.ResponseRecorder, version int64, status string) adminMediaAPIItem {
		t.Helper()
		var result adminMediaAPIItem
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || result.Version != version || result.Status != status || result.ETag != response.Header().Get("ETag") || result.ETag == "" {
			t.Fatalf("media representation = %d %s, %v", response.Code, response.Body, err)
		}
		if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("management cache = %v", response.Header())
		}
		for _, key := range []string{"object_key", "checksum_sha256", "uploaded_by"} {
			if strings.Contains(response.Body.String(), `"`+key+`":`) {
				t.Errorf("management response exposed %s", key)
			}
		}
		return result
	}

	current := readItem(get("/api/v1/admin/media/1"), 1, media.StatusActive)
	patch := write(http.MethodPatch, "/api/v1/admin/media/1", `{"title":"Updated"}`, current.ETag)
	current = readItem(patch, 2, media.StatusActive)
	if current.Title != "Updated" || current.AltText != item.AltText || current.PublicURL != item.PublicURL {
		t.Fatalf("partial update lost metadata: %+v", current)
	}
	if rejected := write(http.MethodDelete, "/api/v1/admin/media/1", "", current.ETag); rejected.Code != http.StatusConflict || !strings.Contains(rejected.Body.String(), "media.not_trashed") {
		t.Fatalf("active permanent deletion = %d %s", rejected.Code, rejected.Body)
	}
	public := get(item.PublicURL)
	if public.Code != http.StatusOK || public.Header().Get("Cache-Control") != "public, no-cache" {
		t.Fatalf("public availability must be revalidated: %d %v", public.Code, public.Header())
	}
	conditional := httptest.NewRequest(http.MethodGet, "https://example.test"+item.PublicURL, nil)
	conditional.Header.Set("If-None-Match", public.Header().Get("ETag"))
	if cached := serve(conditional); cached.Code != http.StatusNotModified || cached.Header().Get("Cache-Control") != "public, no-cache" {
		t.Fatalf("conditional cache = %d %v", cached.Code, cached.Header())
	}

	previousETag := current.ETag
	current = readItem(write(http.MethodPost, "/api/v1/admin/media/1/trash", "", current.ETag), 3, media.StatusTrash)
	if current.TrashedAt.IsZero() {
		t.Fatal("trash timestamp missing")
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		request := httptest.NewRequest(method, "https://example.test"+item.PublicURL, nil)
		request.Header.Set("If-None-Match", public.Header().Get("ETag"))
		request.Header.Set("Range", "bytes=0-3")
		if hidden := serve(request); hidden.Code != http.StatusNotFound || hidden.Header().Get("Cache-Control") != "no-store" || hidden.Header().Get("ETag") != "" {
			t.Fatalf("trash public access = %d %v", hidden.Code, hidden.Header())
		}
	}
	preview := authenticatedMediaRequest(http.MethodGet, "https://example.test/admin/media/1/preview/", nil, token)
	preview.Header.Set("Range", "bytes=0-3")
	if response := serve(preview); response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), payload[:4]) || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("retained preview = %d %v", response.Code, response.Header())
	}
	if anonymous := serve(httptest.NewRequest(http.MethodGet, "https://example.test/admin/media/1/preview/", nil)); anonymous.Code != http.StatusSeeOther || !strings.Contains(anonymous.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("anonymous preview = %d %v", anonymous.Code, anonymous.Header())
	}
	for _, status := range []string{media.StatusActive, media.StatusTrash} {
		response := get("/api/v1/admin/media?status=" + status)
		var result adminMediaListAPIResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || result.StatusCounts[media.StatusActive] != 0 || result.StatusCounts[media.StatusTrash] != 1 || (status == media.StatusTrash && len(result.Items) != 1) || (status == media.StatusActive && len(result.Items) != 0) {
			t.Fatalf("%s list = %d %s, %v", status, response.Code, response.Body, err)
		}
	}
	duplicate := serve(mediaAPIUploadRequest(t, token, principal.CSRFToken, "duplicate.png", payload))
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "media.duplicate_trashed") || s.media.Snapshot().Count != 1 {
		t.Fatalf("trashed duplicate = %d %s", duplicate.Code, duplicate.Body)
	}
	inspector := get("/admin/media/1/edit/")
	if inspector.Code != http.StatusOK || !strings.Contains(inspector.Body.String(), `name="action" value="restore"`) || !strings.Contains(inspector.Body.String(), `name="action" value="delete"`) || !strings.Contains(inspector.Body.String(), `src="/admin/media/1/preview/"`) || strings.Contains(inspector.Body.String(), "复制公开地址") {
		t.Fatalf("trash inspector controls missing: %d", inspector.Code)
	}
	if stale := write(http.MethodPost, "/api/v1/admin/media/1/restore", "", previousETag); stale.Code != http.StatusPreconditionFailed || stale.Header().Get("ETag") != current.ETag {
		t.Fatalf("stale restore = %d %s", stale.Code, stale.Body)
	}
	form := url.Values{"csrf_token": {principal.CSRFToken}, "version": {strconv.FormatInt(current.Version, 10)}, "action": {"restore"}, "title": {current.Title}, "alt_text": {current.AltText}, "caption": {current.Caption}}
	restored := serve(authenticatedMediaFormRequest("https://example.test/admin/media/1/edit/", form, token))
	if restored.Code != http.StatusSeeOther || restored.Header().Get("Location") != "/admin/media/1/edit/?notice=restored" {
		t.Fatalf("HTML restoration = %d %s", restored.Code, restored.Body)
	}
	current = readItem(get("/api/v1/admin/media/1"), 4, media.StatusActive)
	if !current.TrashedAt.IsZero() || current.PublicURL != item.PublicURL {
		t.Fatalf("restored identity = %+v", current)
	}
	if response := get(item.PublicURL); response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), payload) || response.Header().Get("ETag") != public.Header().Get("ETag") {
		t.Fatalf("restored bytes = %d %v", response.Code, response.Header())
	}
	current = readItem(write(http.MethodPost, "/api/v1/admin/media/1/trash", "", current.ETag), 5, media.StatusTrash)
	current = readItem(write(http.MethodPost, "/api/v1/admin/media/1/restore", "", current.ETag), 6, media.StatusActive)
	current = readItem(write(http.MethodPost, "/api/v1/admin/media/1/trash", "", current.ETag), 7, media.StatusTrash)
	deleted := write(http.MethodDelete, "/api/v1/admin/media/1", "", current.ETag)
	if deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 || !strings.Contains(deleted.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("permanent deletion = %d %s", deleted.Code, deleted.Body)
	}
	for _, path := range []string{item.PublicURL, "/api/v1/admin/media/1", "/admin/media/1/preview/"} {
		if missing := get(path); missing.Code != http.StatusNotFound {
			t.Errorf("deleted resource %s = %d", path, missing.Code)
		}
	}
}

func TestMediaLifecycleAPIStrictWritesLeaveStateUntouched(t *testing.T) {
	s, handler, token, principal, _ := newMediaHTTPTestServer(t, 8192)
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 2, 3)), media.UploadInput{Filename: "strict.png"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/v1/admin/media/1", `{"title":"Changed"}`},
		{http.MethodPost, "/api/v1/admin/media/1/trash", ""},
		{http.MethodPost, "/api/v1/admin/media/1/restore", ""},
		{http.MethodDelete, "/api/v1/admin/media/1", ""},
	} {
		for _, test := range []struct {
			name   string
			status int
			change func(*http.Request)
		}{
			{"anonymous", 401, func(r *http.Request) { r.Header.Del("Cookie") }},
			{"cross origin", 403, func(r *http.Request) { r.Header.Set("Origin", "https://other.test") }},
			{"missing CSRF", 403, func(r *http.Request) { r.Header.Del("X-CSRF-Token") }},
			{"duplicate CSRF", 403, func(r *http.Request) { r.Header.Add("X-CSRF-Token", principal.CSRFToken) }},
			{"unknown query", 400, func(r *http.Request) { r.URL.RawQuery = "force=true" }},
			{"missing precondition", 428, func(r *http.Request) { r.Header.Del("If-Match") }},
			{"repeated precondition", 428, func(r *http.Request) { r.Header.Add("If-Match", mediaAPIETag(item)) }},
			{"weak precondition", 412, func(r *http.Request) { r.Header.Set("If-Match", "W/"+mediaAPIETag(item)) }},
			{"wildcard precondition", 412, func(r *http.Request) { r.Header.Set("If-Match", "*") }},
			{"stale precondition", 412, func(r *http.Request) { r.Header.Set("If-Match", `"media-1-v99"`) }},
		} {
			t.Run(endpoint.method+endpoint.path+"/"+test.name, func(t *testing.T) {
				request := mediaLifecycleRequest(endpoint.method, endpoint.path, endpoint.body, token, principal.CSRFToken, mediaAPIETag(item))
				test.change(request)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				assertMediaWriteRejected(t, s, response, item, test.status)
			})
		}
		if endpoint.method != http.MethodPatch {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, mediaLifecycleRequest(endpoint.method, endpoint.path, "{}", token, principal.CSRFToken, mediaAPIETag(item)))
			assertMediaWriteRejected(t, s, response, item, http.StatusBadRequest)
		}
	}
	for _, test := range []struct {
		body, contentType string
		status            int
	}{
		{`{}`, "application/json", 400},
		{`{"title":null}`, "application/json", 400},
		{`{"title":"One","title":"Two"}`, "application/json", 400},
		{`{"status":"trash"}`, "application/json", 400},
		{`{"title":"One"} {}`, "application/json", 400},
		{`{"title":"One"}`, "text/plain", 415},
		{`{"title":""}`, "application/json", 422},
		{`{"caption":"` + strings.Repeat("x", 2001) + `"}`, "application/json", 422},
		{`{"caption":"` + strings.Repeat("x", 33<<10) + `"}`, "application/json", 413},
	} {
		request := mediaLifecycleRequest(http.MethodPatch, "/api/v1/admin/media/1", test.body, token, principal.CSRFToken, mediaAPIETag(item))
		request.Header.Set("Content-Type", test.contentType)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertMediaWriteRejected(t, s, response, item, test.status)
	}
}

func assertMediaWriteRejected(t *testing.T, s *Server, response *httptest.ResponseRecorder, original media.Item, status int) {
	t.Helper()
	var failure apiErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || response.Code != status || failure.Error.Code == "" || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("rejected mutation = %d %s, %v", response.Code, response.Body, err)
	}
	if current, err := s.media.Get(original.ID); err != nil || current != original {
		t.Fatalf("rejected mutation changed media: %+v %v", current, err)
	}
}

func TestMediaLifecycleAPIRechecksReferencesAndScanFailures(t *testing.T) {
	s, _, token, principal, _ := newMediaHTTPTestServer(t, 8192)
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 3, 3)), media.UploadInput{Filename: "referenced.png"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.repository.Create(t.Context(), content.PostInput{Title: "Referenced", Slug: "referenced-media", CoverURL: item.PublicURL}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"trash", "delete"} {
		if action == "delete" {
			// Simulate a legacy writer bypassing the coordinated application guard.
			item, err = s.media.Trash(item.ID, item.Version)
			if err != nil {
				t.Fatal(err)
			}
		}
		method, path := http.MethodPost, "/api/v1/admin/media/1/trash"
		if action == "delete" {
			method, path = http.MethodDelete, "/api/v1/admin/media/1"
		}
		response := httptest.NewRecorder()
		s.routes().ServeHTTP(response, mediaLifecycleRequest(method, path, "", token, principal.CSRFToken, mediaAPIETag(item)))
		assertMediaWriteRejected(t, s, response, item, http.StatusConflict)
		if !strings.Contains(response.Body.String(), "media.in_use") {
			t.Fatal(response.Body.String())
		}
	}
	s.mediaUsage = mediausage.New(s.media, unavailableMediaSnapshots{s.repository}, s.navigation, s.siteSettings, s.themes, s.config.PublicURL)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, mediaLifecycleRequest(http.MethodDelete, "/api/v1/admin/media/1", "", token, principal.CSRFToken, mediaAPIETag(item)))
	assertMediaWriteRejected(t, s, response, item, http.StatusServiceUnavailable)
	if _, _, err := s.media.Open(item.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatal("failed deletion made trash public", err)
	}
	_, reader, err := s.media.OpenForManagement(item.ID)
	if err != nil {
		t.Fatal("failed deletion lost retained object", err)
	}
	_ = reader.Close()
}

func TestMediaLifecycleRequiresWriteCapabilityAndProtectsPreview(t *testing.T) {
	s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
	_, authorCookie, author := createAndLoginTestUser(t, manager, owner, "trash-author", "trash-author@example.test", "Media author", "author")
	_, readerCookie, _ := createAndLoginTestUser(t, manager, owner, "trash-reader", "trash-reader@example.test", "Reader", "subscriber")
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 2, 2)), media.UploadInput{Filename: "retained.png"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "/trash", "/restore"} {
		method := http.MethodPost
		if path == "" {
			method = http.MethodDelete
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, mediaLifecycleRequest(method, "/api/v1/admin/media/1"+path, "", authorCookie.Value, author.CSRFToken, mediaAPIETag(item)))
		assertMediaWriteRejected(t, s, response, item, http.StatusForbidden)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mediaLifecycleRequest(http.MethodPatch, "/api/v1/admin/media/1", `{"title":"Changed"}`, authorCookie.Value, author.CSRFToken, mediaAPIETag(item)))
	assertMediaWriteRejected(t, s, response, item, http.StatusForbidden)
	if _, err := s.media.Trash(item.ID, item.Version); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		cookie *http.Cookie
		status int
	}{{authorCookie, http.StatusOK}, {readerCookie, http.StatusForbidden}} {
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/media/1/preview/", test.cookie, nil)
		if response.Code != test.status || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("preview capability = %d %v", response.Code, response.Header())
		}
	}
}
