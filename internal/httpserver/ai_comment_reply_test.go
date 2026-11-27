package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
)

func TestAICommentReplyDraftReturnsEditableSuggestionWithoutWritingComment(t *testing.T) {
	s, handler, comments, cookie, principal := newCommentHTTPTestServer(t)
	post, err := s.repository.Create(t.Context(), content.PostInput{
		Type: content.TypePost, Status: content.StatusPublish, Slug: "ai-reply-context",
		Title: "站点评论规则", Excerpt: "请在评论中说明具体问题。",
		ContentSource: "我们会核对文章中的技术信息后再回复。", AuthorName: "NewPress", Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := comments.Create(t.Context(), comment.CreateInput{
		PostID: post.ID, AuthorName: "访客", AuthorEmail: "guest@example.test", Status: comment.StatusApproved,
		ContentSource: "文中提到的设置项在哪里？",
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := aieditor.NewSettingsStore("")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Save(aieditor.Settings{Revision: store.Snapshot().Revision, DefaultProvider: "writer", Providers: []aieditor.Provider{{
		ID: "writer", Name: "测试写作接口", BaseURL: "https://writer.example.test/v1", APIKey: "private-test-key",
		Models: []string{"reply-model"}, DefaultModel: "reply-model",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	s.SetAISettings(store)
	calls := 0
	setAIResponse(s, http.StatusOK, "text/event-stream", aiFrame("感谢提问。请在文章对应的设置说明中查看该选项；如仍找不到，欢迎补充你使用的版本。", "stop")+"data: [DONE]\n\n", func(request *http.Request) {
		calls++
		if request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer private-test-key" {
			t.Fatalf("AI request did not use the saved default provider: %s %s", request.URL, request.Header.Get("Authorization"))
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "reply-model" || len(payload.Messages) != 2 || !strings.Contains(payload.Messages[1].Content, "文中提到的设置项在哪里？") ||
			!strings.Contains(payload.Messages[1].Content, "站点评论规则") || !strings.Contains(payload.Messages[1].Content, "我们会核对文章中的技术信息") {
			t.Fatalf("missing reply context in model request: %+v", payload)
		}
	})
	itemID := strconv.FormatInt(item.ID, 10)
	url := "https://example.test/api/v1/admin/comments/" + itemID + "/reply-draft"
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken}
	for _, test := range []struct {
		name    string
		cookie  *http.Cookie
		headers map[string]string
		want    int
	}{
		{name: "anonymous", headers: headers, want: http.StatusUnauthorized},
		{name: "missing csrf", cookie: cookie, headers: map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"}, want: http.StatusForbidden},
		{name: "cross site", cookie: cookie, headers: map[string]string{"Content-Type": "application/json", "Origin": "https://attacker.test", "X-CSRF-Token": principal.CSRFToken}, want: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := serveContentAPI(handler, test.cookie, http.MethodPost, url, "{}", test.headers)
			if response.Code != test.want || calls != 0 {
				t.Fatalf("unauthorized draft request status=%d upstream calls=%d body=%s", response.Code, calls, response.Body.String())
			}
		})
	}
	checkDisabled := func() {
		t.Helper()
		r := serveContentAPI(handler, cookie, http.MethodPost, url, "{}", headers)
		page := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/admin/comments/"+itemID+"/edit/", "", "", "198.51.100.40:4300")
		if r.Code != http.StatusNotFound || calls != 0 || page.Code != 200 || strings.Contains(page.Body.String(), "data-ai-reply-draft") {
			t.Fatalf("inactive draft: status=%d calls=%d page=%d", r.Code, calls, page.Code)
		}
	}
	checkDisabled()
	if err := s.plugins.InstallBundled(plugin.AIProMax); err != nil {
		t.Fatal(err)
	}
	checkDisabled()
	if err := s.plugins.SetEnabled(plugin.AIProMax, true); err != nil {
		t.Fatal(err)
	}
	setModule := func(value bool) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"revision": s.siteSettings.Snapshot().Revision, "modules": map[string]bool{"ai": value}})
		r := serveContentAPI(handler, cookie, "PATCH", "https://example.test/api/v1/admin/modules", string(body), headers)
		if r.Code != 200 {
			t.Fatal(r.Body.String())
		}
	}
	setModule(false)
	checkDisabled()
	setModule(true)
	response := serveContentAPI(handler, cookie, http.MethodPost, url, "{}", headers)
	if response.Code != http.StatusOK || calls != 1 || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("draft request status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
	var result aiCommentReplyDraftResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Model != "reply-model" || !strings.HasPrefix(result.Draft, "感谢提问") || strings.Contains(response.Body.String(), "private-test-key") {
		t.Fatalf("invalid draft response: %+v err=%v body=%s", result, err, response.Body.String())
	}
	updated, err := comments.Get(t.Context(), item.ID)
	if err != nil || updated.Version != item.Version {
		t.Fatalf("draft generation modified source comment: %+v err=%v", updated, err)
	}
	list, err := comments.ListAdmin(t.Context(), comment.Query{PostID: post.ID, Limit: 20})
	if err != nil || list.Total != 1 {
		t.Fatalf("draft generation created a reply: total=%d err=%v", list.Total, err)
	}
	page := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/admin/comments/"+itemID+"/edit/", "", "", "198.51.100.40:4300")
	for _, marker := range []string{"data-ai-reply-draft", "/api/v1/admin/comments/" + itemID + "/reply-draft", "检查并编辑后再发送"} {
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), marker) {
			t.Fatalf("comment editor missing AI draft affordance %q: status=%d", marker, page.Code)
		}
	}
	for _, off := range []string{"plugin", "module"} {
		t.Run("interrupt "+off, func(t *testing.T) {
			started := make(chan struct{})
			s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(req *http.Request) (*http.Response, error) {
				close(started)
				select {
				case <-req.Context().Done():
					return nil, req.Context().Err()
				case <-time.After(5 * time.Second):
					return nil, errors.New("fixture timeout")
				}
			})}}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- serveContentAPI(handler, cookie, "POST", url, "{}", headers) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("generation did not start")
			}
			if off == "plugin" {
				if err := s.plugins.SetEnabled(plugin.AIProMax, false); err != nil {
					t.Fatal(err)
				}
				if err := s.plugins.SetEnabled(plugin.AIProMax, true); err != nil {
					t.Fatal(err)
				}
			} else {
				setModule(false)
				setModule(true)
			}
			select {
			case r := <-done:
				if r.Code != 409 || strings.Contains(r.Body.String(), `"draft":`) {
					t.Fatal(r.Code, r.Body.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("disabled model request still waiting")
			}
		})
	}
}

func TestAICommentReplyPrivateArticleRequiresObjectPermission(t *testing.T) {
	s, h, comments, _, _ := newCommentHTTPTestServer(t)
	enableWritingFixture(t, s)
	store, err := auth.NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	pw := "Comment ownership fixture 2026"
	hash, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	s.auth, err = auth.New(auth.Config{Users: store, Username: "owner", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	_, owner, err := s.auth.Authenticate("test", "owner", pw)
	if err != nil {
		t.Fatal(err)
	}
	role := auth.Role{Slug: "reviewer", Name: "评论审核", Capabilities: []string{auth.CapabilityAdminAccess, auth.CapabilityCommentsModerate, auth.CapabilityContentWriteOwn}}
	if err = store.EnsureRoles(t.Context(), []auth.Role{role}); err != nil {
		t.Fatal(err)
	}
	u, c, p := createAndLoginTestUser(t, s.auth, owner, "test-reviewer", "reviewer@example.test", "审核", role.Slug)
	calls := 0
	setAIResponse(s, 200, "text/event-stream", aiFrame("已收到，谢谢。", "stop")+"data: [DONE]\n\n", func(*http.Request) { calls++ })
	for _, id := range []int64{owner.UserID, u.ID} {
		post, err := s.repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, AuthorID: id, AuthorName: "作者", Actor: "test", Title: "尚未公开", Slug: "private-" + strconv.FormatInt(id, 10), ContentSource: "私有材料"})
		if err != nil {
			t.Fatal(err)
		}
		item, err := comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, AuthorName: "读者", AuthorEmail: "reader@example.test", ContentSource: "待回复", Status: comment.StatusPending})
		if err != nil {
			t.Fatal(err)
		}
		tail := strconv.FormatInt(item.ID, 10)
		page := serveCookieRequest(h, "GET", "https://example.test/admin/comments/"+tail+"/edit/", c, nil)
		want := id == u.ID
		if page.Code != 200 || strings.Contains(page.Body.String(), "data-ai-reply-draft") != want {
			t.Fatal("button did not match object access", id, page.Code)
		}
		r := serveContentAPI(h, c, "POST", "https://example.test/api/v1/admin/comments/"+tail+"/reply-draft", "{}", map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": p.CSRFToken})
		if want && (r.Code != 200 || calls != 1) || !want && (r.Code != 403 || calls != 0) {
			t.Fatal(id, r.Code, calls, r.Body.String())
		}
	}
}

func TestAICommentReplyDraftRejectsDisallowedStatusBeforeCallingProvider(t *testing.T) {
	s, handler, comments, cookie, principal := newCommentHTTPTestServer(t)
	if err := s.plugins.InstallBundled(plugin.AIProMax); err != nil {
		t.Fatal(err)
	}
	if err := s.plugins.SetEnabled(plugin.AIProMax, true); err != nil {
		t.Fatal(err)
	}
	item, err := comments.Create(t.Context(), comment.CreateInput{
		PostID: 1, AuthorName: "垃圾评论", AuthorEmail: "spam@example.test", Status: comment.StatusSpam, ContentSource: "请回复我",
	})
	if err != nil {
		t.Fatal(err)
	}
	store, _ := aieditor.NewSettingsStore("")
	_, err = store.Save(aieditor.Settings{Revision: store.Snapshot().Revision, DefaultProvider: "writer", Providers: []aieditor.Provider{{
		ID: "writer", Name: "测试写作接口", BaseURL: "https://writer.example.test/v1", APIKey: "test-key", Models: []string{"reply-model"}, DefaultModel: "reply-model",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	s.SetAISettings(store)
	calls := 0
	setAIResponse(s, http.StatusOK, "text/event-stream", aiFrame("不应被调用", "stop")+"data: [DONE]\n\n", func(*http.Request) { calls++ })
	response := serveContentAPI(handler, cookie, http.MethodPost, "https://example.test/api/v1/admin/comments/"+strconv.FormatInt(item.ID, 10)+"/reply-draft", "{}", map[string]string{
		"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken,
	})
	if response.Code != http.StatusConflict || calls != 0 || !strings.Contains(response.Body.String(), "reply_not_allowed") {
		t.Fatalf("spam draft request status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
}

func TestAICommentReplyContextBoundsLongArticleContent(t *testing.T) {
	post := content.Post{Title: "长文", ContentSource: strings.Repeat("字", aiCommentReplyContextRunes+100)}
	messages := aiCommentReplyMessages(comment.Comment{AuthorName: "访客", ContentSource: "问题"}, post)
	if len(messages) != 2 || !strings.Contains(messages[1].Content, "…[已截断]") || strings.Count(messages[1].Content, "字") != aiCommentReplyContextRunes {
		t.Fatalf("article context was not bounded: len=%d", strings.Count(messages[1].Content, "字"))
	}
}
