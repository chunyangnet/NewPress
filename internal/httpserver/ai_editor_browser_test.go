package httpserver

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
)

// Opt-in browser fixture. Uses only synthetic keys and responses in a temporary store.
func TestAISettingsBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_AI_SETTINGS_BROWSER") != "1" {
		t.Skip("opt-in browser fixture")
	}
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	s.config.PublicURL = "http://127.0.0.1:19426"
	store, err := aieditor.NewSettingsStore(filepath.Join(t.TempDir(), "ai-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetAISettings(store)
	if os.Getenv("NEWPRESS_AI_PRO_MAX_BROWSER") == "1" {
		enableWritingFixture(t, s)
	}
	if os.Getenv("NEWPRESS_AI_THINKING_BROWSER") == "1" {
		settings := store.Snapshot()
		settings.DefaultProvider = "writer"
		settings.Providers = []aieditor.Provider{{ID: "writer", Name: "思考流式验证", BaseURL: "https://fixture.example.test/v1", APIKey: "writer-test-key", Models: []string{"writer-thinking", "writer-thinking-slow", "writer-thinking-broken", "writer-thinking-only", "writer-natural"}}}
		if _, err := store.Save(settings); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("NEWPRESS_AI_IMAGES_BROWSER") == "1" {
		settings := store.Snapshot()
		settings.DefaultProvider = "writer"
		settings.DefaultImageProvider = "gpt-image"
		settings.Providers = []aieditor.Provider{
			{ID: "writer", Name: "模拟写作", BaseURL: "https://fixture.example.test/v1", APIKey: "writer-test-key", Models: []string{"writer-natural"}},
			{ID: "gpt-image", Name: "GPT Image · 模拟", Protocol: aieditor.ProtocolOpenAIImages, BaseURL: "https://fixture.example.test/v1", APIKey: "image-test-key", Models: []string{"image-fast", "image-slow", "image-fail"}},
			{ID: "nano", Name: "Nano Banana · 模拟", Protocol: aieditor.ProtocolGemini, BaseURL: "https://fixture.example.test/v1beta", APIKey: "image-test-key", Models: []string{"gemini-2.5-flash-image"}},
			{ID: "seedream", Name: "字节 Seedream · 模拟", Protocol: aieditor.ProtocolSeedream, BaseURL: "https://fixture.example.test/api/v3", APIKey: "image-test-key", Models: []string{"seedream-image"}},
			{ID: "nano-interactions", Name: "Nano Interactions · 模拟", Protocol: aieditor.ProtocolGeminiInteractions, BaseURL: "https://fixture.example.test/v1beta", APIKey: "image-test-key", Models: []string{"gemini-3.1-flash-image"}},
		}
		if _, err := store.Save(settings); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("NEWPRESS_AI_USAGE_BROWSER") == "1" {
		seedAIUsageBrowser(t, s)
	}
	encodedImage := aiTestImage(t)
	s.aiImages = &aieditor.ImageClient{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		delay := 3 * time.Second
		if payload["model"] == "image-slow" {
			delay = 35 * time.Second
		}
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-time.After(delay):
		}
		if payload["model"] == "image-fail" {
			return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`{"error":"synthetic limit"}`))}, nil
		}
		body := `{"data":[{"b64_json":"` + encodedImage + `"}]}`
		if strings.HasSuffix(r.URL.Path, ":generateContent") {
			body = `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + encodedImage + `"}}]}}]}`
		}
		if strings.HasSuffix(r.URL.Path, "/interactions") {
			body = `{"outputs":[{"type":"image","mime_type":"image/png","data":"` + encodedImage + `"}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "Bearer invalid-test-key" {
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"error":"invalid test key"}`))}, nil
		}
		if strings.HasSuffix(r.URL.Path, "/models") {
			if r.Header.Get("x-api-key") != "" {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"claude-fixture","display_name":"Claude fixture"}]}`))}, nil
			}
			if r.Header.Get("x-goog-api-key") != "" {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[{"name":"models/gemini-fixture","supportedGenerationMethods":["generateContent"]},{"name":"models/gemini-2.5-flash-image"},{"name":"models/gemini-3.1-flash-image"}]}`))}, nil
			}
			if r.Header.Get("Authorization") == "Bearer image-test-key" {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"image-fast","output_modalities":["image"]},{"id":"image-slow","output_modalities":["image"]},{"id":"image-fail","output_modalities":["image"]}]}`))}, nil
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"writer-natural"},{"id":"writer-careful"},{"id":"writer-slow"},{"id":"writer-broken"}]}`))}, nil
		}
		var request struct {
			Model    string             `json:"model"`
			Messages []aieditor.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		body := "文章保存在自己的服务器里，迁移时可以连同图片一起导出。域名不变，旧链接就能继续使用。\n\n## 维护要花多少时间\n\n服务器和程序都需要更新。文章不多时，每周检查一次备份，发布后打开手机看一遍排版，通常就能发现明显的问题。\n\n不确定自己能否持续维护，可以先用静态页面试写。写上几篇，再决定是否增加评论、搜索和会员功能。"
		if len(request.Messages) > 1 && strings.Contains(request.Messages[1].Content, "写作方式：去除 AI 感") {
			body = "文章和图片保存在自己的服务器里，可以一起导出。迁移后保留域名，旧链接仍然可用。\n\n服务器和程序需要定期更新。每周检查备份，发布后用手机看一遍排版。暂时拿不准是否能持续维护，就先用静态页面写几篇，再决定是否增加评论、搜索和会员功能。"
		}
		metadata := `<newpress-meta>{"title":"个人博客的维护成本","slug":"blog-maintenance","excerpt":"自己托管博客，需要定期检查备份、更新程序，并在发布后核对页面。","tags":["博客"],"category_ids":[],"cover_prompt":"桌上的笔记本"}</newpress-meta>`
		frame, terminal := func(text string) string { return aiFrame(text, "") }, aiFrame("", "stop")+"data: [DONE]\n\n"
		encodeEvent := func(value any) string { data, _ := json.Marshal(value); return "data: " + string(data) + "\n\n" }
		thoughtFrame := func(text string) string {
			return encodeEvent(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"reasoning_content": text}}}})
		}
		// Deliberately synthetic token counts, unrelated to the fixture's text.
		usageFrame := func(output int) string {
			return encodeEvent(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 120, "completion_tokens": output, "total_tokens": 120 + output, "prompt_tokens_details": map[string]int{"cached_tokens": 80}, "completion_tokens_details": map[string]int{"reasoning_tokens": min(output, 180)}}})
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/responses"):
			usageFrame = func(int) string { return "" }
			thoughtFrame = func(text string) string {
				return encodeEvent(map[string]any{"type": "response.reasoning_summary_text.delta", "output_index": 0, "summary_index": 0, "delta": text})
			}
			frame = func(text string) string {
				return encodeEvent(map[string]any{"type": "response.output_text.delta", "delta": text})
			}
			terminal = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
		case strings.HasSuffix(r.URL.Path, "/messages"):
			usageFrame = func(output int) string {
				return encodeEvent(map[string]any{"type": "message_delta", "usage": map[string]int{"output_tokens": output}})
			}
			thoughtFrame = func(text string) string {
				return encodeEvent(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "thinking_delta", "thinking": text}})
			}
			frame = func(text string) string {
				return encodeEvent(map[string]any{"type": "content_block_delta", "delta": map[string]string{"type": "text_delta", "text": text}})
			}
			terminal = "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
		case strings.HasSuffix(r.URL.Path, ":streamGenerateContent"):
			usageFrame = func(output int) string {
				return encodeEvent(map[string]any{"usageMetadata": map[string]int{"promptTokenCount": 120, "candidatesTokenCount": max(0, output-180), "thoughtsTokenCount": min(output, 180), "totalTokenCount": 120 + output}})
			}
			thoughtFrame = func(text string) string {
				return encodeEvent(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"thought": true, "text": text}}}}}})
			}
			frame = func(text string) string {
				return encodeEvent(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]string{"text": text}}}}}})
			}
			terminal = "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"
		}
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			if strings.HasPrefix(request.Model, "writer-thinking") {
				if _, err := io.WriteString(writer, usageFrame(0)); err != nil {
					return
				}
				thought := []rune("**模拟思考摘要 · 仅用于界面验证**\n\n" + strings.Repeat("先看现有材料：文章讨论个人博客的维护成本，需要说明备份、更新和迁移涉及哪些具体操作。\n\n正文从读者手里的事情展开，保留材料中已有的事实，不添加没有来源的数字。\n\n", 4) + "最后检查标题与正文是否一致。\n\n<img src=x onerror=alert(1)> 这段测试文本应当原样显示。")
				for i := 0; i < len(thought); i += 18 {
					delay := 70 * time.Millisecond
					if request.Model == "writer-thinking-slow" {
						delay = 250 * time.Millisecond
					}
					select {
					case <-r.Context().Done():
						return
					case <-time.After(delay):
					}
					if _, err := io.WriteString(writer, thoughtFrame(string(thought[i:min(i+18, len(thought))]))); err != nil {
						return
					}
					if request.Model == "writer-thinking-only" && i > 110 {
						return
					}
					if i == 108 {
						if _, err := io.WriteString(writer, usageFrame(60)); err != nil {
							return
						}
					}
				}
			}
			text := []rune(metadata + "\n\n" + body)
			delay := 45 * time.Millisecond
			if request.Model == "writer-slow" {
				delay = 300 * time.Millisecond
			}
			for i := 0; i < len(text); i += 15 {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(delay):
				}
				end := min(i+15, len(text))
				if _, err := io.WriteString(writer, frame(string(text[i:end]))); err != nil {
					return
				}
				if (request.Model == "writer-broken" || request.Model == "writer-thinking-broken") && i > 250 {
					return
				}
			}
			if strings.HasPrefix(request.Model, "writer-thinking") {
				if _, err := io.WriteString(writer, usageFrame(360)); err != nil {
					return
				}
			}
			_, _ = io.WriteString(writer, terminal)
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
	})}}
	if os.Getenv("NEWPRESS_AI_LOCAL_BROWSER") == "1" {
		// Exercise the production scoped transport against a real local HTTP
		// service, while retaining synthetic responses and a temporary store.
		transport := s.aiClient.HTTP.Transport
		upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			response, err := transport.RoundTrip(r)
			if err != nil {
				http.Error(w, "synthetic upstream unavailable", 502)
				return
			}
			defer response.Body.Close()
			for key, values := range response.Header {
				w.Header()[key] = values
			}
			w.WriteHeader(response.StatusCode)
			_, _ = io.Copy(w, response.Body)
		}))
		upstream.Listener.Close()
		upstream.Listener, err = net.Listen("tcp", "127.0.0.1:19427")
		if err != nil {
			t.Fatal(err)
		}
		upstream.Start()
		defer upstream.Close()
		t.Log("Synthetic local AI endpoint: http://127.0.0.1:19427/v1")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:19426")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second}
	defer server.Close()
	go server.Serve(listener)
	t.Log("Synthetic AI settings browser fixture: http://127.0.0.1:19426")
	select {
	case <-t.Context().Done():
	case <-time.After(25 * time.Minute):
	}
}
