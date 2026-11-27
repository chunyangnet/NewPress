package aieditor

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func textFrames(frames ...string) string {
	return "data: " + strings.Join(frames, "\n\ndata: ") + "\n\n"
}

func TestNativeTextRequestsAndStreams(t *testing.T) {
	for _, tc := range []struct {
		protocol, path, auth, body string
		check                      func(*testing.T, map[string]any)
	}{
		{ProtocolResponses, "/v1/responses", "Authorization", textFrames(`{"type":"response.output_text.delta","delta":"正文"}`, `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"正文"}]}],"usage":{"input_tokens":12,"output_tokens":4}}}`), func(t *testing.T, p map[string]any) {
			if p["input"] == nil || p["messages"] != nil || p["store"] != false || p["max_output_tokens"] != float64(8192) {
				t.Fatalf("unexpected responses payload: %#v", p)
			}
		}},
		{ProtocolAzureResponses, "/v1/responses", "api-key", textFrames(`{"type":"response.output_text.delta","delta":"正文"}`, `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":12,"output_tokens":4}}}`), nil},
		{ProtocolAnthropic, "/v1/messages", "x-api-key", textFrames(`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":2}}}`, `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"不要输出"}}`, `{"type":"content_block_delta","delta":{"type":"text_delta","text":"正文"}}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`, `{"type":"message_stop"}`), func(t *testing.T, p map[string]any) {
			messages := p["messages"].([]any)
			if p["system"] != "系统指令" || len(messages) != 1 || p["max_tokens"] != float64(8192) {
				t.Fatalf("unexpected messages payload: %#v", p)
			}
		}},
		{ProtocolGeminiText, "/v1/models/writer-model:streamGenerateContent?alt=sse", "x-goog-api-key", textFrames(`{"candidates":[{"content":{"parts":[{"text":"不要输出","thought":true},{"text":"正文"}]}}]}`, `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3,"thoughtsTokenCount":1}}`), func(t *testing.T, p map[string]any) {
			if p["systemInstruction"] == nil || p["contents"] == nil || p["model"] != nil || p["generationConfig"].(map[string]any)["maxOutputTokens"] != float64(8192) {
				t.Fatalf("unexpected Gemini payload: %#v", p)
			}
		}},
		{ProtocolAzure, "/v1/chat/completions", "api-key", textFrames(`{"choices":[{"delta":{"content":"正文"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":4}}`, `[DONE]`), nil},
		{ProtocolAzureLegacy, "/v1/deployments/writer-model/chat/completions?api-version=2024-10-21", "api-key", textFrames(`{"choices":[{"delta":{"content":"正文"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":4}}`, `[DONE]`), nil},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			connection := testConnection()
			connection.Protocol = tc.protocol
			client := fakeClient(t, 200, "text/event-stream", tc.body, func(r *http.Request) {
				if r.URL.RequestURI() != tc.path || r.Method != "POST" {
					t.Fatalf("unexpected path: %s", r.URL.RequestURI())
				}
				for _, header := range []string{"Authorization", "api-key", "x-api-key", "x-goog-api-key"} {
					want := ""
					if header == tc.auth {
						want = connection.APIKey
						if header == "Authorization" {
							want = "Bearer " + want
						}
					}
					if r.Header.Get(header) != want {
						t.Fatalf("unexpected %s header", header)
					}
				}
				if tc.protocol == ProtocolAnthropic && r.Header.Get("anthropic-version") != "2023-06-01" {
					t.Fatal("missing Anthropic version")
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if tc.check != nil {
					tc.check(t, payload)
				}
			})
			var deltas strings.Builder
			result, err := client.Stream(t.Context(), connection, []Message{{Role: "system", Content: "系统指令"}, {Role: "user", Content: "写文章"}}, 8192, func(text string) error { deltas.WriteString(text); return nil })
			if err != nil || result.Text != "正文" || deltas.String() != "正文" || result.FinishReason != "stop" || result.Usage.PromptTokens == nil || *result.Usage.PromptTokens != 12 || result.Usage.CompletionTokens == nil || *result.Usage.CompletionTokens != 4 || result.Usage.TotalTokens == nil || *result.Usage.TotalTokens != 16 {
				t.Fatalf("result=%#v deltas=%q err=%v", result, deltas.String(), err)
			}
		})
	}
}

func TestNativeJSONAndTruncatedOutput(t *testing.T) {
	for _, tc := range []struct{ protocol, body string }{
		{ProtocolResponses, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"部分正文"}]}]}`},
		{ProtocolAnthropic, `{"type":"message","content":[{"type":"text","text":"部分正文"}],"stop_reason":"max_tokens"}`},
		{ProtocolGeminiText, `{"candidates":[{"content":{"parts":[{"text":"部分正文"}]},"finishReason":"MAX_TOKENS"}]}`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			connection := testConnection()
			connection.Protocol = tc.protocol
			result, err := fakeClient(t, 200, "application/json", tc.body, nil).Stream(t.Context(), connection, nil, 0, func(string) error { return nil })
			if err != nil || result.Text != "部分正文" || result.FinishReason != "length" {
				t.Fatalf("%#v %v", result, err)
			}
		})
	}
}

func TestNativeStreamsRequireTerminalEventsAndRedactFailures(t *testing.T) {
	for _, tc := range []struct{ name, protocol, body string }{
		{"responses-disconnect", ProtocolResponses, textFrames(`{"type":"response.output_text.delta","delta":"部分正文"}`, `[DONE]`)},
		{"claude-disconnect", ProtocolAnthropic, textFrames(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"部分正文"}}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)},
		{"gemini-disconnect", ProtocolGeminiText, textFrames(`{"candidates":[{"content":{"parts":[{"text":"部分正文"}]}}]}`)},
		{"responses-refusal", ProtocolResponses, textFrames(`{"type":"response.refusal.delta","delta":"synthetic-test-key"}`)},
		{"responses-failed", ProtocolResponses, textFrames(`{"type":"response.failed","response":{"status":"failed","error":{"message":"synthetic-test-key"}}}`)},
		{"claude-refusal", ProtocolAnthropic, textFrames(`{"type":"message_delta","delta":{"stop_reason":"refusal"}}`)},
		{"claude-error", ProtocolAnthropic, textFrames(`{"type":"error","error":{"message":"synthetic-test-key"}}`)},
		{"gemini-refusal", ProtocolGeminiText, textFrames(`{"promptFeedback":{"blockReason":"SAFETY"}}`)},
		{"gemini-error", ProtocolGeminiText, textFrames(`{"error":{"message":"synthetic-test-key"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connection := testConnection()
			connection.Protocol = tc.protocol
			var received strings.Builder
			result, err := fakeClient(t, 200, "text/event-stream", tc.body, nil).Stream(t.Context(), connection, nil, 0, func(text string) error { received.WriteString(text); return nil })
			if err == nil || strings.Contains(err.Error(), connection.APIKey) {
				t.Fatalf("unsafe or absent error: %v", err)
			}
			if strings.Contains(tc.name, "disconnect") && (received.String() != "部分正文" || result.Text != "部分正文") {
				t.Fatal("partial text was lost")
			}
		})
	}
}

func TestNativeStreamOutputLimitsAndConsumerCancellation(t *testing.T) {
	for _, protocol := range []string{ProtocolResponses, ProtocolAnthropic, ProtocolGeminiText} {
		t.Run(protocol, func(t *testing.T) {
			connection := testConnection()
			connection.Protocol = protocol
			frame := func(text string) string {
				encoded, _ := json.Marshal(text)
				switch protocol {
				case ProtocolAnthropic:
					return `{"type":"content_block_delta","delta":{"type":"text_delta","text":` + string(encoded) + `}}`
				case ProtocolGeminiText:
					return `{"candidates":[{"content":{"parts":[{"text":` + string(encoded) + `}]}}]}`
				default:
					return `{"type":"response.output_text.delta","delta":` + string(encoded) + `}`
				}
			}
			client := fakeClient(t, 200, "text/event-stream", textFrames(frame(strings.Repeat("a", MaxOutputBytes)), frame("b")), nil)
			result, err := client.Stream(t.Context(), connection, nil, 0, func(string) error { return nil })
			var known *Error
			if !errors.As(err, &known) || known.Code != "output_limit" || len(result.Text) != MaxOutputBytes {
				t.Fatalf("limit: %d %v", len(result.Text), err)
			}
			client = fakeClient(t, 200, "text/event-stream", textFrames(frame("first"), frame("second")), nil)
			calls := 0
			_, err = client.Stream(t.Context(), connection, nil, 0, func(string) error { calls++; return io.ErrClosedPipe })
			if err == nil || calls != 1 {
				t.Fatalf("consumer cancellation: %d %v", calls, err)
			}
		})
	}
}

func TestNativeModelDiscoveryPagination(t *testing.T) {
	for _, protocol := range []string{ProtocolAnthropic, ProtocolGeminiText} {
		t.Run(protocol, func(t *testing.T) {
			connection := testConnection()
			connection.Protocol = protocol
			calls := 0
			client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				body := `{"data":[{"id":"claude-a","display_name":"Claude"}],"has_more":true,"last_id":"claude-a"}`
				if protocol == ProtocolGeminiText {
					if r.Header.Get("x-goog-api-key") != connection.APIKey {
						t.Fatal("wrong Gemini auth")
					}
					body = `{"models":[{"name":"models/gemini-a","displayName":"Gemini","supportedGenerationMethods":["generateContent"]},{"name":"models/embedding","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"next+/="}`
					if calls == 2 {
						if r.URL.Query().Get("pageToken") != "next+/=" {
							t.Fatal("page token not encoded")
						}
						body = `{"models":[{"name":"models/gemini-b"},{"name":"models/gemini-a"}]}`
					}
				} else {
					if r.Header.Get("x-api-key") != connection.APIKey {
						t.Fatal("wrong Claude auth")
					}
					if calls == 2 {
						if r.URL.Query().Get("after_id") != "claude-a" {
							t.Fatal("missing cursor")
						}
						body = `{"data":[{"id":"claude-b"},{"id":"claude-a"}]}`
					}
				}
				if calls > 2 {
					t.Fatal("unexpected extra page")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			models, err := client.Models(t.Context(), connection)
			if err != nil || len(models) != 2 || calls != 2 || models[0].Name == "" {
				t.Fatalf("models=%#v calls=%d err=%v", models, calls, err)
			}
		})
	}
}

func TestTextProtocolEndpointNormalization(t *testing.T) {
	for _, tc := range []struct{ protocol, raw, want string }{
		{ProtocolResponses, "https://api.example.test/v1/responses", "https://api.example.test/v1"},
		{ProtocolAnthropic, "https://api.example.test/v1/messages", "https://api.example.test/v1"},
		{ProtocolGeminiText, "https://api.example.test", "https://api.example.test/v1beta"},
		{ProtocolAzure, "https://resource.openai.azure.com", "https://resource.openai.azure.com/openai/v1"},
		{ProtocolAzureResponses, "https://resource.openai.azure.com/openai/v1/responses", "https://resource.openai.azure.com/openai/v1"},
		{ProtocolAzureLegacy, "https://resource.openai.azure.com", "https://resource.openai.azure.com/openai"},
	} {
		got, err := normalizeEndpoint(tc.raw, tc.protocol)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %s %v", tc.protocol, got, err)
		}
	}
	connection := testConnection()
	connection.Protocol = ProtocolAzureLegacy
	for _, model := range []string{"../secret", "bad/model", "bad?key=value"} {
		connection.Model = model
		if _, err := connection.Validate(true); err == nil {
			t.Fatal("unsafe deployment accepted", model)
		}
	}
	connection.Model = "deployment"
	connection.APIVersion = "bad&api-key=secret"
	if _, err := connection.Validate(true); err == nil {
		t.Fatal("unsafe API version accepted")
	}
	connection.APIVersion = ""
	normalized, err := connection.Validate(true)
	if err != nil || normalized.APIVersion != DefaultAzureAPIVersion {
		t.Fatalf("version default: %#v %v", normalized, err)
	}
	_, payload := textRequest(Connection{Protocol: ProtocolAnthropic}, []Message{{Role: "user", Content: "test"}}, 0)
	if payload["max_tokens"] != 4096 {
		t.Fatal("Claude requires a default token budget")
	}
}

func TestTextProtocolSettingsPersistenceAndImageSeparation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-settings.json")
	store, err := NewSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	settings := store.Snapshot()
	settings.DefaultProvider = "writer0"
	settings.DefaultImageProvider = "image"
	protocols := []string{"", ProtocolResponses, ProtocolAnthropic, ProtocolGeminiText, ProtocolAzure, ProtocolAzureResponses, ProtocolAzureLegacy}
	for i, protocol := range protocols {
		id := "writer" + string(rune('0'+i))
		settings.Providers = append(settings.Providers, Provider{ID: id, Name: id, Protocol: protocol, BaseURL: "https://provider.example.test", APIKey: "synthetic-test-key", Models: []string{"model"}})
	}
	settings.Providers = append(settings.Providers, Provider{ID: "image", Name: "image", Protocol: ProtocolOpenAIImages, BaseURL: "https://provider.example.test", APIKey: "synthetic-test-key", Models: []string{"image"}})
	saved, err := store.Save(settings)
	if err != nil {
		t.Fatal(err)
	}
	store, err = NewSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range saved.Providers {
		connection, err := store.Resolve(Connection{ProviderID: provider.ID}, true)
		if provider.ID == "image" {
			if err == nil {
				t.Fatal("image resolved as text")
			}
			continue
		}
		if err != nil || connection.Protocol != provider.Protocol || connection.APIKey != "synthetic-test-key" {
			t.Fatalf("resolve %s: %#v %v", provider.ID, connection, err)
		}
		if _, err := store.ResolveImage(Connection{ProviderID: provider.ID}); err == nil {
			t.Fatal("text resolved as image")
		}
		if _, err := store.Resolve(Connection{ProviderID: provider.ID, APIVersion: "2024-10-21"}, true); err == nil {
			t.Fatal("saved API version overridden")
		}
	}
	if _, err := store.Save(saved); err != nil {
		t.Fatal("redacted snapshot should retain keys", err)
	}
}
