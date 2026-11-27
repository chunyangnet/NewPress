package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/content"
)

type aiRoundTripper func(*http.Request) (*http.Response, error)

func (f aiRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func setAIResponse(s *Server, status int, kind, body string, inspect func(*http.Request)) {
	s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
		if inspect != nil {
			inspect(r)
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {kind}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
}

func aiGenerationJSON() string {
	return `{"base_url":"https://writer.example.test/v1","api_key":"synthetic-test-key","model":"writer-model","prompt_id":"tutorial","brief":"写一篇完整教程","tone":"natural","length":"medium","language":"zh-CN","draft":{"title":"","content":""}}`
}

func aiFrame(text, reason string) string {
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": text}, "finish_reason": reason}}})
	return fmt.Sprintf("data: %s\n\n", data)
}

func aiDone(t *testing.T, body string) map[string]any {
	t.Helper()
	_, tail, ok := strings.Cut(body, "event: done\ndata: ")
	if !ok {
		t.Fatal("missing terminal document", body)
	}
	line, _, _ := strings.Cut(tail, "\n")
	var data map[string]any
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAIEditorAuthorizationCSRFAndEditorResources(t *testing.T) {
	s, handler, manager, _, cookie, principal := newPersistentAdminTestServer(t)
	_, reader, readerPrincipal := createAndLoginTestUser(t, manager, principal, "ai-reader", "ai-reader@example.test", "读者", "subscriber")
	_, author, authorPrincipal := createAndLoginTestUser(t, manager, principal, "ai-author", "ai-author@example.test", "作者", "author")
	calls := 0
	setAIResponse(s, 200, "application/json", `{"data":[{"id":"writer-model"}]}`, func(*http.Request) { calls++ })
	body := `{"base_url":"https://writer.example.test","api_key":"synthetic-test-key"}`
	for _, item := range []struct {
		name, token, origin string
		cookie              *http.Cookie
		status              int
	}{
		{"anonymous", "", "https://example.test", nil, 401},
		{"reader", readerPrincipal.CSRFToken, "https://example.test", reader, 403},
		{"missing csrf", "", "https://example.test", cookie, 403},
		{"cross site", principal.CSRFToken, "https://elsewhere.test", cookie, 403},
	} {
		t.Run(item.name, func(t *testing.T) {
			response := serveContentAPI(handler, item.cookie, "POST", "https://example.test/api/v1/admin/ai/models", body, map[string]string{"Content-Type": "application/json", "Origin": item.origin, "X-CSRF-Token": item.token})
			if response.Code != item.status || calls != 0 {
				t.Fatalf("unauthorized upstream request: %d, calls %d", response.Code, calls)
			}
		})
	}
	response := serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/models", body, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": authorPrincipal.CSRFToken})
	if response.Code != 200 || calls != 1 || strings.Contains(response.Body.String(), "synthetic-test-key") || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("author model discovery failed", response.Code, response.Body.String())
	}
	editor := serveCookieRequest(handler, "GET", "https://example.test/admin/content/new/", author, nil)
	for _, control := range []string{"data-ai-open", "data-ai-editor", "data-ai-key", "data-ai-stop", "data-ai-undo-apply", "data-ai-thinking", "data-ai-thinking-toggle", "data-ai-thinking-output", `data-ai-starter="humanize"`, "chunnel-ai-editor.js", "chunnel-ai-editor.css"} {
		if editor.Code != 200 || !strings.Contains(editor.Body.String(), control) {
			t.Fatalf("editor missing %s", control)
		}
	}
	for _, line := range strings.Split(editor.Body.String(), "\n") {
		if strings.Contains(line, `<script src=`) && !strings.Contains(line, "data-ai-assets") && (strings.Contains(line, "chunnel-ai-editor.js") || strings.Contains(line, "chunnel-ai-images.js")) || strings.Contains(line, `<link rel="stylesheet"`) && (strings.Contains(line, "chunnel-ai-editor.css") || strings.Contains(line, "chunnel-ai-images.css")) {
			t.Fatal("AI dialogs must load on demand", line)
		}
	}
	if !strings.Contains(editor.Body.String(), "chunnel-ai-loader.js") {
		t.Fatal("AI launcher loader is missing")
	}
	home := serveCookieRequest(handler, "GET", "https://example.test/", nil, nil)
	if strings.Contains(home.Body.String(), "chunnel-ai-editor") {
		t.Fatal("AI assets must not be loaded on the public home page")
	}
	prompts := serveContentAPI(handler, author, "GET", "https://example.test/api/v1/admin/ai/prompts", "", nil)
	if prompts.Code != 200 || !strings.Contains(prompts.Body.String(), "技术写作") || !strings.Contains(prompts.Body.String(), "去除 AI 感") || !strings.Contains(prompts.Body.String(), `"id":"humanize"`) {
		t.Fatal("author prompt catalog unavailable")
	}
}

func TestAIEditorThinkingFlushesBeforeArticleAndStaysOutOfDocument(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	release := make(chan struct{})
	s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"只属于思考区的内容\"}}]}\n\n")
			select {
			case <-release:
				_, _ = io.WriteString(writer, aiFrame("# 标题\n\n文章正文。", "stop")+"data: [DONE]\n\n")
			case <-r.Context().Done():
			}
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
	})}}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/admin/ai/generate", strings.NewReader(aiGenerationJSON()))
	request.Host = "example.test"
	request.Header.Set("Origin", "https://example.test")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", principal.CSRFToken)
	request.AddCookie(cookie)
	client := server.Client()
	client.Timeout = 10 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	var received strings.Builder
	for !strings.Contains(received.String(), "只属于思考区的内容") {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal("thinking was buffered until article completion", err)
		}
		received.WriteString(line)
	}
	if strings.Contains(received.String(), "event: delta") {
		t.Fatal("article arrived before release")
	}
	close(release)
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	received.Write(rest)
	body := received.String()
	thoughtEnd, article := strings.Index(body, "event: thinking_done"), strings.Index(body, "event: delta")
	if thoughtEnd < 0 || article < thoughtEnd {
		t.Fatal("incorrect phase order", body)
	}
	doc, _ := json.Marshal(aiDone(t, body)["document"])
	if strings.Contains(string(doc), "只属于思考区") || !strings.Contains(string(doc), "文章正文") {
		t.Fatal("thinking contaminated document", string(doc))
	}

	// A later transport failure must retain both channels without manufacturing
	// a successful terminal event or embedding thought text in the partial body.
	setAIResponse(s, 200, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"已收到的思考\"}}]}\n\n"+aiFrame("部分正文", ""), nil)
	failed := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/generate", aiGenerationJSON(), map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken})
	if !strings.Contains(failed.Body.String(), "已收到的思考") || !strings.Contains(failed.Body.String(), "部分正文") || !strings.Contains(failed.Body.String(), "event: error") || strings.Contains(failed.Body.String(), "event: done") {
		t.Fatal("partial stream was lost or marked complete", failed.Body.String())
	}
}

func TestAIEditorGenerationAndPrepareNeverSaveContent(t *testing.T) {
	s, handler, _, repository, cookie, principal := newPersistentAdminTestServer(t)
	post, err := repository.Create(t.Context(), content.PostInput{Title: "原有文章", Slug: "original-ai-draft", Status: content.StatusDraft, ContentSource: "<p>原有正文</p>", AuthorID: principal.UserID})
	if err != nil {
		t.Fatal(err)
	}
	meta := `<newpress-meta>{"title":"完整教程","slug":"complete-tutorial","excerpt":"教程摘要","tags":["Go","教程"],"category_ids":[99999],"cover_prompt":"桌上的书与代码"}</newpress-meta>`
	setAIResponse(s, 200, "text/event-stream", aiFrame(meta+"\n\n", "")+aiFrame("## 开始\n\n完整的 **正文**。", "stop")+"data: [DONE]\n\n", nil)
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken}
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/generate", aiGenerationJSON(), headers)
	if response.Code != 200 || !response.Flushed || !strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") || response.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatal("generation is not streamed", response.Code, response.Header())
	}
	done := aiDone(t, response.Body.String())
	doc := done["document"].(map[string]any)
	if done["complete"] != true || doc["title"] != "完整教程" || !strings.Contains(doc["content_source"].(string), "<strong>正文</strong>") || len(doc["category_ids"].([]any)) != 0 {
		t.Fatal("invalid complete document", done)
	}
	if strings.Contains(response.Body.String(), "synthetic-test-key") {
		t.Fatal("credential leaked into stream")
	}
	prepare := `{"title":"手工调整的标题","slug":"edited-title","excerpt":"摘要","tags":["Go"],"category_ids":[99999],"markdown":"## 最后修改\n\n这是一段修改后的正文。\n\n![x](https://tracker.example/a)","cover_prompt":""}`
	prepared := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/prepare", prepare, headers)
	if prepared.Code != 200 || !strings.Contains(prepared.Body.String(), "最后修改") || strings.Contains(prepared.Body.String(), `\u003cimg`) {
		t.Fatal("prepare failed or introduced an external image", prepared.Body.String())
	}
	stored, err := repository.GetByID(t.Context(), post.ID)
	if err != nil || stored.Title != post.Title || stored.ContentSource != post.ContentSource || stored.Version != post.Version {
		t.Fatal("AI generation changed persisted content")
	}
}

func TestAIEditorUsageEventsSurviveSuccessAndFailure(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken}
	usageFrame := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14}}\n\n"
	for _, stopped := range []bool{false, true} {
		body := aiFrame("文章正文。", "") + usageFrame
		if !stopped {
			body += aiFrame("", "stop") + "data: [DONE]\n\n"
		}
		setAIResponse(s, 200, "text/event-stream", body, nil)
		response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/generate", aiGenerationJSON(), headers)
		text := response.Body.String()
		terminal := "done"
		if stopped {
			terminal = "error"
		}
		usageAt, endAt := strings.Index(text, "event: usage"), strings.Index(text, "event: "+terminal)
		if usageAt < 0 || endAt < usageAt || !strings.Contains(text[endAt:], `"total_tokens":14`) {
			t.Fatal("usage was lost at stream termination", text)
		}
		if !stopped {
			doc, _ := json.Marshal(aiDone(t, text)["document"])
			if strings.Contains(string(doc), "tokens") || strings.Contains(string(doc), "usage") {
				t.Fatal("usage contaminated the document", string(doc))
			}
		}
	}
	setAIResponse(s, 200, "text/event-stream", aiFrame("正文。", "stop")+"data: [DONE]\n\n", func(r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["stream_options"] != nil {
			t.Fatal("opt-out was not respected", payload)
		}
	})
	var input map[string]any
	_ = json.Unmarshal([]byte(aiGenerationJSON()), &input)
	input["stream_usage"] = false
	request, _ := json.Marshal(input)
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/generate", string(request), headers)
	done := aiDone(t, response.Body.String())
	if len(done["usage"].(map[string]any)) != 0 {
		t.Fatal("missing usage became zero", done)
	}
}

func TestAIEditorStreamDistinguishesIncompleteResults(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken}
	setAIResponse(s, 200, "text/event-stream", aiFrame("# 标题\n\n已收到的正文。", "length")+"data: [DONE]\n\n", nil)
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/generate", aiGenerationJSON(), headers)
	if aiDone(t, response.Body.String())["complete"] != false {
		t.Fatal("token-limited response claimed completion")
	}
	setAIResponse(s, 200, "text/event-stream", aiFrame("已收到的正文。", ""), nil)
	response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/generate", aiGenerationJSON(), headers)
	if strings.Contains(response.Body.String(), "event: done") || !strings.Contains(response.Body.String(), "event: error") || !strings.Contains(response.Body.String(), "已收到的正文") {
		t.Fatal("truncated response lost text or claimed completion", response.Body.String())
	}
}

func TestAIEditorDisconnectCancelsUpstreamAndRejectsParallelGeneration(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	entered, canceled := make(chan struct{}), make(chan struct{})
	s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
		return nil, r.Context().Err()
	})}}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/admin/ai/generate", strings.NewReader(aiGenerationJSON()))
	request.Host = "example.test"
	request.Header.Set("Origin", "https://example.test")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", principal.CSRFToken)
	request.AddCookie(cookie)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "event: ready\n" {
		t.Fatal("stream headers were buffered", line, err, response.StatusCode)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream not started")
	}
	second := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/generate", aiGenerationJSON(), map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken})
	if second.Code != 429 {
		t.Fatal("parallel generation was not rejected", second.Code)
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("browser disconnect did not cancel provider request")
	}
}

func TestAIEditorRejectsPrivateEndpointsAndMalformedBodiesBeforeCallingProvider(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	setAIResponse(s, 200, "application/json", `{"data":[]}`, func(*http.Request) { t.Error("invalid request reached upstream") })
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken}
	for _, body := range []string{`{"base_url":"https://127.0.0.1","api_key":"test"}`, `{"base_url":"https://169.254.169.254","api_key":"test"}`, `{"base_url":"https://writer.example.test?api_key=secret","api_key":"test"}`, `{"base_url":"https://writer.example.test","api_key":"test\r\nHeader: value"}`, `{"api_key":"first","api_key":"second"}`} {
		response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/models", body, headers)
		if response.Code != 400 {
			t.Fatalf("invalid request returned %d", response.Code)
		}
	}
}

func TestAIEditorGenericNewsStopsBeforeProviderAndAcceptsSource(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	calls := 0
	setAIResponse(s, 200, "text/event-stream", aiFrame("# 公告\n\n图书馆恢复周日开放。", "stop")+"data: [DONE]\n\n", func(*http.Request) { calls++ })
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken}
	var input map[string]any
	if err := json.Unmarshal([]byte(aiGenerationJSON()), &input); err != nil {
		t.Fatal(err)
	}
	input["prompt_id"], input["brief"] = "article", "今日新闻"
	data, _ := json.Marshal(input)
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/generate", string(data), headers)
	if response.Code != 400 || calls != 0 || !strings.Contains(response.Body.String(), "ai.invalid_brief") || !strings.Contains(response.Body.String(), "补充具体事件") || strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("generic news should return actionable JSON without a model request: %d %s calls=%d", response.Code, response.Body.String(), calls)
	}
	input["prompt_id"] = "news"
	input["draft"] = map[string]string{"content": "图书馆公告：11 月 15 日起恢复周日开放。"}
	data, _ = json.Marshal(input)
	response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/generate", string(data), headers)
	if response.Code != 200 || calls != 1 || aiDone(t, response.Body.String())["complete"] != true {
		t.Fatalf("source-backed news should stream normally: %d %s calls=%d", response.Code, response.Body.String(), calls)
	}
}
