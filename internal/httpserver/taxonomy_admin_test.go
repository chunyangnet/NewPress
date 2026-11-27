package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func TestAdminTaxonomyAuthenticationAndRequestValidation(t *testing.T) {
	server, handler, _, cookie, principal := newAuthenticatedContentServer(t)

	unauthenticated := httptest.NewRequest(http.MethodGet, "https://example.test/admin/taxonomy/", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusSeeOther || !strings.HasPrefix(unauthenticatedResponse.Header().Get("Location"), "/admin/login/") {
		t.Fatalf("unauthenticated taxonomy = %d %q", unauthenticatedResponse.Code, unauthenticatedResponse.Header().Get("Location"))
	}

	for _, target := range []string{
		"https://example.test/admin/taxonomy/?taxonomy=unknown",
		"https://example.test/admin/taxonomy/?page=0",
		"https://example.test/admin/taxonomy/?edit=-1",
		"https://example.test/admin/taxonomy/?taxonomy=category&taxonomy=post_tag",
		"https://example.test/admin/taxonomy/?unexpected=true",
		"https://example.test/admin/taxonomy/?q=" + url.QueryEscape(strings.Repeat("检", content.MaxTermSearchRunes+1)),
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid taxonomy query %q = %d, body=%s", target, response.Code, response.Body.String())
		}
	}

	base := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"create"}, "taxonomy": {content.TaxonomyCategory},
		"name": {"安全分类"}, "slug": {"secure-category"}, "parent_id": {"0"}, "description": {"请求形状测试"},
	}

	crossSite := serveTaxonomyForm(handler, cookie, "https://attacker.test", base)
	assertJSONError(t, crossSite, http.StatusForbidden, "cross_site_request_rejected")

	badCSRF := cloneValues(base)
	badCSRF.Set("csrf_token", "invalid")
	assertJSONError(t, serveTaxonomyForm(handler, cookie, "https://example.test", badCSRF), http.StatusForbidden, "csrf_validation_failed")

	unknown := cloneValues(base)
	unknown.Set("is_system", "true")
	assertJSONError(t, serveTaxonomyForm(handler, cookie, "https://example.test", unknown), http.StatusBadRequest, "invalid_taxonomy_form_shape")

	duplicate := cloneValues(base)
	duplicate["name"] = []string{"安全分类", "覆盖名称"}
	assertJSONError(t, serveTaxonomyForm(handler, cookie, "https://example.test", duplicate), http.StatusBadRequest, "invalid_taxonomy_form_shape")

	wrongTypeRequest := httptest.NewRequest(http.MethodPost, "https://example.test/admin/taxonomy/", strings.NewReader(base.Encode()))
	wrongTypeRequest.Header.Set("Content-Type", "application/json")
	wrongTypeRequest.Header.Set("Origin", "https://example.test")
	wrongTypeRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	wrongTypeRequest.AddCookie(cookie)
	wrongTypeResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongTypeResponse, wrongTypeRequest)
	assertJSONError(t, wrongTypeResponse, http.StatusUnsupportedMediaType, "form_content_type_required")

	oversized := cloneValues(base)
	oversized.Set("description", strings.Repeat("x", 70<<10))
	assertJSONError(t, serveTaxonomyForm(handler, cookie, "https://example.test", oversized), http.StatusRequestEntityTooLarge, "taxonomy_form_too_large")

	unconfigured, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	server.auth = unconfigured
	readOnlyPage := httptest.NewRequest(http.MethodGet, "http://localhost/admin/taxonomy/", nil)
	readOnlyPage.RemoteAddr = "127.0.0.1:41000"
	readOnlyPageResponse := httptest.NewRecorder()
	handler.ServeHTTP(readOnlyPageResponse, readOnlyPage)
	if readOnlyPageResponse.Code != http.StatusOK || !strings.Contains(readOnlyPageResponse.Body.String(), "只读预览不会接受分类变更") {
		t.Fatalf("read-only taxonomy page = %d %s", readOnlyPageResponse.Code, readOnlyPageResponse.Body.String())
	}
	readOnlyWrite := httptest.NewRequest(http.MethodPost, "http://localhost/admin/taxonomy/", strings.NewReader(base.Encode()))
	readOnlyWrite.RemoteAddr = "[::1]:41000"
	readOnlyWrite.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	readOnlyWriteResponse := httptest.NewRecorder()
	handler.ServeHTTP(readOnlyWriteResponse, readOnlyWrite)
	assertJSONError(t, readOnlyWriteResponse, http.StatusForbidden, "admin_credentials_not_configured")
}

func TestAdminTaxonomyCRUDHierarchyConflictsAndFallback(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)

	parent := createTermThroughAdmin(t, handler, repository, cookie, principal.CSRFToken, content.TermInput{
		Taxonomy: content.TaxonomyCategory, Name: "工程实践", Slug: "工程实践", Description: "长期工程栏目",
	})
	if parent.Slug != "工程实践" || parent.Version != 1 {
		t.Fatalf("parent = %#v", parent)
	}
	child := createTermThroughAdmin(t, handler, repository, cookie, principal.CSRFToken, content.TermInput{
		Taxonomy: content.TaxonomyCategory, ParentID: parent.ID, Name: "性能工程", Slug: "performance", Description: "性能与容量规划",
	})
	if child.ParentID != parent.ID || child.Depth != 1 || child.ParentName != parent.Name {
		t.Fatalf("child = %#v", child)
	}
	tag := createTermThroughAdmin(t, handler, repository, cookie, principal.CSRFToken, content.TermInput{
		Taxonomy: content.TaxonomyTag, Name: "Go", Slug: "go", Description: "Go 语言",
	})
	if tag.ParentID != 0 || tag.Taxonomy != content.TaxonomyTag {
		t.Fatalf("tag = %#v", tag)
	}

	duplicateSlug := taxonomyMutationValues(principal.CSRFToken, "create", content.TermInput{
		Taxonomy: content.TaxonomyCategory, Name: "重复别名", Slug: parent.Slug,
	}, 0, 0)
	duplicateResponse := serveTaxonomyForm(handler, cookie, "https://example.test", duplicateSlug)
	if duplicateResponse.Code != http.StatusConflict || !strings.Contains(duplicateResponse.Body.String(), "别名冲突") {
		t.Fatalf("duplicate slug = %d %s", duplicateResponse.Code, duplicateResponse.Body.String())
	}

	updateParent := taxonomyMutationValues(principal.CSRFToken, "update", content.TermInput{
		Taxonomy: content.TaxonomyCategory, Name: "工程与架构", Slug: parent.Slug, Description: "已更新说明",
	}, parent.ID, parent.Version)
	updateResponse := serveTaxonomyForm(handler, cookie, "https://example.test", updateParent)
	if updateResponse.Code != http.StatusSeeOther {
		t.Fatalf("update parent = %d %s", updateResponse.Code, updateResponse.Body.String())
	}
	updatedParent, err := repository.GetTermByID(t.Context(), parent.ID)
	if err != nil || updatedParent.Version != 2 || updatedParent.Name != "工程与架构" {
		t.Fatalf("updated parent = %#v, err=%v", updatedParent, err)
	}

	stale := cloneValues(updateParent)
	stale.Set("name", "过期覆盖")
	staleResponse := serveTaxonomyForm(handler, cookie, "https://example.test", stale)
	if staleResponse.Code != http.StatusConflict || !strings.Contains(staleResponse.Body.String(), "其他请求修改") {
		t.Fatalf("stale update = %d %s", staleResponse.Code, staleResponse.Body.String())
	}

	cycle := taxonomyMutationValues(principal.CSRFToken, "update", content.TermInput{
		Taxonomy: content.TaxonomyCategory, ParentID: child.ID, Name: updatedParent.Name, Slug: updatedParent.Slug, Description: updatedParent.Description,
	}, updatedParent.ID, updatedParent.Version)
	cycleResponse := serveTaxonomyForm(handler, cookie, "https://example.test", cycle)
	if cycleResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(cycleResponse.Body.String(), "形成循环") {
		t.Fatalf("cycle update = %d %s", cycleResponse.Code, cycleResponse.Body.String())
	}

	terms, err := repository.ListTerms(t.Context(), content.TermQuery{Taxonomy: content.TaxonomyCategory, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var defaultCategory content.Term
	for _, term := range terms.Items {
		if term.System {
			defaultCategory = term
			break
		}
	}
	if defaultCategory.ID == 0 {
		t.Fatal("default category was not found")
	}
	protectedDelete := taxonomyMutationValues(principal.CSRFToken, "delete", content.TermInput{Taxonomy: content.TaxonomyCategory}, defaultCategory.ID, defaultCategory.Version)
	protectedResponse := serveTaxonomyForm(handler, cookie, "https://example.test", protectedDelete)
	if protectedResponse.Code != http.StatusConflict || !strings.Contains(protectedResponse.Body.String(), "不能删除") {
		t.Fatalf("protected delete = %d %s", protectedResponse.Code, protectedResponse.Body.String())
	}

	post, err := repository.Create(t.Context(), content.PostInput{
		Type: content.TypePost, Status: content.StatusPublish, Title: "分类回落验证", Slug: "taxonomy-fallback",
		ContentSource: "<p>验证删除分类后的默认回落。</p>", CategoryIDs: []int64{child.ID}, TagNames: []string{tag.Name},
		CommentStatus: content.CommentOpen, PublishedAt: time.Now().Add(-time.Minute), Actor: "editor",
	})
	if err != nil {
		t.Fatal(err)
	}
	deleteChild := taxonomyMutationValues(principal.CSRFToken, "delete", content.TermInput{Taxonomy: content.TaxonomyCategory}, child.ID, child.Version)
	deleteResponse := serveTaxonomyForm(handler, cookie, "https://example.test", deleteChild)
	if deleteResponse.Code != http.StatusSeeOther || !strings.Contains(deleteResponse.Header().Get("Location"), "notice=deleted") {
		t.Fatalf("delete child = %d %q %s", deleteResponse.Code, deleteResponse.Header().Get("Location"), deleteResponse.Body.String())
	}
	post, err = repository.GetByID(t.Context(), post.ID)
	if err != nil || len(post.Categories) != 1 || post.Categories[0].ID != defaultCategory.ID || post.Category != defaultCategory.Name {
		t.Fatalf("post fallback = %#v, err=%v", post, err)
	}
}

func TestEditorTaxonomyPublicArchivesSitemapAndSystemMetrics(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	category := createTermThroughAdmin(t, handler, repository, cookie, principal.CSRFToken, content.TermInput{
		Taxonomy: content.TaxonomyCategory, Name: "数据工程", Slug: "数据工程", Description: "数据系统实践",
	})

	newEditor := httptest.NewRequest(http.MethodGet, "https://example.test/admin/content/new/", nil)
	newEditor.AddCookie(cookie)
	newEditorResponse := httptest.NewRecorder()
	handler.ServeHTTP(newEditorResponse, newEditor)
	if newEditorResponse.Code != http.StatusOK || !strings.Contains(newEditorResponse.Body.String(), fmt.Sprintf(`name="category_ids" value="%d"`, category.ID)) || !strings.Contains(newEditorResponse.Body.String(), "标签") {
		t.Fatalf("editor taxonomy controls = %d %s", newEditorResponse.Code, newEditorResponse.Body.String())
	}

	create := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"save"}, "type": {content.TypePost},
		"status": {content.StatusPublish}, "title": {"Unicode 分类归档"}, "slug": {"unicode-taxonomy-archive"},
		"excerpt": {"验证分类和标签公开归档"}, "content_source": {"<p>分类与标签已经进入关系模型。</p>"},
		"cover_url": {""}, "category": {""}, "category_ids": {strconv.FormatInt(category.ID, 10)},
		"tags": {"Go, 数据库，Go"}, "comment_status": {content.CommentOpen}, "published_at": {""},
		"published_timezone": {"Asia/Shanghai"}, "published_source": {"canonical"},
	}
	created := serveContentForm(handler, cookie, "https://example.test/admin/content/new/", "https://example.test", create)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create categorized post = %d %s", created.Code, created.Body.String())
	}
	posts, err := repository.ListAdmin(t.Context(), content.AdminQuery{Search: "Unicode 分类归档", Limit: 10})
	if err != nil || len(posts.Items) != 1 {
		t.Fatalf("created post lookup = %#v, err=%v", posts, err)
	}
	post := posts.Items[0]
	if len(post.Categories) != 1 || post.Categories[0].ID != category.ID || len(post.Tags) != 2 || post.Tags[0].Name != "Go" || post.Tags[1].Name != "数据库" {
		t.Fatalf("post terms = %#v / %#v", post.Categories, post.Tags)
	}

	categoryArchive := httptest.NewRequest(http.MethodGet, "https://example.test"+termArchiveURL(category), nil)
	categoryArchiveResponse := httptest.NewRecorder()
	handler.ServeHTTP(categoryArchiveResponse, categoryArchive)
	categoryArchiveBody := categoryArchiveResponse.Body.String()
	if categoryArchiveResponse.Code != http.StatusOK || !strings.Contains(categoryArchiveBody, "Unicode 分类归档") || !strings.Contains(categoryArchiveBody, "数据系统实践") ||
		!strings.Contains(categoryArchiveBody, `/images/editorial/cover-notes-small.webp`) || strings.Contains(categoryArchiveBody, `src=""`) ||
		!strings.Contains(categoryArchiveBody, `<h2>文章列表</h2>`) || !strings.Contains(categoryArchiveBody, `<h3><a href="/posts/unicode-taxonomy-archive">Unicode 分类归档</a></h3>`) ||
		strings.Contains(categoryArchiveBody, "分类目录是什么？") || strings.Contains(categoryArchiveBody, "NEARBY ROUTES") || strings.Contains(categoryArchiveBody, `class="taxonomy-pagination"`) {
		t.Fatalf("category archive = %d %s", categoryArchiveResponse.Code, categoryArchiveResponse.Body.String())
	}

	tagArchive := httptest.NewRequest(http.MethodGet, "https://example.test/tags/go/", nil)
	tagArchiveResponse := httptest.NewRecorder()
	handler.ServeHTTP(tagArchiveResponse, tagArchive)
	tagArchiveBody := tagArchiveResponse.Body.String()
	if tagArchiveResponse.Code != http.StatusOK || !strings.Contains(tagArchiveBody, "Unicode 分类归档") || !strings.Contains(tagArchiveBody, "其他标签") || strings.Contains(tagArchiveBody, "文章标签是什么？") {
		t.Fatalf("tag archive = %d %s", tagArchiveResponse.Code, tagArchiveResponse.Body.String())
	}

	for _, target := range []string{
		"https://example.test/categories/missing/",
		"https://example.test/categories/%2E%2E/",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing/invalid archive %q = %d", target, response.Code)
		}
	}
	for _, target := range []string{
		"https://example.test/categories/" + url.PathEscape(category.Slug) + "/?unexpected=1",
		"https://example.test/categories/" + url.PathEscape(category.Slug) + "/?page=1&page=2",
		"https://example.test/categories/" + url.PathEscape(category.Slug) + "/?page=0",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid archive query %q = %d", target, response.Code)
		}
	}

	sitemap := httptest.NewRequest(http.MethodGet, "https://example.test/sitemap.xml", nil)
	sitemapResponse := httptest.NewRecorder()
	handler.ServeHTTP(sitemapResponse, sitemap)
	sitemapBody := sitemapResponse.Body.String()
	if sitemapResponse.Code != http.StatusOK || !strings.Contains(sitemapBody, "https://example.test"+termArchiveURL(category)) || !strings.Contains(sitemapBody, "https://example.test/tags/go/") {
		t.Fatalf("taxonomy sitemap = %d %s", sitemapResponse.Code, sitemapBody)
	}

	systemRequest := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/system", nil)
	systemRequest.AddCookie(cookie)
	systemResponse := httptest.NewRecorder()
	handler.ServeHTTP(systemResponse, systemRequest)
	if systemResponse.Code != http.StatusOK {
		t.Fatalf("system API = %d %s", systemResponse.Code, systemResponse.Body.String())
	}
	var systemPayload map[string]any
	if err := json.Unmarshal(systemResponse.Body.Bytes(), &systemPayload); err != nil {
		t.Fatal(err)
	}
	if systemPayload["term_count"] == nil || systemPayload["category_count"] == nil || systemPayload["tag_count"] == nil {
		t.Fatalf("taxonomy metrics missing from system API: %#v", systemPayload)
	}
	invalidSystemRequest := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/system?unexpected=1", nil)
	invalidSystemRequest.AddCookie(cookie)
	invalidSystemResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidSystemResponse, invalidSystemRequest)
	if invalidSystemResponse.Code != http.StatusBadRequest || !strings.Contains(invalidSystemResponse.Body.String(), "request.invalid_query") {
		t.Fatalf("invalid system query = %d %s", invalidSystemResponse.Code, invalidSystemResponse.Body.String())
	}

	dashboard := httptest.NewRequest(http.MethodGet, "https://example.test/admin/", nil)
	dashboard.AddCookie(cookie)
	dashboardResponse := httptest.NewRecorder()
	handler.ServeHTTP(dashboardResponse, dashboard)
	dashboardBody := dashboardResponse.Body.String()
	if dashboardResponse.Code != http.StatusOK || !strings.Contains(dashboardBody, "分类与标签") ||
		!strings.Contains(dashboardBody, "/admin/taxonomy/") {
		t.Fatalf("dashboard taxonomy navigation = %d %s", dashboardResponse.Code, dashboardResponse.Body.String())
	}
	taxonomy := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/taxonomy/", cookie, nil)
	if taxonomy.Code != http.StatusOK ||
		!strings.Contains(taxonomy.Body.String(), fmt.Sprintf("%v 个分类", systemPayload["category_count"])) ||
		!strings.Contains(taxonomy.Body.String(), fmt.Sprintf("%v 个标签", systemPayload["tag_count"])) {
		t.Fatalf("taxonomy page summary = %d %s", taxonomy.Code, taxonomy.Body.String())
	}
	contentList := httptest.NewRequest(http.MethodGet, "https://example.test/admin/content/?q=Unicode", nil)
	contentList.AddCookie(cookie)
	contentListResponse := httptest.NewRecorder()
	handler.ServeHTTP(contentListResponse, contentList)
	contentListBody := contentListResponse.Body.String()
	if contentListResponse.Code != http.StatusOK || !strings.Contains(strings.ToLower(contentListBody), strings.ToLower(termArchiveURL(category))) || !strings.Contains(contentListBody, "#数据库") {
		t.Fatalf("content list taxonomy projection = %d %s", contentListResponse.Code, contentListResponse.Body.String())
	}
}

func createTermThroughAdmin(t *testing.T, handler http.Handler, repository *content.MemoryRepository, cookie *http.Cookie, csrf string, input content.TermInput) content.Term {
	t.Helper()
	response := serveTaxonomyForm(handler, cookie, "https://example.test", taxonomyMutationValues(csrf, "create", input, 0, 0))
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "notice=created") {
		t.Fatalf("create term %q = %d %q %s", input.Name, response.Code, response.Header().Get("Location"), response.Body.String())
	}
	result, err := repository.ListTerms(t.Context(), content.TermQuery{Taxonomy: input.Taxonomy, Search: input.Name, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range result.Items {
		if term.Name == strings.TrimSpace(input.Name) {
			return term
		}
	}
	t.Fatalf("created term %q was not found: %#v", input.Name, result.Items)
	return content.Term{}
}

func taxonomyMutationValues(csrf, action string, input content.TermInput, id, version int64) url.Values {
	values := url.Values{
		"csrf_token": {csrf}, "action": {action}, "taxonomy": {input.Taxonomy},
		"name": {input.Name}, "slug": {input.Slug}, "parent_id": {strconv.FormatInt(input.ParentID, 10)}, "description": {input.Description},
	}
	if id > 0 {
		values.Set("id", strconv.FormatInt(id, 10))
	}
	if version > 0 {
		values.Set("version", strconv.FormatInt(version, 10))
	}
	return values
}

func serveTaxonomyForm(handler http.Handler, cookie *http.Cookie, origin string, values url.Values) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://example.test/admin/taxonomy/", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertJSONError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status || !strings.Contains(response.Body.String(), `"error":"`+code+`"`) {
		t.Fatalf("JSON error = %d %s, want %d %q", response.Code, response.Body.String(), status, code)
	}
}
