package httpserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
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

func TestAdminContentCreatePublishConflictRevisionAndTrash(t *testing.T) {
	server, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	_ = server

	listRequest := httptest.NewRequest(http.MethodGet, "https://example.test/admin/content/", nil)
	listRequest.AddCookie(cookie)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), "<h1>文章与页面</h1>") {
		t.Fatalf("content list = %d %s", listResponse.Code, listResponse.Body.String())
	}

	create := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"save"}, "type": {content.TypePost},
		"status": {content.StatusDraft}, "title": {"安全发布流水线"}, "slug": {"secure-publishing-flow"},
		"excerpt": {""}, "content_source": {`<p>第一版</p><script>alert(1)</script>`},
		"cover_url": {"/theme-assets/images/cover-core.svg"}, "category": {"项目日志"},
		"comment_status": {content.CommentOpen}, "published_at": {""},
		"published_timezone": {"Asia/Shanghai"}, "published_source": {"canonical"},
	}
	createResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/new/", "https://example.test", create)
	if createResponse.Code != http.StatusSeeOther {
		t.Fatalf("create = %d %s", createResponse.Code, createResponse.Body.String())
	}
	match := regexp.MustCompile(`/admin/content/(\d+)/edit/\?notice=created`).FindStringSubmatch(createResponse.Header().Get("Location"))
	if len(match) != 2 {
		t.Fatalf("create location = %q", createResponse.Header().Get("Location"))
	}
	id, _ := strconv.ParseInt(match[1], 10, 64)
	post, err := repository.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if post.Version != 1 || post.Status != content.StatusDraft || strings.Contains(post.ContentHTML, "script") || post.Excerpt == "" {
		t.Fatalf("created post = %#v", post)
	}

	editRequest := httptest.NewRequest(http.MethodGet, "https://example.test/admin/content/"+match[1]+"/edit/", nil)
	editRequest.AddCookie(cookie)
	editResponse := httptest.NewRecorder()
	handler.ServeHTTP(editResponse, editRequest)
	if editResponse.Code != http.StatusOK || !strings.Contains(editResponse.Body.String(), "修订时间线") || !strings.Contains(editResponse.Body.String(), "安全发布流水线") {
		t.Fatalf("edit page = %d %s", editResponse.Code, editResponse.Body.String())
	}

	publish := create
	publish.Set("version", "1")
	publish.Set("status", content.StatusPublish)
	publish.Set("content_source", "<p>第二版正文</p>")
	publishResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/"+match[1]+"/edit/", "https://example.test", publish)
	if publishResponse.Code != http.StatusSeeOther || !strings.Contains(publishResponse.Header().Get("Location"), "notice=saved") {
		t.Fatalf("publish save = %d %q %s", publishResponse.Code, publishResponse.Header().Get("Location"), publishResponse.Body.String())
	}
	post, _ = repository.GetByID(t.Context(), id)
	if post.Version != 2 || post.Status != content.StatusPublish {
		t.Fatalf("published post = %#v", post)
	}

	publicRequest := httptest.NewRequest(http.MethodGet, "https://example.test/posts/secure-publishing-flow", nil)
	publicResponse := httptest.NewRecorder()
	handler.ServeHTTP(publicResponse, publicRequest)
	if publicResponse.Code != http.StatusOK || !strings.Contains(publicResponse.Body.String(), "第二版正文") {
		t.Fatalf("public post = %d %s", publicResponse.Code, publicResponse.Body.String())
	}
	apiRequest := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/posts", nil)
	apiResponse := httptest.NewRecorder()
	handler.ServeHTTP(apiResponse, apiRequest)
	if body := apiResponse.Body.String(); apiResponse.Code != http.StatusOK || strings.Contains(body, "content_source") || strings.Contains(body, "<script>") {
		t.Fatalf("public API leaked editorial source = %d %s", apiResponse.Code, body)
	}

	stale := cloneValues(publish)
	stale.Set("title", "过期会话覆盖")
	staleResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/"+match[1]+"/edit/", "https://example.test", stale)
	if staleResponse.Code != http.StatusConflict || !strings.Contains(staleResponse.Body.String(), "检测到并发编辑") {
		t.Fatalf("stale update = %d %s", staleResponse.Code, staleResponse.Body.String())
	}
	post, _ = repository.GetByID(t.Context(), id)
	if post.Title != "安全发布流水线" || post.Version != 2 {
		t.Fatalf("stale update mutated content: %#v", post)
	}

	third := cloneValues(publish)
	third.Set("version", "2")
	third.Set("title", "安全发布流水线 2.0")
	thirdResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/"+match[1]+"/edit/", "https://example.test", third)
	if thirdResponse.Code != http.StatusSeeOther {
		t.Fatalf("third revision = %d %s", thirdResponse.Code, thirdResponse.Body.String())
	}
	post, _ = repository.GetByID(t.Context(), id)
	revisions, err := repository.ListRevisions(t.Context(), id, 20)
	if err != nil || len(revisions) != 3 {
		t.Fatalf("revisions = %#v, err=%v", revisions, err)
	}
	var revisionTwoID int64
	for _, revision := range revisions {
		if revision.Version == 2 {
			revisionTwoID = revision.ID
		}
	}
	restore := url.Values{
		"csrf_token": {principal.CSRFToken}, "version": {strconv.FormatInt(post.Version, 10)},
		"action": {"restore_revision-" + strconv.FormatInt(revisionTwoID, 10)},
	}
	restoreResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/"+match[1]+"/edit/", "https://example.test", restore)
	if restoreResponse.Code != http.StatusSeeOther || !strings.Contains(restoreResponse.Header().Get("Location"), "revision-restored") {
		t.Fatalf("restore revision = %d %q %s", restoreResponse.Code, restoreResponse.Header().Get("Location"), restoreResponse.Body.String())
	}
	post, _ = repository.GetByID(t.Context(), id)
	if post.Title != "安全发布流水线" || post.Version != 4 || post.Status != content.StatusPublish {
		t.Fatalf("restored post = %#v", post)
	}

	trash := url.Values{
		"csrf_token": {principal.CSRFToken}, "version": {strconv.FormatInt(post.Version, 10)}, "action": {"trash"},
	}
	trashResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/"+match[1]+"/edit/", "https://example.test", trash)
	if trashResponse.Code != http.StatusSeeOther || !strings.Contains(trashResponse.Header().Get("Location"), "trashed") {
		t.Fatalf("trash = %d %s", trashResponse.Code, trashResponse.Body.String())
	}
	publicAfterTrash := httptest.NewRequest(http.MethodGet, "https://example.test/posts/secure-publishing-flow", nil)
	publicAfterTrashResponse := httptest.NewRecorder()
	handler.ServeHTTP(publicAfterTrashResponse, publicAfterTrash)
	if publicAfterTrashResponse.Code != http.StatusNotFound {
		t.Fatalf("trashed public post status = %d", publicAfterTrashResponse.Code)
	}
}

func TestAdminContentSecurityValidationAndPublishedPage(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	newPageRequest := httptest.NewRequest(http.MethodGet, "https://example.test/admin/content/new/?type=page", nil)
	newPageRequest.AddCookie(cookie)
	newPageResponse := httptest.NewRecorder()
	handler.ServeHTTP(newPageResponse, newPageRequest)
	newPageBody := newPageResponse.Body.String()
	if newPageResponse.Code != http.StatusOK || !strings.Contains(newPageBody, "新建页面") ||
		!strings.Contains(newPageBody, `<option value="closed" selected`) ||
		!strings.Contains(newPageBody, `type="text" inputmode="url" name="cover_url"`) {
		t.Fatalf("new page defaults = %d %s", newPageResponse.Code, newPageBody)
	}
	base := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"save"}, "type": {content.TypePage},
		"status": {content.StatusPublish}, "title": {"关于我们"}, "slug": {"about"},
		"excerpt": {"NewPress 项目介绍"}, "content_source": {"<h2>使命</h2><p>建立更安全的内容核心。</p>"},
		"cover_url": {""}, "category": {""}, "comment_status": {content.CommentClosed}, "published_at": {""},
		"published_timezone": {"Asia/Shanghai"}, "published_source": {"canonical"},
	}

	crossSite := serveContentForm(handler, cookie, "https://example.test/admin/content/new/", "https://attacker.test", base)
	if crossSite.Code != http.StatusForbidden {
		t.Fatalf("cross-site create status = %d", crossSite.Code)
	}

	badCSRF := cloneValues(base)
	badCSRF.Set("csrf_token", "invalid")
	badCSRFResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/new/", "https://example.test", badCSRF)
	if badCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("bad CSRF status = %d", badCSRFResponse.Code)
	}

	unknown := cloneValues(base)
	unknown.Set("is_admin", "true")
	unknownResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/new/", "https://example.test", unknown)
	if unknownResponse.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", unknownResponse.Code)
	}

	unsafe := cloneValues(base)
	unsafe.Set("slug", "unsafe-cover")
	unsafe.Set("cover_url", "javascript:alert(1)")
	unsafeResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/new/", "https://example.test", unsafe)
	if unsafeResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(unsafeResponse.Body.String(), "仅允许站内绝对路径") {
		t.Fatalf("unsafe cover = %d %s", unsafeResponse.Code, unsafeResponse.Body.String())
	}

	external := cloneValues(base)
	external.Set("slug", "external-cover")
	external.Set("cover_url", "https://cdn.example.test/cover.jpg")
	externalResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/new/", "https://example.test", external)
	externalBody := externalResponse.Body.String()
	if externalResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(externalBody, "导入媒体库") ||
		!strings.Contains(externalBody, "保存失败，输入尚未写入") || strings.Contains(externalBody, `src="https://cdn.example.test/cover.jpg"`) {
		t.Fatalf("external cover = %d %s", externalResponse.Code, externalBody)
	}

	oversized := cloneValues(base)
	oversized.Set("content_source", strings.Repeat("x", 3<<20))
	oversizedResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/new/", "https://example.test", oversized)
	if oversizedResponse.Code != http.StatusRequestEntityTooLarge || !strings.Contains(oversizedResponse.Body.String(), "content_form_too_large") {
		t.Fatalf("oversized form = %d %s", oversizedResponse.Code, oversizedResponse.Body.String())
	}

	created := serveContentForm(handler, cookie, "https://example.test/admin/content/new/", "https://example.test", base)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("page create = %d %s", created.Code, created.Body.String())
	}
	page, err := repository.GetPublishedPageBySlug(t.Context(), "about")
	if err != nil || page.Type != content.TypePage {
		t.Fatalf("published page = %#v, err=%v", page, err)
	}
	pageRequest := httptest.NewRequest(http.MethodGet, "https://example.test/pages/about", nil)
	pageResponse := httptest.NewRecorder()
	handler.ServeHTTP(pageResponse, pageRequest)
	if pageResponse.Code != http.StatusOK || !strings.Contains(pageResponse.Body.String(), "站点页面") || !strings.Contains(pageResponse.Body.String(), "建立更安全的内容核心") {
		t.Fatalf("public page = %d %s", pageResponse.Code, pageResponse.Body.String())
	}

	filteredRequest := httptest.NewRequest(http.MethodGet, "https://example.test/admin/content/?type=page&q=%E5%85%B3%E4%BA%8E", nil)
	filteredRequest.AddCookie(cookie)
	filteredResponse := httptest.NewRecorder()
	handler.ServeHTTP(filteredResponse, filteredRequest)
	if filteredResponse.Code != http.StatusOK || !strings.Contains(filteredResponse.Body.String(), "关于我们") {
		t.Fatalf("filtered list = %d %s", filteredResponse.Code, filteredResponse.Body.String())
	}

	if _, err := repository.GetPublishedBySlug(t.Context(), "about"); !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("page leaked through post route: %v", err)
	}
}

func newAuthenticatedContentServer(t *testing.T) (*Server, http.Handler, *content.MemoryRepository, *http.Cookie, auth.Principal) {
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
	token, principal, err := adminAuth.Authenticate("content-test", "editor", password)
	if err != nil {
		t.Fatal(err)
	}
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	repository := content.NewMemoryRepository(false)
	server := New(config.Config{
		SiteTitle: "NewPress", SiteTagline: "Test", PublicURL: "https://example.test", ActiveTheme: "chunnel",
		AdminSessionTTL: time.Hour,
	}, repository, themes, plugin.NewManager(t.TempDir()), adminAuth)
	return server, server.routes(), repository, &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}, principal
}

func serveContentForm(handler http.Handler, cookie *http.Cookie, target, origin string, values url.Values) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func cloneValues(values url.Values) url.Values {
	result := make(url.Values, len(values))
	for key, entries := range values {
		result[key] = append([]string(nil), entries...)
	}
	return result
}
