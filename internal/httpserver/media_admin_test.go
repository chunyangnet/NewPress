package httpserver

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/theme"
)

func TestAdminMediaUploadEditDuplicateServeAndDelete(t *testing.T) {
	server, handler, token, principal, settings := newMediaHTTPTestServer(t, 4096)

	listRequest := authenticatedMediaRequest(http.MethodGet, "https://example.test/admin/media/", nil, token)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), "安全上传入口") || !strings.Contains(listResponse.Body.String(), "SHA-256") {
		t.Fatalf("empty media page = %d %s", listResponse.Code, listResponse.Body.String())
	}

	payload := httpTestPNG(t, 4, 3)
	uploadRequest := mediaUploadRequest(t, token, principal.CSRFToken, "Hero.HTML", payload, "首页封面", "蓝绿色后台封面", "测试素材")
	uploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(uploadResponse, uploadRequest)
	if uploadResponse.Code != http.StatusSeeOther || uploadResponse.Header().Get("Location") != "/admin/media/1/edit/?notice=uploaded" {
		t.Fatalf("upload response = %d %q %s", uploadResponse.Code, uploadResponse.Header().Get("Location"), uploadResponse.Body.String())
	}
	item, err := server.media.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if item.Filename != "Hero.png" || item.Title != "首页封面" || item.Width != 4 || item.Height != 3 || item.AltText != "蓝绿色后台封面" {
		t.Fatalf("uploaded item = %#v", item)
	}

	duplicateRequest := mediaUploadRequest(t, token, principal.CSRFToken, "copy.png", payload, "重复文件", "", "")
	duplicateResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateResponse, duplicateRequest)
	if duplicateResponse.Code != http.StatusSeeOther || duplicateResponse.Header().Get("Location") != "/admin/media/1/edit/?notice=duplicate" || server.media.Snapshot().Count != 1 {
		t.Fatalf("duplicate response = %d %q count=%d", duplicateResponse.Code, duplicateResponse.Header().Get("Location"), server.media.Snapshot().Count)
	}

	editRequest := authenticatedMediaRequest(http.MethodGet, "https://example.test/admin/media/1/edit/?notice=uploaded", nil, token)
	editResponse := httptest.NewRecorder()
	handler.ServeHTTP(editResponse, editRequest)
	if editResponse.Code != http.StatusOK || !strings.Contains(editResponse.Body.String(), "资源详情") || !strings.Contains(editResponse.Body.String(), item.PublicURL) {
		t.Fatalf("media edit page = %d %s", editResponse.Code, editResponse.Body.String())
	}

	assetRequest := httptest.NewRequest(http.MethodGet, "https://example.test"+item.PublicURL, nil)
	assetResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetResponse, assetRequest)
	if assetResponse.Code != http.StatusOK || assetResponse.Header().Get("Content-Type") != "image/png" || !bytes.Equal(assetResponse.Body.Bytes(), payload) {
		t.Fatalf("public media = %d type=%q bytes=%d", assetResponse.Code, assetResponse.Header().Get("Content-Type"), assetResponse.Body.Len())
	}
	if assetResponse.Header().Get("ETag") == "" || assetResponse.Header().Get("Cache-Control") != "public, no-cache" || assetResponse.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatalf("public media headers = %#v", assetResponse.Header())
	}
	conditionalRequest := httptest.NewRequest(http.MethodGet, "https://example.test"+item.PublicURL, nil)
	conditionalRequest.Header.Set("If-None-Match", assetResponse.Header().Get("ETag"))
	conditionalResponse := httptest.NewRecorder()
	handler.ServeHTTP(conditionalResponse, conditionalRequest)
	if conditionalResponse.Code != http.StatusNotModified || conditionalResponse.Body.Len() != 0 {
		t.Fatalf("conditional media = %d bytes=%d", conditionalResponse.Code, conditionalResponse.Body.Len())
	}
	wrongNameRequest := httptest.NewRequest(http.MethodGet, "https://example.test/media/1/wrong.png", nil)
	wrongNameResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongNameResponse, wrongNameRequest)
	if wrongNameResponse.Code != http.StatusNotFound {
		t.Fatalf("wrong media name status = %d", wrongNameResponse.Code)
	}

	updateForm := url.Values{
		"csrf_token": {principal.CSRFToken}, "version": {"1"}, "action": {"save"},
		"title": {"发布封面"}, "alt_text": {"更新后的替代文本"}, "caption": {"更新后的版权说明"},
	}
	updateRequest := authenticatedMediaFormRequest("https://example.test/admin/media/1/edit/", updateForm, token)
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, updateRequest)
	if updateResponse.Code != http.StatusSeeOther || updateResponse.Header().Get("Location") != "/admin/media/1/edit/?notice=saved" {
		t.Fatalf("media update = %d %q %s", updateResponse.Code, updateResponse.Header().Get("Location"), updateResponse.Body.String())
	}
	updated, err := server.media.Get(1)
	if err != nil || updated.Version != 2 || updated.Title != "发布封面" || updated.PublicURL != item.PublicURL {
		t.Fatalf("updated media = %#v, %v", updated, err)
	}

	staleForm := url.Values{
		"csrf_token": {principal.CSRFToken}, "version": {"1"}, "action": {"save"},
		"title": {"保留这段过期输入"}, "alt_text": {"stale alt"}, "caption": {"stale caption"},
	}
	staleRequest := authenticatedMediaFormRequest("https://example.test/admin/media/1/edit/", staleForm, token)
	staleResponse := httptest.NewRecorder()
	handler.ServeHTTP(staleResponse, staleRequest)
	if staleResponse.Code != http.StatusConflict || !strings.Contains(staleResponse.Body.String(), "保留这段过期输入") || !strings.Contains(staleResponse.Body.String(), `name="version" value="2"`) {
		t.Fatalf("stale update = %d %s", staleResponse.Code, staleResponse.Body.String())
	}

	snapshot := settings.Snapshot()
	maintenance := snapshot.Settings
	maintenance.Operations.MaintenanceEnabled = true
	if _, fields, err := settings.Save(snapshot.Revision, maintenance); err != nil || len(fields) != 0 {
		t.Fatalf("enable maintenance = %v, %#v", err, fields)
	}
	maintenanceAssetRequest := httptest.NewRequest(http.MethodGet, "https://example.test"+item.PublicURL, nil)
	maintenanceAssetResponse := httptest.NewRecorder()
	handler.ServeHTTP(maintenanceAssetResponse, maintenanceAssetRequest)
	if maintenanceAssetResponse.Code != http.StatusOK {
		t.Fatalf("media during maintenance = %d", maintenanceAssetResponse.Code)
	}
	rootRequest := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
	rootResponse := httptest.NewRecorder()
	handler.ServeHTTP(rootResponse, rootRequest)
	if rootResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("root during maintenance = %d", rootResponse.Code)
	}

	deleteForm := url.Values{
		"csrf_token": {principal.CSRFToken}, "version": {"2"}, "action": {"trash"},
		"title": {updated.Title}, "alt_text": {updated.AltText}, "caption": {updated.Caption},
	}
	deleteRequest := authenticatedMediaFormRequest("https://example.test/admin/media/1/edit/", deleteForm, token)
	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusSeeOther || deleteResponse.Header().Get("Location") != "/admin/media/1/edit/?notice=trashed" {
		t.Fatalf("media delete = %d %q %s", deleteResponse.Code, deleteResponse.Header().Get("Location"), deleteResponse.Body.String())
	}
	deletedAssetRequest := httptest.NewRequest(http.MethodGet, "https://example.test"+item.PublicURL, nil)
	deletedAssetResponse := httptest.NewRecorder()
	handler.ServeHTTP(deletedAssetResponse, deletedAssetRequest)
	if deletedAssetResponse.Code != http.StatusNotFound {
		t.Fatalf("deleted media status = %d", deletedAssetResponse.Code)
	}
}

func TestAdminMediaUploadSecurityBoundaries(t *testing.T) {
	_, handler, token, principal, _ := newMediaHTTPTestServer(t, 128)

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	unsupportedRequest := mediaUploadRequest(t, token, principal.CSRFToken, "active.svg", svg, "SVG", "", "")
	unsupportedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unsupportedResponse, unsupportedRequest)
	if unsupportedResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(unsupportedResponse.Body.String(), "拒绝 SVG") {
		t.Fatalf("unsupported upload = %d %s", unsupportedResponse.Code, unsupportedResponse.Body.String())
	}

	largePDF := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 512)...)
	largeRequest := mediaUploadRequest(t, token, principal.CSRFToken, "large.pdf", largePDF, "Large", "", "")
	largeResponse := httptest.NewRecorder()
	handler.ServeHTTP(largeResponse, largeRequest)
	if largeResponse.Code != http.StatusRequestEntityTooLarge || !strings.Contains(largeResponse.Body.String(), "超过部署上限") {
		t.Fatalf("large upload = %d %s", largeResponse.Code, largeResponse.Body.String())
	}

	crossSiteRequest := mediaUploadRequest(t, token, principal.CSRFToken, "cover.png", httpTestPNG(t, 1, 1), "Cover", "", "")
	crossSiteRequest.Header.Set("Origin", "https://attacker.test")
	crossSiteRequest.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSiteResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossSiteResponse, crossSiteRequest)
	if crossSiteResponse.Code != http.StatusForbidden || !strings.Contains(crossSiteResponse.Body.String(), "cross_site_request_rejected") {
		t.Fatalf("cross-site upload = %d %s", crossSiteResponse.Code, crossSiteResponse.Body.String())
	}

	badShapeBody := &bytes.Buffer{}
	writer := multipart.NewWriter(badShapeBody)
	_ = writer.WriteField("csrf_token", principal.CSRFToken)
	_ = writer.WriteField("title", "Bad")
	_ = writer.WriteField("alt_text", "")
	_ = writer.WriteField("caption", "")
	_ = writer.WriteField("unexpected", "value")
	part, _ := writer.CreateFormFile("file", "cover.png")
	_, _ = part.Write(httpTestPNG(t, 1, 1))
	_ = writer.Close()
	badShapeRequest := authenticatedMediaRequest(http.MethodPost, "https://example.test/admin/media/upload/", badShapeBody, token)
	badShapeRequest.Header.Set("Content-Type", writer.FormDataContentType())
	badShapeResponse := httptest.NewRecorder()
	handler.ServeHTTP(badShapeResponse, badShapeRequest)
	if badShapeResponse.Code != http.StatusBadRequest || !strings.Contains(badShapeResponse.Body.String(), "invalid_form_shape") {
		t.Fatalf("bad shape upload = %d %s", badShapeResponse.Code, badShapeResponse.Body.String())
	}

	repeatedBody := &bytes.Buffer{}
	repeatedWriter := multipart.NewWriter(repeatedBody)
	_ = repeatedWriter.WriteField("csrf_token", principal.CSRFToken)
	_ = repeatedWriter.WriteField("title", "First")
	_ = repeatedWriter.WriteField("title", "Second")
	_ = repeatedWriter.WriteField("alt_text", "")
	_ = repeatedWriter.WriteField("caption", "")
	repeatedPart, _ := repeatedWriter.CreateFormFile("file", "cover.png")
	_, _ = repeatedPart.Write(httpTestPNG(t, 1, 1))
	_ = repeatedWriter.Close()
	repeatedRequest := authenticatedMediaRequest(http.MethodPost, "https://example.test/admin/media/upload/", repeatedBody, token)
	repeatedRequest.Header.Set("Content-Type", repeatedWriter.FormDataContentType())
	repeatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(repeatedResponse, repeatedRequest)
	if repeatedResponse.Code != http.StatusBadRequest || !strings.Contains(repeatedResponse.Body.String(), "invalid_form_shape") {
		t.Fatalf("repeated upload field = %d %s", repeatedResponse.Code, repeatedResponse.Body.String())
	}
}

func TestPublicMediaUnicodeRangeAndDocumentHardening(t *testing.T) {
	server, handler, token, principal, _ := newMediaHTTPTestServer(t, 8192)

	imagePayload := httpTestPNG(t, 5, 4)
	imageUpload := mediaUploadRequest(t, token, principal.CSRFToken, "首页 海报.HTML", imagePayload, "首页海报", "蓝绿色首页海报", "内部设计")
	imageUploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(imageUploadResponse, imageUpload)
	if imageUploadResponse.Code != http.StatusSeeOther {
		t.Fatalf("unicode image upload = %d %s", imageUploadResponse.Code, imageUploadResponse.Body.String())
	}
	imageItem, err := server.media.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	expectedURL := "/media/" + strconv.FormatInt(imageItem.ID, 10) + "/" + url.PathEscape(imageItem.Filename)
	if imageItem.PublicURL != expectedURL || !strings.Contains(imageItem.PublicURL, "%E9") {
		t.Fatalf("unicode public URL = %q, want %q", imageItem.PublicURL, expectedURL)
	}

	unicodeRequest := httptest.NewRequest(http.MethodGet, "https://example.test"+imageItem.PublicURL, nil)
	unicodeResponse := httptest.NewRecorder()
	handler.ServeHTTP(unicodeResponse, unicodeRequest)
	if unicodeResponse.Code != http.StatusOK || !bytes.Equal(unicodeResponse.Body.Bytes(), imagePayload) {
		t.Fatalf("unicode media = %d bytes=%d", unicodeResponse.Code, unicodeResponse.Body.Len())
	}

	rangeRequest := httptest.NewRequest(http.MethodGet, "https://example.test"+imageItem.PublicURL, nil)
	rangeRequest.Header.Set("Range", "bytes=0-7")
	rangeResponse := httptest.NewRecorder()
	handler.ServeHTTP(rangeResponse, rangeRequest)
	if rangeResponse.Code != http.StatusPartialContent || !bytes.Equal(rangeResponse.Body.Bytes(), imagePayload[:8]) {
		t.Fatalf("media range = %d %q", rangeResponse.Code, rangeResponse.Body.Bytes())
	}
	wantRange := "bytes 0-7/" + strconv.Itoa(len(imagePayload))
	if rangeResponse.Header().Get("Content-Range") != wantRange || rangeResponse.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("media range headers = %#v", rangeResponse.Header())
	}

	pdfPayload := []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF\n")
	pdfUpload := mediaUploadRequest(t, token, principal.CSRFToken, "发布 规范.exe", pdfPayload, "发布规范", "", "公开下载")
	pdfUploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(pdfUploadResponse, pdfUpload)
	if pdfUploadResponse.Code != http.StatusSeeOther {
		t.Fatalf("PDF upload = %d %s", pdfUploadResponse.Code, pdfUploadResponse.Body.String())
	}
	document, err := server.media.Get(2)
	if err != nil {
		t.Fatal(err)
	}
	pdfRequest := httptest.NewRequest(http.MethodGet, "https://example.test"+document.PublicURL, nil)
	pdfResponse := httptest.NewRecorder()
	handler.ServeHTTP(pdfResponse, pdfRequest)
	disposition, parameters, parseErr := mime.ParseMediaType(pdfResponse.Header().Get("Content-Disposition"))
	if pdfResponse.Code != http.StatusOK || pdfResponse.Header().Get("Content-Type") != "application/pdf" ||
		disposition != "attachment" || parseErr != nil || parameters["filename"] != document.Filename || !bytes.Equal(pdfResponse.Body.Bytes(), pdfPayload) {
		t.Fatalf("public PDF = %d disposition=%q params=%#v parse=%v headers=%#v", pdfResponse.Code, disposition, parameters, parseErr, pdfResponse.Header())
	}
	if pdfResponse.Header().Get("Content-Security-Policy") != "sandbox; default-src 'none'" || pdfResponse.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("public PDF security headers = %#v", pdfResponse.Header())
	}
}

func TestAdminMediaAuthenticationMetadataAndStrictEditForms(t *testing.T) {
	server, handler, token, principal, _ := newMediaHTTPTestServer(t, 8192)
	payload := httpTestPNG(t, 2, 2)

	unauthenticated := mediaUploadRequest(t, token, principal.CSRFToken, "cover.png", payload, "Cover", "", "")
	unauthenticated.Header.Del("Cookie")
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusSeeOther || !strings.HasPrefix(unauthenticatedResponse.Header().Get("Location"), "/admin/login/") {
		t.Fatalf("unauthenticated upload = %d %q", unauthenticatedResponse.Code, unauthenticatedResponse.Header().Get("Location"))
	}

	badCSRFUpload := mediaUploadRequest(t, token, "invalid", "cover.png", payload, "Cover", "", "")
	badCSRFUploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(badCSRFUploadResponse, badCSRFUpload)
	if badCSRFUploadResponse.Code != http.StatusForbidden || !strings.Contains(badCSRFUploadResponse.Body.String(), "csrf_validation_failed") {
		t.Fatalf("bad CSRF upload = %d %s", badCSRFUploadResponse.Code, badCSRFUploadResponse.Body.String())
	}

	invalidMetadata := mediaUploadRequest(t, token, principal.CSRFToken, "cover.png", payload, strings.Repeat("题", 161), "", "")
	invalidMetadataResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidMetadataResponse, invalidMetadata)
	if invalidMetadataResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(invalidMetadataResponse.Body.String(), "标题最多 160 个字符") || server.media.Snapshot().Count != 0 {
		t.Fatalf("invalid upload metadata = %d count=%d %s", invalidMetadataResponse.Code, server.media.Snapshot().Count, invalidMetadataResponse.Body.String())
	}

	validUpload := mediaUploadRequest(t, token, principal.CSRFToken, "cover.png", payload, "封面", "初始替代文本", "")
	validUploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(validUploadResponse, validUpload)
	if validUploadResponse.Code != http.StatusSeeOther {
		t.Fatalf("valid upload = %d %s", validUploadResponse.Code, validUploadResponse.Body.String())
	}

	badEdit := url.Values{
		"csrf_token": {principal.CSRFToken}, "version": {"1"}, "action": {"save"},
		"title": {" "}, "alt_text": {strings.Repeat("替", 251)}, "caption": {"保留这段说明"},
	}
	badEditResponse := httptest.NewRecorder()
	handler.ServeHTTP(badEditResponse, authenticatedMediaFormRequest("https://example.test/admin/media/1/edit/", badEdit, token))
	badEditBody := badEditResponse.Body.String()
	if badEditResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(badEditBody, "标题必填且最多 160 个字符") ||
		!strings.Contains(badEditBody, "替代文本最多 250 个字符") || !strings.Contains(badEditBody, "保留这段说明") {
		t.Fatalf("invalid edit metadata = %d %s", badEditResponse.Code, badEditBody)
	}
	if current, err := server.media.Get(1); err != nil || current.Version != 1 || current.Title != "封面" {
		t.Fatalf("invalid edit mutated item = %#v, %v", current, err)
	}

	wrongCSRF := url.Values{
		"csrf_token": {"invalid"}, "version": {"1"}, "action": {"save"},
		"title": {"封面"}, "alt_text": {""}, "caption": {""},
	}
	wrongCSRFResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongCSRFResponse, authenticatedMediaFormRequest("https://example.test/admin/media/1/edit/", wrongCSRF, token))
	if wrongCSRFResponse.Code != http.StatusForbidden || !strings.Contains(wrongCSRFResponse.Body.String(), "csrf_validation_failed") {
		t.Fatalf("bad CSRF edit = %d %s", wrongCSRFResponse.Code, wrongCSRFResponse.Body.String())
	}

	unknown := url.Values{
		"csrf_token": {principal.CSRFToken}, "version": {"1"}, "action": {"save"},
		"title": {"封面"}, "alt_text": {""}, "caption": {""}, "unexpected": {"value"},
	}
	unknownResponse := httptest.NewRecorder()
	handler.ServeHTTP(unknownResponse, authenticatedMediaFormRequest("https://example.test/admin/media/1/edit/", unknown, token))
	if unknownResponse.Code != http.StatusBadRequest || !strings.Contains(unknownResponse.Body.String(), "invalid_form_shape") {
		t.Fatalf("unknown edit field = %d %s", unknownResponse.Code, unknownResponse.Body.String())
	}

	repeated := url.Values{
		"csrf_token": {principal.CSRFToken}, "version": {"1"}, "action": {"save"},
		"title": {"First", "Second"}, "alt_text": {""}, "caption": {""},
	}
	repeatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(repeatedResponse, authenticatedMediaFormRequest("https://example.test/admin/media/1/edit/", repeated, token))
	if repeatedResponse.Code != http.StatusBadRequest || !strings.Contains(repeatedResponse.Body.String(), "invalid_form_shape") {
		t.Fatalf("repeated edit field = %d %s", repeatedResponse.Code, repeatedResponse.Body.String())
	}
}

func TestAdminEditorRendersRecentImageMediaPicker(t *testing.T) {
	server, handler, token, _, _ := newMediaHTTPTestServer(t, 8192)
	imageItem, err := server.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 3, 2)), media.UploadInput{
		Filename: "editor-cover.png", Title: "编辑器封面", AltText: "用于文章编辑器", UploadedBy: "editor",
	}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	document, err := server.media.Upload(t.Context(), strings.NewReader("%PDF-1.7\n%%EOF\n"), media.UploadInput{
		Filename: "guide.pdf", Title: "编辑指南", UploadedBy: "editor",
	}, 8192)
	if err != nil {
		t.Fatal(err)
	}

	request := authenticatedMediaRequest(http.MethodGet, "https://example.test/admin/content/new/", nil, token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "最近上传") ||
		!strings.Contains(body, `data-cover-media="`+imageItem.PublicURL+`"`) || !strings.Contains(body, "编辑器封面") {
		t.Fatalf("editor media picker = %d %s", response.Code, body)
	}
	if strings.Contains(body, `data-cover-media="`+document.PublicURL+`"`) || strings.Contains(body, "先上传图片到安全媒体库") {
		t.Fatalf("editor picker included non-image or empty state: %s", body)
	}
}

func newMediaHTTPTestServer(t *testing.T, maximum int64) (*Server, http.Handler, string, auth.Principal, *site.Manager) {
	t.Helper()
	password := "correct horse battery staple 2026"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	adminAuth, err := auth.New(auth.Config{Username: "editor", PasswordHash: hash, AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	library, err := media.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings, err := site.NewManager("", site.DefaultSettings("NewPress", "Test"))
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithSiteSettingsAndMedia(config.Config{
		SiteTitle: "NewPress", SiteTagline: "Test", PublicURL: "https://example.test", ActiveTheme: "chunnel",
		AdminSessionTTL: time.Hour, MaxUploadBytes: maximum,
	}, content.NewMemoryRepository(true), themes, plugin.NewManager(t.TempDir()), settings, library, adminAuth)
	token, principal, err := adminAuth.Authenticate("test-client", "editor", password)
	if err != nil {
		t.Fatal(err)
	}
	return server, server.routes(), token, principal, settings
}

func mediaUploadRequest(t *testing.T, token, csrf, filename string, payload []byte, title, alt, caption string) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for key, value := range map[string]string{"csrf_token": csrf, "title": title, "alt_text": alt, "caption": caption} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := authenticatedMediaRequest(http.MethodPost, "https://example.test/admin/media/upload/", body, token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func authenticatedMediaFormRequest(target string, form url.Values, token string) *http.Request {
	request := authenticatedMediaRequest(http.MethodPost, target, strings.NewReader(form.Encode()), token)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

func authenticatedMediaRequest(method, target string, body io.Reader, token string) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	request.Header.Set("Origin", "https://example.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	return request
}

func httpTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(35 + x), G: uint8(145 + y), B: 183, A: 255})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
