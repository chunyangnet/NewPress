package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
)

func TestDashboardQuickDraftCreatesServerOwnedDraftAndPRGQueue(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)

	dashboard := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/", cookie, nil)
	if dashboard.Code != http.StatusOK || dashboard.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(dashboard.Body.String(), "<h1>工作台</h1>") ||
		!strings.Contains(dashboard.Body.String(), `action="/admin/quick-draft/"`) {
		t.Fatalf("dashboard quick draft = %d %s", dashboard.Code, dashboard.Body.String())
	}

	values := quickDraftValues(principal.CSRFToken, "仪表盘快速草稿", "一句交接摘要", `<p>第一段</p><script>alert(1)</script>`)
	created := serveQuickDraftForm(handler, cookie, "https://example.test/admin/quick-draft/", values, "application/x-www-form-urlencoded", "https://example.test")
	if created.Code != http.StatusSeeOther || created.Header().Get("Location") != "/admin/?notice=quick-draft-created" {
		t.Fatalf("quick draft create = %d %q %s", created.Code, created.Header().Get("Location"), created.Body.String())
	}
	result, err := repository.ListAdmin(t.Context(), content.AdminQuery{Status: content.StatusDraft, Limit: 20})
	if err != nil || result.Total != 1 {
		t.Fatalf("draft list = %#v, err=%v", result, err)
	}
	draft := result.Items[0]
	if draft.Status != content.StatusDraft || draft.Type != content.TypePost || draft.Title != "仪表盘快速草稿" ||
		draft.AuthorID != principal.UserID || draft.AuthorName != principal.Label() || draft.Excerpt != "一句交接摘要" ||
		strings.Contains(draft.ContentHTML, "script") || !strings.Contains(draft.ContentSource, "<script>") {
		t.Fatalf("quick draft persisted = %#v", draft)
	}

	prg := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/?notice=quick-draft-created", cookie, nil)
	if prg.Code != http.StatusOK || !strings.Contains(prg.Body.String(), "快速草稿已安全保存") ||
		!strings.Contains(prg.Body.String(), "仪表盘快速草稿") ||
		!strings.Contains(prg.Body.String(), "/admin/content/1/edit/") {
		t.Fatalf("quick draft PRG dashboard = %d %s", prg.Code, prg.Body.String())
	}

	second := serveQuickDraftForm(handler, cookie, "https://example.test/admin/quick-draft/", values, "application/x-www-form-urlencoded; charset=utf-8", "https://example.test")
	if second.Code != http.StatusSeeOther {
		t.Fatalf("duplicate-title quick draft = %d %s", second.Code, second.Body.String())
	}
	result, _ = repository.ListAdmin(t.Context(), content.AdminQuery{Status: content.StatusDraft, Limit: 20})
	if result.Total != 2 || result.Items[0].Slug == result.Items[1].Slug {
		t.Fatalf("quick draft unique slugs = %#v", result.Items)
	}
}

func TestDashboardQuickDraftStrictSecurityAndValidation(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	base := quickDraftValues(principal.CSRFToken, "安全边界草稿", "", "正文")

	tests := []struct {
		name        string
		target      string
		values      url.Values
		contentType string
		origin      string
		status      int
		errorCode   string
	}{
		{name: "cross site", target: "https://example.test/admin/quick-draft/", values: base, contentType: "application/x-www-form-urlencoded", origin: "https://attacker.test", status: http.StatusForbidden, errorCode: "cross_site_request_rejected"},
		{name: "wrong media type", target: "https://example.test/admin/quick-draft/", values: base, contentType: "text/plain", origin: "https://example.test", status: http.StatusUnsupportedMediaType, errorCode: "form_content_type_required"},
		{name: "query smuggling", target: "https://example.test/admin/quick-draft/?status=publish", values: base, contentType: "application/x-www-form-urlencoded", origin: "https://example.test", status: http.StatusBadRequest, errorCode: "invalid_quick_draft_target"},
		{name: "missing field", target: "https://example.test/admin/quick-draft/", values: withoutValue(base, "excerpt"), contentType: "application/x-www-form-urlencoded", origin: "https://example.test", status: http.StatusBadRequest, errorCode: "invalid_quick_draft_form_shape"},
		{name: "unknown author", target: "https://example.test/admin/quick-draft/", values: withValue(base, "author_id", "999"), contentType: "application/x-www-form-urlencoded", origin: "https://example.test", status: http.StatusBadRequest, errorCode: "invalid_quick_draft_form_shape"},
		{name: "duplicate title", target: "https://example.test/admin/quick-draft/", values: withDuplicate(base, "title", "second"), contentType: "application/x-www-form-urlencoded", origin: "https://example.test", status: http.StatusBadRequest, errorCode: "invalid_quick_draft_form_shape"},
		{name: "bad csrf", target: "https://example.test/admin/quick-draft/", values: withValue(base, "csrf_token", "invalid"), contentType: "application/x-www-form-urlencoded", origin: "https://example.test", status: http.StatusForbidden, errorCode: "csrf_validation_failed"},
		{name: "unknown action", target: "https://example.test/admin/quick-draft/", values: withValue(base, "action", "publish"), contentType: "application/x-www-form-urlencoded", origin: "https://example.test", status: http.StatusBadRequest, errorCode: "unknown_quick_draft_action"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveQuickDraftForm(handler, cookie, test.target, test.values, test.contentType, test.origin)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.errorCode) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}

	invalid := quickDraftValues(principal.CSRFToken, "   ", "", "保留这段未保存输入")
	invalidResponse := serveQuickDraftForm(handler, cookie, "https://example.test/admin/quick-draft/", invalid, "application/x-www-form-urlencoded", "https://example.test")
	if invalidResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(invalidResponse.Body.String(), "标题不能为空") ||
		!strings.Contains(invalidResponse.Body.String(), "保留这段未保存输入") {
		t.Fatalf("invalid quick draft = %d %s", invalidResponse.Code, invalidResponse.Body.String())
	}

	tooMuchContent := quickDraftValues(principal.CSRFToken, "超过正文上限", "", strings.Repeat("x", content.MaxContentBytes+1))
	tooMuchResponse := serveQuickDraftForm(handler, cookie, "https://example.test/admin/quick-draft/", tooMuchContent, "application/x-www-form-urlencoded", "https://example.test")
	if tooMuchResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(tooMuchResponse.Body.String(), "正文不能超过 2 MiB") {
		t.Fatalf("decoded quick draft limit = %d %s", tooMuchResponse.Code, tooMuchResponse.Body.String())
	}

	oversizedBody := strings.Repeat("x", quickDraftRequestMaxBytes+1)
	oversizedRequest := httptest.NewRequest(http.MethodPost, "https://example.test/admin/quick-draft/", strings.NewReader(oversizedBody))
	oversizedRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	oversizedRequest.Header.Set("Origin", "https://example.test")
	oversizedRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	oversizedRequest.AddCookie(cookie)
	oversizedResponse := httptest.NewRecorder()
	handler.ServeHTTP(oversizedResponse, oversizedRequest)
	if oversizedResponse.Code != http.StatusRequestEntityTooLarge || !strings.Contains(oversizedResponse.Body.String(), "quick_draft_form_too_large") {
		t.Fatalf("encoded quick draft limit = %d %s", oversizedResponse.Code, oversizedResponse.Body.String())
	}

	result, err := repository.ListAdmin(t.Context(), content.AdminQuery{Status: content.StatusDraft, Limit: 20})
	if err != nil || result.Total != 0 {
		t.Fatalf("rejected quick drafts mutated repository = %#v, err=%v", result, err)
	}
}

func TestDashboardQuickDraftUsesPersistentAuthorScope(t *testing.T) {
	_, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
	author, authorCookie, authorPrincipal := createAndLoginTestUser(t, manager, owner, "flight-author", "flight-author@example.test", "飞行作者", "author")
	_ = createOwnedTestPost(t, repository, owner.UserID, owner.Label(), "owner-private-draft", "所有者私有草稿")

	created := serveQuickDraftForm(handler, authorCookie, "https://example.test/admin/quick-draft/",
		quickDraftValues(authorPrincipal.CSRFToken, "作者快速草稿", "本人队列", "作者正文"),
		"application/x-www-form-urlencoded", "https://example.test")
	if created.Code != http.StatusSeeOther {
		t.Fatalf("author quick draft = %d %s", created.Code, created.Body.String())
	}
	result, err := repository.ListAdmin(t.Context(), content.AdminQuery{AuthorID: author.ID, Status: content.StatusDraft, Limit: 20})
	if err != nil || result.Total != 1 || result.Items[0].AuthorID != author.ID || result.Items[0].AuthorName != "飞行作者" {
		t.Fatalf("author-owned quick draft = %#v, err=%v", result, err)
	}
	dashboard := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/", authorCookie, nil)
	body := dashboard.Body.String()
	if dashboard.Code != http.StatusOK || !strings.Contains(body, "作者快速草稿") || strings.Contains(body, "所有者私有草稿") {
		t.Fatalf("author dashboard scope = %d %s", dashboard.Code, body)
	}
}

func quickDraftValues(csrf, title, excerpt, source string) url.Values {
	return url.Values{
		"csrf_token": {csrf}, "action": {"quick_draft"}, "title": {title},
		"excerpt": {excerpt}, "content_source": {source},
	}
}

func serveQuickDraftForm(handler http.Handler, cookie *http.Cookie, target string, values url.Values, contentType, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func withValue(values url.Values, key, value string) url.Values {
	result := cloneValues(values)
	result.Set(key, value)
	return result
}

func withDuplicate(values url.Values, key, value string) url.Values {
	result := cloneValues(values)
	result[key] = append(result[key], value)
	return result
}

func withoutValue(values url.Values, key string) url.Values {
	result := cloneValues(values)
	delete(result, key)
	return result
}
