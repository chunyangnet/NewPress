package aieditor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeClient(t *testing.T, status int, mediaType, body string, inspect func(*http.Request)) *Client {
	t.Helper()
	return &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if inspect != nil {
			inspect(r)
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {mediaType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
}

func testConnection() Connection {
	return Connection{BaseURL: "https://writer.example.test/v1", APIKey: "synthetic-test-key", Model: "writer-model"}
}

func TestNormalizeEndpoint(t *testing.T) {
	for raw, want := range map[string]string{
		"https://writer.example.test":                     "https://writer.example.test/v1",
		"https://writer.example.test/v1/":                 "https://writer.example.test/v1",
		"https://writer.example.test/v1/chat/completions": "https://writer.example.test/v1",
		"https://writer.example.test/api/compatible/v1":   "https://writer.example.test/api/compatible/v1",
		"https://writer.example.test/api/models":          "https://writer.example.test/api",
	} {
		got, err := NormalizeBaseURL(raw)
		if err != nil || got != want {
			t.Errorf("normalize %q = %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"http://writer.example.test", "https://127.0.0.1", "https://[::1]", "https://10.0.0.1", "https://169.254.169.254", "https://writer.example.test:8443", "https://user:secret@writer.example.test", "https://writer.example.test?key=secret", "https://writer.example.test/#fragment", "https://writer.example.test/%2e%2e/v1"} {
		if _, err := NormalizeBaseURL(raw); err == nil {
			t.Errorf("unsafe endpoint accepted: %s", raw)
		}
	}
}

func TestModelsNormalizeDeduplicateAndDoNotReturnCredentials(t *testing.T) {
	client := fakeClient(t, 200, "application/json", `{"data":[{"id":"z-model"},{"id":"a-model","name":"写作模型"},{"id":"z-model"},{"id":"image-only","output_modalities":["image"]},{"id":"bad\nmodel"}]}`, func(r *http.Request) {
		if r.URL.String() != "https://writer.example.test/v1/models" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic-test-key" || r.URL.RawQuery != "" {
			t.Fatal("wrong model discovery request")
		}
	})
	models, err := client.Models(t.Context(), testConnection())
	if err != nil || len(models) != 2 || models[0].ID != "a-model" || models[1].ID != "z-model" {
		t.Fatalf("models = %#v, %v", models, err)
	}
	encoded, _ := json.Marshal(models)
	if strings.Contains(string(encoded), testConnection().APIKey) {
		t.Fatal("credentials in response")
	}
	for _, status := range []int{301, 400, 401, 403, 404, 429, 500} {
		client = fakeClient(t, status, "application/json", `{"error":"synthetic-test-key secret draft"}`, nil)
		_, err = client.Models(t.Context(), testConnection())
		if err == nil || strings.Contains(err.Error(), "synthetic-test-key") || strings.Contains(err.Error(), "secret draft") {
			t.Fatalf("upstream error not redacted: %v", err)
		}
	}
}

func TestStreamDeliversBeforeUpstreamCompletes(t *testing.T) {
	reader, writer := io.Pipe()
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["stream"] != true || payload["model"] != "writer-model" || payload["max_tokens"] != float64(8192) {
			t.Error("wrong completion payload")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}}, Body: reader}, nil
	})}}
	first := make(chan struct{})
	go func() {
		defer writer.Close()
		// A multi-line SSE data field and a split UTF-8 character must stay intact.
		initial := ": heartbeat\r\ndata: {\"choices\":[\r\ndata: {\"index\":0,\"delta\":{\"content\":\"你好，\"}}]}\r\n\r\n"
		for _, b := range []byte(initial) {
			if _, err := writer.Write([]byte{b}); err != nil {
				return
			}
		}
		select {
		case <-first:
		case <-t.Context().Done():
			return
		}
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"private reasoning\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"世界 🌏\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":8}}\n\ndata: [DONE]\n\n")
	}()
	var deltas []string
	result, err := client.Stream(t.Context(), testConnection(), []Message{{Role: "user", Content: "写文章"}}, 8192, func(text string) error {
		deltas = append(deltas, text)
		if len(deltas) == 1 {
			close(first)
		}
		return nil
	})
	if err != nil || result.Text != "你好，世界 🌏" || len(deltas) != 2 || result.FinishReason != "stop" || result.Usage.CompletionTokens == nil || *result.Usage.CompletionTokens != 8 {
		t.Fatalf("stream = %#v, %#v, %v", result, deltas, err)
	}
}

func TestStreamFailuresRetainDeltasAndRequireTerminalEvent(t *testing.T) {
	for _, item := range []struct{ name, tail string }{
		{"truncated", ""},
		{"provider_error", "data: {\"error\":{\"message\":\"synthetic-test-key\"}}\n\n"},
		{"malformed", "data: not-json\n\n"},
		{"refusal", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n"},
	} {
		t.Run(item.name, func(t *testing.T) {
			client := fakeClient(t, 200, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"已经生成的正文\"}}]}\n\n"+item.tail, nil)
			var received string
			result, err := client.Stream(t.Context(), testConnection(), nil, 0, func(text string) error { received += text; return nil })
			if err == nil || received != "已经生成的正文" || result.Text != received || strings.Contains(err.Error(), "synthetic-test-key") {
				t.Fatalf("incorrect interrupted stream: %#v %v", result, err)
			}
		})
	}
	client := fakeClient(t, 200, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"部分正文\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n", nil)
	result, err := client.Stream(t.Context(), testConnection(), nil, 0, func(string) error { return nil })
	if err != nil || result.FinishReason != "length" {
		t.Fatal("lost token-limit finish reason", result, err)
	}
}

func TestStreamJSONFallbackAndCancellation(t *testing.T) {
	client := fakeClient(t, 200, "application/json", `{"choices":[{"message":{"content":"完整正文"},"finish_reason":"stop"}]}`, nil)
	result, err := client.Stream(t.Context(), testConnection(), nil, 0, func(string) error { return nil })
	if err != nil || result.Text != "完整正文" {
		t.Fatal(result, err)
	}
	entered := make(chan struct{})
	client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := client.Stream(ctx, testConnection(), nil, 0, func(string) error { return nil })
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach provider")
	}
}
