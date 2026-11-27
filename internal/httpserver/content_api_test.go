package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

func TestAdminContentAPIAuthenticationSessionAndReadDTOs(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	created, err := repository.Create(t.Context(), content.PostInput{
		Type: content.TypePost, Status: content.StatusDraft, Slug: "api-read-contract",
		Title: "API 读取契约", ContentSource: `<p>公开投影</p><script>alert(1)</script>`,
		CoverURL: "/theme-assets/images/cover-core.svg", Category: "接口设计",
		CommentStatus: content.CommentOpen, AuthorName: "editor", Actor: "editor",
	})
	if err != nil {
		t.Fatal(err)
	}

	unauthenticated := serveContentAPI(handler, nil, http.MethodGet, "https://example.test/api/v1/admin/session", "", nil)
	if unauthenticated.Code != http.StatusUnauthorized || unauthenticated.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("unauthenticated session = %d %s", unauthenticated.Code, unauthenticated.Body.String())
	}
	var unauthenticatedError apiErrorEnvelope
	decodeAPIResponse(t, unauthenticated, &unauthenticatedError)
	if unauthenticatedError.Error.Code != "auth.authentication_required" || unauthenticatedError.Error.RequestID == "" {
		t.Fatalf("unauthenticated error = %#v", unauthenticatedError)
	}

	session := serveContentAPI(handler, cookie, http.MethodGet, "https://example.test/api/v1/admin/session", "", nil)
	if session.Code != http.StatusOK {
		t.Fatalf("session = %d %s", session.Code, session.Body.String())
	}
	var sessionBody adminSessionAPIResponse
	decodeAPIResponse(t, session, &sessionBody)
	if !sessionBody.Authenticated || sessionBody.ReadOnly || sessionBody.Username != "editor" ||
		sessionBody.CSRFToken != principal.CSRFToken || sessionBody.ExpiresAt == nil || len(sessionBody.Capabilities) < 5 {
		t.Fatalf("session body = %#v", sessionBody)
	}

	list := serveContentAPI(handler, cookie, http.MethodGet,
		"https://example.test/api/v1/admin/contents?type=post&status=draft&page=1&per_page=1", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	if body := list.Body.String(); strings.Contains(body, "content_source") || strings.Contains(body, "content_html") || strings.Contains(body, `"meta"`) {
		t.Fatalf("list leaked heavy/editorial fields: %s", body)
	}
	var listBody adminContentsAPIResponse
	decodeAPIResponse(t, list, &listBody)
	if len(listBody.Items) != 1 || listBody.Items[0].ID != created.ID || listBody.Pagination.Total != 1 ||
		listBody.Pagination.PerPage != 1 || listBody.StatusCounts[content.StatusDraft] != 1 {
		t.Fatalf("list body = %#v", listBody)
	}

	target := "https://example.test/api/v1/admin/contents/" + strconv.FormatInt(created.ID, 10)
	item := serveContentAPI(handler, cookie, http.MethodGet, target, "", nil)
	if item.Code != http.StatusOK || !strings.HasPrefix(item.Header().Get("ETag"), `"content-1-v1-t`) || item.Header().Get("Last-Modified") == "" ||
		item.Header().Get("Vary") != "Cookie" {
		t.Fatalf("item = %d etag=%q body=%s", item.Code, item.Header().Get("ETag"), item.Body.String())
	}
	if body := item.Body.String(); !strings.Contains(body, "content_source") || !strings.Contains(body, "content_html") || strings.Contains(body, `"meta"`) {
		t.Fatalf("item DTO contract = %s", body)
	}
	var itemBody adminContentAPIResponse
	decodeAPIResponse(t, item, &itemBody)
	if itemBody.ContentSource == "" || strings.Contains(itemBody.ContentHTML, "script") || itemBody.ETag != item.Header().Get("ETag") {
		t.Fatalf("item body = %#v", itemBody)
	}

	notModified := serveContentAPI(handler, cookie, http.MethodGet, target, "", map[string]string{"If-None-Match": item.Header().Get("ETag")})
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 || notModified.Header().Get("ETag") != item.Header().Get("ETag") {
		t.Fatalf("conditional item = %d %#v %s", notModified.Code, notModified.Header(), notModified.Body.String())
	}
}

func TestAdminContentAPIStrictWriteBoundary(t *testing.T) {
	_, handler, _, cookie, principal := newAuthenticatedContentServer(t)
	target := "https://example.test/api/v1/admin/contents"
	valid := `{"type":"post","status":"draft","title":"严格 JSON","slug":"strict-json","content_source":"正文"}`

	missingCSRF := serveContentAPI(handler, cookie, http.MethodPost, target, valid, map[string]string{
		"Content-Type": "application/json", "Origin": "https://example.test", "Sec-Fetch-Site": "same-origin",
	})
	assertAPIError(t, missingCSRF, http.StatusForbidden, "auth.csrf_validation_failed")

	crossSiteHeaders := authenticatedAPIHeaders(principal)
	crossSiteHeaders["Origin"] = "https://attacker.test"
	crossSiteHeaders["Sec-Fetch-Site"] = "cross-site"
	crossSite := serveContentAPI(handler, cookie, http.MethodPost, target, valid, crossSiteHeaders)
	assertAPIError(t, crossSite, http.StatusForbidden, "auth.cross_site_request_rejected")

	wrongTypeHeaders := authenticatedAPIHeaders(principal)
	wrongTypeHeaders["Content-Type"] = "text/plain"
	wrongType := serveContentAPI(handler, cookie, http.MethodPost, target, valid, wrongTypeHeaders)
	assertAPIError(t, wrongType, http.StatusUnsupportedMediaType, "request.json_content_type_required")

	for name, payload := range map[string]string{
		"unknown field":   `{"title":"未知字段","is_admin":true}`,
		"duplicate field": `{"title":"第一份","title":"第二份"}`,
		"trailing value":  `{"title":"尾随"} {}`,
		"top level array": `[ {"title":"数组"} ]`,
		"null string":     `{"title":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := serveContentAPI(handler, cookie, http.MethodPost, target, payload, authenticatedAPIHeaders(principal))
			assertAPIError(t, response, http.StatusBadRequest, "request.invalid_json")
		})
	}

	invalidTime := serveContentAPI(handler, cookie, http.MethodPost, target,
		`{"status":"future","title":"错误时间","slug":"invalid-time","published_at":"tomorrow"}`,
		authenticatedAPIHeaders(principal))
	assertAPIError(t, invalidTime, http.StatusUnprocessableEntity, "content.validation_failed")
	if !strings.Contains(invalidTime.Body.String(), "published_at") {
		t.Fatalf("invalid time details = %s", invalidTime.Body.String())
	}

	oversizedPayload := `{"title":"超限","content_source":"` + strings.Repeat("x", adminContentAPIRequestMaxBytes) + `"}`
	oversized := serveContentAPI(handler, cookie, http.MethodPost, target, oversizedPayload, authenticatedAPIHeaders(principal))
	assertAPIError(t, oversized, http.StatusRequestEntityTooLarge, "request.body_too_large")

	repeatedQuery := serveContentAPI(handler, cookie, http.MethodGet,
		"https://example.test/api/v1/admin/contents?page=1&page=2", "", nil)
	assertAPIError(t, repeatedQuery, http.StatusBadRequest, "request.invalid_query")

	methodNotAllowed := serveContentAPI(handler, cookie, http.MethodPut, "https://example.test/api/v1/admin/contents", "", nil)
	assertAPIError(t, methodNotAllowed, http.StatusMethodNotAllowed, "request.method_not_allowed")
	if methodNotAllowed.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("method Allow = %q", methodNotAllowed.Header().Get("Allow"))
	}
}

func TestAdminContentAPICRUDIdempotencyOptimisticLockAndRevisions(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	createTarget := "https://example.test/api/v1/admin/contents"
	createPayload := `{"type":"post","status":"draft","title":"API 第一版","slug":"api-lifecycle","content_source":"<p>第一版</p><script>alert(1)</script>","cover_url":"/theme-assets/images/cover-core.svg","category_ids":[1]}`
	createHeaders := authenticatedAPIHeaders(principal)
	createHeaders["Idempotency-Key"] = "content-create-0001"
	createdResponse := serveContentAPI(handler, cookie, http.MethodPost, createTarget, createPayload, createHeaders)
	createdETag := createdResponse.Header().Get("ETag")
	if createdResponse.Code != http.StatusCreated || createdResponse.Header().Get("Location") != "/api/v1/admin/contents/1" ||
		!strings.HasPrefix(createdETag, `"content-1-v1-t`) {
		t.Fatalf("create = %d %#v %s", createdResponse.Code, createdResponse.Header(), createdResponse.Body.String())
	}
	var created adminContentAPIResponse
	decodeAPIResponse(t, createdResponse, &created)
	if created.ID != 1 || created.Version != 1 || strings.Contains(created.ContentHTML, "script") || created.IsPublic {
		t.Fatalf("created = %#v", created)
	}
	retiredCategory := serveContentAPI(handler, cookie, http.MethodPost, createTarget, `{"title":"旧分类输入","category":"旧栏目"}`, authenticatedAPIHeaders(principal))
	assertAPIError(t, retiredCategory, http.StatusBadRequest, "request.invalid_json")

	replayed := serveContentAPI(handler, cookie, http.MethodPost, createTarget, createPayload, createHeaders)
	if replayed.Code != http.StatusCreated || replayed.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replayed create = %d %#v %s", replayed.Code, replayed.Header(), replayed.Body.String())
	}
	var replayedBody adminContentAPIResponse
	decodeAPIResponse(t, replayed, &replayedBody)
	adminList, err := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 100})
	if err != nil || replayedBody.ID != created.ID || adminList.Total != 1 {
		t.Fatalf("idempotent create = %#v total=%d err=%v", replayedBody, adminList.Total, err)
	}

	conflictingPayload := strings.Replace(createPayload, "API 第一版", "另一份请求", 1)
	idempotencyConflict := serveContentAPI(handler, cookie, http.MethodPost, createTarget, conflictingPayload, createHeaders)
	assertAPIError(t, idempotencyConflict, http.StatusConflict, "request.idempotency_conflict")

	itemTarget := "https://example.test/api/v1/admin/contents/1"
	missingPrecondition := serveContentAPI(handler, cookie, http.MethodPatch, itemTarget, `{"title":"无锁更新"}`, authenticatedAPIHeaders(principal))
	assertAPIError(t, missingPrecondition, http.StatusPreconditionRequired, "content.precondition_required")

	staleHeaders := authenticatedAPIHeaders(principal)
	staleHeaders["If-Match"] = `"content-1-v999"`
	stale := serveContentAPI(handler, cookie, http.MethodPatch, itemTarget, `{"title":"过期更新"}`, staleHeaders)
	assertAPIError(t, stale, http.StatusPreconditionFailed, "content.precondition_failed")
	if stale.Header().Get("ETag") != createdETag {
		t.Fatalf("stale response current etag = %q", stale.Header().Get("ETag"))
	}

	updateHeaders := authenticatedAPIHeaders(principal)
	updateHeaders["If-Match"] = createdETag
	updatedResponse := serveContentAPI(handler, cookie, http.MethodPatch, itemTarget,
		`{"title":"API 第二版","status":"publish","content_source":"<p>第二版</p><img src=x onerror=alert(1)>"}`,
		updateHeaders)
	updatedETag := updatedResponse.Header().Get("ETag")
	if updatedResponse.Code != http.StatusOK || !strings.HasPrefix(updatedETag, `"content-1-v2-t`) {
		t.Fatalf("update = %d %#v %s", updatedResponse.Code, updatedResponse.Header(), updatedResponse.Body.String())
	}
	var updated adminContentAPIResponse
	decodeAPIResponse(t, updatedResponse, &updated)
	if updated.Version != 2 || updated.Status != content.StatusPublish || !updated.IsPublic || strings.Contains(updated.ContentHTML, "onerror") {
		t.Fatalf("updated = %#v", updated)
	}

	staleRetry := serveContentAPI(handler, cookie, http.MethodPatch, itemTarget, `{"title":"重复旧写入"}`, updateHeaders)
	assertAPIError(t, staleRetry, http.StatusPreconditionFailed, "content.precondition_failed")

	revisionsTarget := itemTarget + "/revisions?limit=20"
	revisionsResponse := serveContentAPI(handler, cookie, http.MethodGet, revisionsTarget, "", nil)
	if revisionsResponse.Code != http.StatusOK || strings.Contains(revisionsResponse.Body.String(), "content_source") {
		t.Fatalf("revisions = %d %s", revisionsResponse.Code, revisionsResponse.Body.String())
	}
	var revisions adminRevisionsAPIResponse
	decodeAPIResponse(t, revisionsResponse, &revisions)
	if revisions.Count != 2 || revisions.CurrentVersion != 2 || revisions.CurrentETag != updatedETag {
		t.Fatalf("revisions body = %#v", revisions)
	}
	var firstRevisionID int64
	for _, revision := range revisions.Items {
		if revision.Version == 1 {
			firstRevisionID = revision.ID
		}
	}
	if firstRevisionID == 0 {
		t.Fatalf("first revision missing: %#v", revisions)
	}

	deleteHeaders := authenticatedAPIHeaders(principal)
	deleteHeaders["If-Match"] = updatedETag
	deletedResponse := serveContentAPI(handler, cookie, http.MethodDelete, itemTarget, "", deleteHeaders)
	deletedETag := deletedResponse.Header().Get("ETag")
	if deletedResponse.Code != http.StatusOK || !strings.HasPrefix(deletedETag, `"content-1-v3-t`) {
		t.Fatalf("delete = %d %#v %s", deletedResponse.Code, deletedResponse.Header(), deletedResponse.Body.String())
	}
	var deleted adminContentAPIResponse
	decodeAPIResponse(t, deletedResponse, &deleted)
	if deleted.Status != content.StatusTrash || deleted.IsPublic {
		t.Fatalf("deleted = %#v", deleted)
	}

	restoreHeaders := authenticatedAPIHeaders(principal)
	restoreHeaders["If-Match"] = deletedETag
	restoreTarget := itemTarget + "/revisions/" + strconv.FormatInt(firstRevisionID, 10) + "/restore"
	restoredResponse := serveContentAPI(handler, cookie, http.MethodPost, restoreTarget, "", restoreHeaders)
	restoredETag := restoredResponse.Header().Get("ETag")
	if restoredResponse.Code != http.StatusOK || !strings.HasPrefix(restoredETag, `"content-1-v4-t`) {
		t.Fatalf("restore = %d %#v %s", restoredResponse.Code, restoredResponse.Header(), restoredResponse.Body.String())
	}
	var restored adminContentAPIResponse
	decodeAPIResponse(t, restoredResponse, &restored)
	if restored.Title != "API 第一版" || restored.Status != content.StatusTrash || restored.Version != 4 {
		t.Fatalf("restored = %#v", restored)
	}

	recoverHeaders := authenticatedAPIHeaders(principal)
	recoverHeaders["If-Match"] = restoredETag
	recoveredResponse := serveContentAPI(handler, cookie, http.MethodPatch, itemTarget, `{"status":"draft"}`, recoverHeaders)
	if recoveredResponse.Code != http.StatusOK {
		t.Fatalf("recover draft = %d %s", recoveredResponse.Code, recoveredResponse.Body.String())
	}
	var recovered adminContentAPIResponse
	decodeAPIResponse(t, recoveredResponse, &recovered)
	if recovered.Status != content.StatusDraft || recovered.Version != 5 {
		t.Fatalf("recovered = %#v", recovered)
	}

	pagePayload := `{"type":"page","status":"draft","title":"API 页面","slug":"api-page"}`
	pageResponse := serveContentAPI(handler, cookie, http.MethodPost, createTarget, pagePayload, authenticatedAPIHeaders(principal))
	if pageResponse.Code != http.StatusCreated {
		t.Fatalf("page create = %d %s", pageResponse.Code, pageResponse.Body.String())
	}
	var page adminContentAPIResponse
	decodeAPIResponse(t, pageResponse, &page)
	if page.Type != content.TypePage || page.CommentStatus != content.CommentClosed || page.Category != "" {
		t.Fatalf("page defaults = %#v", page)
	}

	futureTime := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	futurePayload := `{"type":"post","status":"publish","title":"API 定时文章","slug":"api-future","published_at":"` + futureTime + `"}`
	futureResponse := serveContentAPI(handler, cookie, http.MethodPost, createTarget, futurePayload, authenticatedAPIHeaders(principal))
	if futureResponse.Code != http.StatusCreated {
		t.Fatalf("future create = %d %s", futureResponse.Code, futureResponse.Body.String())
	}
	var future adminContentAPIResponse
	decodeAPIResponse(t, futureResponse, &future)
	if future.Status != content.StatusFuture || future.IsPublic {
		t.Fatalf("future = %#v", future)
	}
	futurePublishHeaders := authenticatedAPIHeaders(principal)
	futurePublishHeaders["If-Match"] = future.ETag
	futurePublishTarget := createTarget + "/" + strconv.FormatInt(future.ID, 10)
	futurePublishedResponse := serveContentAPI(handler, cookie, http.MethodPatch, futurePublishTarget,
		`{"status":"publish","published_at":null}`, futurePublishHeaders)
	if futurePublishedResponse.Code != http.StatusOK {
		t.Fatalf("publish future now = %d %s", futurePublishedResponse.Code, futurePublishedResponse.Body.String())
	}
	var futurePublished adminContentAPIResponse
	decodeAPIResponse(t, futurePublishedResponse, &futurePublished)
	if futurePublished.Status != content.StatusPublish || !futurePublished.IsPublic || futurePublished.PublishedAt == nil {
		t.Fatalf("future published = %#v", futurePublished)
	}

	slugConflictPayload := `{"type":"post","status":"draft","title":"冲突别名","slug":"api-lifecycle"}`
	slugConflict := serveContentAPI(handler, cookie, http.MethodPost, createTarget, slugConflictPayload, authenticatedAPIHeaders(principal))
	assertAPIError(t, slugConflict, http.StatusConflict, "content.slug_conflict")
}

func TestAdminContentAPIUnconfiguredLoopbackIsReadOnly(t *testing.T) {
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	server := New(
		config.Config{SiteTitle: "NewPress", PublicURL: "http://localhost:8080", ActiveTheme: "chunnel"},
		content.NewMemoryRepository(false), themes, plugin.NewManager(t.TempDir()),
	)
	handler := server.routes()

	loopbackRequest := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/admin/session", nil)
	loopbackRequest.RemoteAddr = "127.0.0.1:41234"
	loopbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(loopbackResponse, loopbackRequest)
	if loopbackResponse.Code != http.StatusOK {
		t.Fatalf("loopback session = %d %s", loopbackResponse.Code, loopbackResponse.Body.String())
	}
	var loopback adminSessionAPIResponse
	decodeAPIResponse(t, loopbackResponse, &loopback)
	if !loopback.ReadOnly || loopback.Authenticated || len(loopback.Capabilities) != 6 ||
		loopback.Capabilities[0] != "content.read" || loopback.Capabilities[1] != "comments.read" ||
		loopback.Capabilities[2] != "comments.revisions.read" || loopback.Capabilities[3] != "sessions.read" || loopback.Capabilities[4] != "audit.read" || loopback.Capabilities[5] != "updates.read" {
		t.Fatalf("loopback session body = %#v", loopback)
	}

	writeRequest := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/admin/contents", strings.NewReader(`{"title":"forbidden"}`))
	writeRequest.RemoteAddr = "127.0.0.1:41234"
	writeRequest.Header.Set("Content-Type", "application/json")
	writeResponse := httptest.NewRecorder()
	handler.ServeHTTP(writeResponse, writeRequest)
	assertAPIError(t, writeResponse, http.StatusForbidden, "auth.write_disabled")

	externalRequest := httptest.NewRequest(http.MethodGet, "http://example.test/api/v1/admin/session", nil)
	externalRequest.RemoteAddr = "198.51.100.20:41234"
	externalResponse := httptest.NewRecorder()
	handler.ServeHTTP(externalResponse, externalRequest)
	assertAPIError(t, externalResponse, http.StatusNotFound, "request.not_found")
}

func authenticatedAPIHeaders(principal auth.Principal) map[string]string {
	return map[string]string{
		"Content-Type":   "application/json; charset=utf-8",
		"Origin":         "https://example.test",
		"Sec-Fetch-Site": "same-origin",
		"X-CSRF-Token":   principal.CSRFToken,
	}
}

func serveContentAPI(handler http.Handler, cookie *http.Cookie, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeAPIResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode API response: %v; body=%s", err, response.Body.String())
	}
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("API status = %d, want %d; body=%s", response.Code, status, response.Body.String())
	}
	var envelope apiErrorEnvelope
	decodeAPIResponse(t, response, &envelope)
	if envelope.Error.Code != code || envelope.Error.Message == "" || envelope.Error.RequestID == "" {
		t.Fatalf("API error = %#v, want code %q", envelope, code)
	}
}
