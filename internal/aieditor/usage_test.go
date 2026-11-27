package aieditor

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestUsageSnapshotsNormalizeWithoutDoubleCounting(t *testing.T) {
	for _, tc := range []struct {
		name, protocol string
		updates        []string
		want           string
	}{
		{"chat-zero-and-details", "", []string{`{"prompt_tokens":100,"completion_tokens":5}`, `null`, `{"prompt_tokens":0,"completion_tokens":12,"total_tokens":12,"completion_tokens_details":{"reasoning_tokens":4},"prompt_tokens_details":{"cached_tokens":0}}`}, `{"prompt_tokens":0,"completion_tokens":12,"total_tokens":12,"reasoning_tokens":4,"cached_tokens":0,"total_source":"reported"}`},
		{"deepseek-cache", "", []string{`{"prompt_tokens":80,"completion_tokens":20,"prompt_cache_hit_tokens":30,"prompt_cache_miss_tokens":50}`}, `{"prompt_tokens":80,"completion_tokens":20,"total_tokens":100,"cached_tokens":30,"total_source":"calculated"}`},
		{"responses-details", ProtocolResponses, []string{`{"input_tokens":90,"output_tokens":30,"total_tokens":120,"input_tokens_details":{"cached_tokens":40},"output_tokens_details":{"reasoning_tokens":10}}`}, `{"prompt_tokens":90,"completion_tokens":30,"total_tokens":120,"reasoning_tokens":10,"cached_tokens":40,"total_source":"reported"}`},
		{"claude-cumulative-cache", ProtocolAnthropic, []string{`{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"output_tokens":1}`, `{"output_tokens":5}`, `{"output_tokens":5}`, `{"output_tokens":12}`}, `{"prompt_tokens":60,"completion_tokens":12,"total_tokens":72,"cached_tokens":20,"cache_creation_tokens":30,"total_source":"calculated"}`},
		{"claude-zero-update", ProtocolAnthropic, []string{`{"input_tokens":10,"cache_read_input_tokens":20,"output_tokens":5}`, `{"cache_read_input_tokens":0,"output_tokens":0}`}, `{"prompt_tokens":10,"completion_tokens":0,"total_tokens":10,"cached_tokens":0,"total_source":"calculated"}`},
		{"gemini-thoughts-and-tool", ProtocolGeminiText, []string{`{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":6}`, `{"candidatesTokenCount":4,"toolUsePromptTokenCount":3,"cachedContentTokenCount":5,"totalTokenCount":23}`}, `{"prompt_tokens":10,"completion_tokens":10,"total_tokens":23,"reasoning_tokens":6,"cached_tokens":5,"tool_prompt_tokens":3,"total_source":"reported"}`},
		{"total-only", "", []string{`{"total_tokens":70}`}, `{"total_tokens":70,"total_source":"reported"}`},
		{"partial-does-not-fake-output", "", []string{`{"prompt_tokens":70}`}, `{"prompt_tokens":70}`},
		{"invalid-optional-stats", "", []string{`null`, `{}`, `"broken"`, `{"prompt_tokens":-1,"completion_tokens":"5","total_tokens":9007199254740992}`}, `{}`},
		{"missing-candidate-not-zero", ProtocolGeminiText, []string{`{"promptTokenCount":2,"thoughtsTokenCount":5}`}, `{"prompt_tokens":2,"reasoning_tokens":5}`},
		{"new-output-invalidates-old-total", "", []string{`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`, `{"completion_tokens":8}`}, `{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18,"total_source":"calculated"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := usageTracker{}
			for _, raw := range tc.updates {
				tracker.update(json.RawMessage(raw), tc.protocol)
			}
			got, _ := json.Marshal(tracker.value)
			var gotValue, wantValue any
			_ = json.Unmarshal(got, &gotValue)
			_ = json.Unmarshal([]byte(tc.want), &wantValue)
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Fatalf("got %s; want %s", got, tc.want)
			}
		})
	}
}

func TestUsageStreamTrailersAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, body string
		wantError            bool
	}{
		{"chat-tail", "", textFrames(`{"choices":[{"delta":{"content":"正文"},"finish_reason":"stop"}],"usage":null}`, `{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}}`, `[DONE]`, `{"usage":{"total_tokens":999}}`), false},
		{"gemini-tail", ProtocolGeminiText, textFrames(`{"candidates":[{"content":{"parts":[{"text":"正文"}]},"finishReason":"STOP"}]}`, `{"candidates":[{"content":{"parts":[{"text":"迟到正文"}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3,"thoughtsTokenCount":1,"totalTokenCount":16}}`), false},
		{"chat-disconnect", "", textFrames(`{"choices":[{"delta":{"content":"正文"}}],"usage":{"prompt_tokens":12,"completion_tokens":4}}`), true},
		{"claude-error", ProtocolAnthropic, textFrames(`{"type":"message_start","message":{"usage":{"input_tokens":12}}}`, `{"type":"content_block_delta","delta":{"type":"text_delta","text":"正文"}}`, `{"type":"message_delta","usage":{"output_tokens":4}}`, `{"type":"error","error":{"message":"secret"}}`), true},
		{"responses-error", ProtocolResponses, textFrames(`{"type":"response.output_text.delta","delta":"正文"}`, `{"type":"response.failed","response":{"status":"failed","usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}}`), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connection := testConnection()
			connection.Protocol = tc.protocol
			var snapshots []Usage
			result, err := fakeClient(t, 200, "text/event-stream", tc.body, nil).StreamWithCallbacks(t.Context(), connection, nil, 0, StreamCallbacks{Usage: func(value Usage) error { snapshots = append(snapshots, value); return nil }})
			if (err != nil) != tc.wantError || result.Text != "正文" || result.Usage.TotalTokens == nil || *result.Usage.TotalTokens != 16 || len(snapshots) == 0 {
				t.Fatalf("result=%+v snapshots=%+v err=%v", result, snapshots, err)
			}
		})
	}
}

func TestUsageIsOptionalAndDoesNotInterruptContent(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"prompt_tokens":"invalid"}`, `{"completion_tokens":-1}`} {
		result, err := fakeClient(t, 200, "text/event-stream", textFrames(`{"choices":[{"delta":{"content":"正文"},"finish_reason":"stop"}],"usage":`+raw+`}`, `[DONE]`), nil).StreamWithCallbacks(t.Context(), testConnection(), nil, 0, StreamCallbacks{Usage: func(Usage) error { t.Fatal("invalid usage was emitted"); return nil }})
		if err != nil || result.Text != "正文" || result.Usage.TotalTokens != nil {
			t.Fatal(result, err)
		}
	}
}

func TestUsageRequestOptOutAndNoAutomaticRetry(t *testing.T) {
	for _, protocol := range []string{"", ProtocolAzure, ProtocolAzureLegacy, ProtocolResponses, ProtocolAnthropic, ProtocolGeminiText} {
		c := testConnection()
		c.Protocol = protocol
		_, payload := textRequest(c, nil, 0)
		want := protocol == "" || protocol == ProtocolAzure || protocol == ProtocolAzureLegacy
		if (payload["stream_options"] != nil) != want {
			t.Fatal(protocol, payload)
		}
		disabled := false
		c.StreamUsage = &disabled
		_, payload = textRequest(c, nil, 0)
		if payload["stream_options"] != nil {
			t.Fatal(protocol, payload)
		}
	}
	calls := 0
	client := fakeClient(t, 400, "application/json", `{"error":{"message":"stream_options unsupported"}}`, func(*http.Request) { calls++ })
	_, err := client.Stream(t.Context(), testConnection(), nil, 0, nil)
	if err == nil || calls != 1 {
		t.Fatal("rejected generation retried", calls, err)
	}
}

func TestUsageCallbackCanStopStream(t *testing.T) {
	stop := errors.New("stop usage delivery")
	c := fakeClient(t, 200, "text/event-stream", textFrames(`{"usage":{"prompt_tokens":1}}`, `{"choices":[{"delta":{"content":"不应到达"},"finish_reason":"stop"}]}`, `[DONE]`), nil)
	result, err := c.StreamWithCallbacks(t.Context(), testConnection(), nil, 0, StreamCallbacks{Usage: func(Usage) error { return stop }})
	if err == nil || result.Text != "" || result.Usage.PromptTokens == nil {
		t.Fatal(result, err)
	}
}

func TestResponsesContentSnapshotsPreserveSuffixAndAvoidRepeats(t *testing.T) {
	c := testConnection()
	c.Protocol = ProtocolResponses
	body := textFrames(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"首段"}`, `{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"首段补全。"}`, `{"type":"response.content_part.done","output_index":0,"content_index":1,"part":{"type":"output_text","text":"\n第二段。"}}`, `{"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"首段补全。"},{"type":"output_text","text":"\n第二段。"}]}}`, `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"首段补全。"},{"type":"output_text","text":"\n第二段。"}]}]}}`)
	var emitted strings.Builder
	result, err := fakeClient(t, 200, "text/event-stream", body, nil).Stream(t.Context(), c, nil, 0, func(text string) error { emitted.WriteString(text); return nil })
	if err != nil || result.Text != "首段补全。\n第二段。" || result.Text != emitted.String() {
		t.Fatal(result, err)
	}
}
