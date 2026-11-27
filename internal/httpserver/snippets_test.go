package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestSnippetAPIAuthorizationVisibilityAndAtomicPreview(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	_, authorCookie, author := createAndLoginTestUser(t, manager, owner, "snippet-author", "snippet-author@example.test", "作者", "author")
	_, readerCookie, _ := createAndLoginTestUser(t, manager, owner, "snippet-reader", "snippet-reader@example.test", "读者", "subscriber")
	base := "https://example.test/api/v1/admin/snippets"
	headers := authenticatedAPIHeaders(owner)
	body := `{"title":"私密片段","content_source":"<p>私密正文</p>","visibility":"private"}`
	for _, tc := range []struct {
		cookie *http.Cookie
		want   int
	}{{nil, 401}, {readerCookie, 403}} {
		w := serveContentAPI(handler, tc.cookie, http.MethodGet, base, "", nil)
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := serveContentAPI(handler, authorCookie, http.MethodPost, base, body, authenticatedAPIHeaders(author)); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := serveContentAPI(handler, cookie, http.MethodPost, base, body, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"}); w.Code != 403 {
		t.Fatal("CSRF", w.Code)
	}
	w := serveContentAPI(handler, cookie, http.MethodPost, base, body, headers)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var snippet content.Snippet
	decodeAPIResponse(t, w, &snippet)
	url := fmt.Sprintf("%s/%d", base, snippet.ID)
	w = serveContentAPI(handler, authorCookie, http.MethodGet, url, "", nil)
	if w.Code != 404 || strings.Contains(w.Body.String(), "私密正文") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = serveContentAPI(handler, authorCookie, http.MethodGet, base, "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "私密片段") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := serveContentAPI(handler, cookie, http.MethodPut, url, body, headers); w.Code != 428 {
		t.Fatal("missing version", w.Code)
	}
	headers["If-Match"] = snippetETag(snippet)
	w = serveContentAPI(handler, cookie, http.MethodPut, url, `{"title":"共享说明","content_source":"<p>第一版正文</p>","visibility":"shared"}`, headers)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	decodeAPIResponse(t, w, &snippet)
	first := contenthtml.SnippetKey{ID: snippet.ID, Version: snippet.CurrentVersion}
	p, err := s.repository.Create(t.Context(), content.PostInput{Title: "作者文章", Slug: "snippet-author", AuthorID: author.UserID, ContentSource: contenthtml.SnippetShell(first)})
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.repository.Create(t.Context(), content.PostInput{Title: "其他作者私密文章", Slug: "snippet-other", AuthorID: owner.UserID, ContentSource: contenthtml.SnippetShell(first)})
	if err != nil {
		t.Fatal(err)
	}
	headers["If-Match"] = snippetETag(snippet)
	w = serveContentAPI(handler, cookie, http.MethodPut, url, `{"title":"共享说明","content_source":"<p>第二版正文</p>","visibility":"shared"}`, headers)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	decodeAPIResponse(t, w, &snippet)
	w = serveContentAPI(handler, authorCookie, http.MethodGet, url+"/impacts", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), q.Title) || !strings.Contains(w.Body.String(), p.Title) {
		t.Fatal(w.Code, w.Body.String())
	}
	u := content.SnippetUpdate{SnippetID: snippet.ID, SnippetVersion: snippet.Version, Items: []content.SnippetSelection{{ID: p.ID, Version: p.Version}}}
	encoded, _ := json.Marshal(u)
	w = serveContentAPI(handler, authorCookie, http.MethodPost, url+"/preview", string(encoded), authenticatedAPIHeaders(author))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var plan content.SnippetPlan
	decodeAPIResponse(t, w, &plan)
	u.Digest = plan.Digest
	u.RequestKey = strings.Repeat("c", 64)
	encoded, _ = json.Marshal(u)
	w = serveContentAPI(handler, authorCookie, http.MethodPost, url+"/apply", string(encoded), authenticatedAPIHeaders(author))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result content.SnippetResult
	decodeAPIResponse(t, w, &result)
	if result.Replayed || len(result.Receipt.Items) != 1 {
		t.Fatal(result)
	}
	w = serveContentAPI(handler, authorCookie, http.MethodPost, url+"/apply", string(encoded), authenticatedAPIHeaders(author))
	decodeAPIResponse(t, w, &result)
	if w.Code != 200 || !result.Replayed {
		t.Fatal(w.Code, result)
	}
	request := httptest.NewRequest("GET", "https://example.test/admin/snippets/?edit="+fmt.Sprint(snippet.ID), nil)
	request.AddCookie(cookie)
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, request)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "引用影响清单") || !strings.Contains(page.Body.String(), "newpress-snippets.js") {
		t.Fatal("page", page.Code, page.Body.String())
	}
	snapshot := s.siteSettings.Snapshot()
	settings := snapshot.Settings
	settings.API.AdminContentEnabled = false
	if _, fields, saveErr := s.siteSettings.Save(snapshot.Revision, settings); saveErr != nil || len(fields) != 0 {
		t.Fatal(saveErr, fields)
	}
	w = serveContentAPI(handler, cookie, http.MethodGet, base, "", nil)
	if w.Code != 404 {
		t.Fatal("API gate", w.Code)
	}
}

func TestSnippetPublicResolutionWithdrawalAndCache(t *testing.T) {
	s, handler, repository, _, principal := newAuthenticatedContentServer(t)
	a := snippetAccess(principal)
	item, err := repository.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "source", ContentSource: "<p>独有片段正文</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := repository.Create(t.Context(), content.PostInput{Title: "公开文章", Slug: "snippet-public", Status: content.StatusPublish, Excerpt: "固定摘要", ContentSource: `<p>固定正文</p><aside data-np-snippet="` + fmt.Sprint(item.ID) + `" data-np-snippet-version="1">FORGED_FALLBACK</aside>`})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := s.siteSettings.Snapshot()
	settings := snapshot.Settings
	settings.API.PublicIncludeBody = true
	if _, fields, saveErr := s.siteSettings.Save(snapshot.Revision, settings); saveErr != nil || len(fields) != 0 {
		t.Fatal(saveErr, fields)
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "https://example.test"+path, nil))
		return w
	}
	for _, path := range []string{"/posts/snippet-public", "/api/v1/posts"} {
		w := get(path)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "独有片段正文") || strings.Contains(w.Body.String(), "FORGED_FALLBACK") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(path, w.Code, w.Header(), w.Body.String())
		}
	}
	w := get("/api/v1/posts?q=独有片段正文")
	var search struct {
		Total int `json:"total"`
	}
	decodeAPIResponse(t, w, &search)
	if search.Total != 0 {
		t.Fatal("dynamic snippet text indexed", search)
	}
	item, err = repository.WithdrawSnippet(t.Context(), item.ID, content.Mutation{ExpectedVersion: item.Version}, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/posts/snippet-public", "/api/v1/posts"} {
		w = get(path)
		if w.Code != 200 || strings.Contains(w.Body.String(), "独有片段正文") || strings.Contains(w.Body.String(), "FORGED_FALLBACK") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	if _, err = repository.SaveSnippet(t.Context(), item.ID, content.Mutation{ExpectedVersion: item.Version}, content.SnippetInput{Title: "new", ContentSource: "<p>重新共享正文</p>", Visibility: content.SnippetShared}, a); err != nil {
		t.Fatal(err)
	}
	w = get("/posts/snippet-public")
	if strings.Contains(w.Body.String(), "重新共享正文") {
		t.Fatal("old reference revived")
	}
	current, _ := repository.GetByID(t.Context(), p.ID)
	if current.Version != p.Version || current.ContentSource != p.ContentSource {
		t.Fatal("withdraw mutated article")
	}
}
