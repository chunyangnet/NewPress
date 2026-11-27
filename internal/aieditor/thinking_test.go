package aieditor

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestThinkingStreamsStaySeparateFromDocuments(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, mime, body, thought string
	}{
		{"compatible", "", "text/event-stream", textFrames(
			`{"choices":[{"index":1,"delta":{"reasoning_content":"忽略候选"}},{"index":0,"delta":{"reasoning_content":"先看材料","reasoning":"重复别名","content":null}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"，再写正文。"}}]}`,
			`{"choices":[{"delta":{"content":"文章正文"},"finish_reason":"stop"}]}`, `[DONE]`), "先看材料，再写正文。"},
		{"reasoning-alias", "", "application/json", `{"choices":[{"message":{"reasoning":"先看材料","content":"文章正文"},"finish_reason":"stop"}]}`, "先看材料"},
		{"thinking-alias", "", "application/json", `{"choices":[{"message":{"thinking":"先看材料","content":"文章正文"},"finish_reason":"stop"}]}`, "先看材料"},
		{"responses", ProtocolResponses, "text/event-stream", textFrames(
			`{"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"先看"}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"材料"}`,
			`{"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":0,"text":"先看材料"}`,
			`{"type":"response.reasoning_summary_part.done","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"先看材料"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","encrypted_content":"secret","summary":[{"type":"summary_text","text":"先看材料"}]}}`,
			`{"type":"response.output_text.delta","output_index":1,"delta":"文章正文"}`,
			`{"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"先看材料"}]},{"type":"message","content":[{"type":"output_text","text":"文章正文"}]}]}}`), "先看材料"},
		{"azure-responses-snapshot", ProtocolAzureResponses, "application/json", `{"status":"completed","output":[{"type":"reasoning","encrypted_content":"secret","summary":[{"type":"summary_text","text":"先看材料"},{"type":"summary_text","text":"再写正文"}]},{"type":"message","content":[{"type":"output_text","text":"文章正文"}]}]}`, "先看材料\n\n再写正文"},
		{"responses-visible-reasoning", ProtocolResponses, "text/event-stream", textFrames(
			`{"type":"response.reasoning_text.delta","output_index":0,"content_index":0,"delta":"先看材料"}`,
			`{"type":"response.reasoning_text.done","output_index":0,"content_index":0,"text":"先看材料"}`,
			`{"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"先看材料"}]},{"type":"message","content":[{"type":"output_text","text":"文章正文"}]}]}}`), "先看材料"},
		{"anthropic", ProtocolAnthropic, "text/event-stream", textFrames(
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"先看"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"材料"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"secret"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"secret"}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"文章正文"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, `{"type":"message_stop"}`), "先看材料"},
		{"anthropic-json", ProtocolAnthropic, "application/json", `{"type":"message","content":[{"type":"thinking","thinking":"先看材料","signature":"secret"},{"type":"redacted_thinking","data":"secret"},{"type":"text","text":"文章正文"}],"stop_reason":"end_turn"}`, "先看材料"},
		{"gemini", ProtocolGeminiText, "text/event-stream", textFrames(
			`{"candidates":[{"content":{"parts":[{"thought":true,"text":"先看材料"},{"thoughtSignature":"secret"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"文章正文"}]},"finishReason":"STOP"}]}`), "先看材料"},
		{"gemini-json", ProtocolGeminiText, "application/json", `{"candidates":[{"content":{"parts":[{"thought":true,"text":"先看材料"},{"text":"文章正文","thoughtSignature":"secret"}]},"finishReason":"STOP"}]}`, "先看材料"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connection := testConnection()
			connection.Protocol = tc.protocol
			var thought, body strings.Builder
			var order []string
			callbacks := StreamCallbacks{
				Thinking: func(delta ThinkingDelta) error {
					thought.WriteString(delta.Text)
					order = append(order, "thinking")
					return nil
				},
				ThinkingDone: func() error { order = append(order, "thinking_done"); return nil },
				Delta:        func(text string) error { body.WriteString(text); order = append(order, "delta"); return nil },
			}
			result, err := fakeClient(t, 200, tc.mime, tc.body, nil).StreamWithCallbacks(t.Context(), connection, nil, 0, callbacks)
			if err != nil || thought.String() != tc.thought || result.Text != "文章正文" || body.String() != result.Text {
				t.Fatalf("thought=%q body=%q result=%#v err=%v", thought.String(), body.String(), result, err)
			}
			if len(order) < 3 || order[len(order)-2] != "thinking_done" || order[len(order)-1] != "delta" || strings.Count(strings.Join(order, ","), "thinking_done") != 1 {
				t.Fatalf("phase ordering: %v", order)
			}
		})
	}
}

func TestThinkingSnapshotSuffixesAndInterleavedPhases(t *testing.T) {
	connection := testConnection()
	connection.Protocol = ProtocolResponses
	stream := textFrames(
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"先看"}`,
		`{"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":0,"text":"先看材料"}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"前段"}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":2,"summary_index":0,"delta":"补充核对"}`,
		`{"type":"response.output_text.delta","output_index":3,"delta":"后段"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`)
	var events []string
	result, err := fakeClient(t, 200, "text/event-stream", stream, nil).StreamWithCallbacks(t.Context(), connection, nil, 0, StreamCallbacks{
		Thinking:     func(d ThinkingDelta) error { events = append(events, "think:"+d.Text); return nil },
		ThinkingDone: func() error { events = append(events, "end"); return nil },
		Delta:        func(text string) error { events = append(events, "text:"+text); return nil },
	})
	want := []string{"think:先看", "think:材料", "end", "text:前段", "think:\n\n", "think:补充核对", "end", "text:后段"}
	if err != nil || result.Text != "前段后段" || !reflect.DeepEqual(events, want) {
		t.Fatalf("%v %q %v", events, result.Text, err)
	}
}

func TestThinkingLimitDoesNotStopArticle(t *testing.T) {
	stream := textFrames(`{"choices":[{"delta":{"reasoning_content":"`+strings.Repeat("想", MaxThinkingBytes/3+2)+`"}}]}`, `{"choices":[{"delta":{"reasoning_content":"more","content":"正常正文"},"finish_reason":"stop"}]}`, `[DONE]`)
	var thought strings.Builder
	warnings := 0
	result, err := fakeClient(t, 200, "text/event-stream", stream, nil).StreamWithCallbacks(t.Context(), testConnection(), nil, 0, StreamCallbacks{Thinking: func(d ThinkingDelta) error {
		thought.WriteString(d.Text)
		if d.Truncated {
			warnings++
		}
		return nil
	}})
	if err != nil || result.Text != "正常正文" || thought.Len() > MaxThinkingBytes || thought.Len() < MaxThinkingBytes-3 || !utf8.ValidString(thought.String()) || warnings != 1 {
		t.Fatalf("length=%d warnings=%d result=%#v err=%v", thought.Len(), warnings, result, err)
	}
}

func TestThinkingFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"disconnect", "consumer-error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stream := textFrames(`{"choices":[{"delta":{"reasoning_content":"收到思考"}}]}`)
			if mode != "disconnect" {
				stream += textFrames(`{"choices":[{"delta":{"content":"不应收到"},"finish_reason":"stop"}]}`, `[DONE]`)
			}
			calls, done := 0, 0
			result, err := fakeClient(t, 200, "text/event-stream", stream, nil).StreamWithCallbacks(ctx, testConnection(), nil, 0, StreamCallbacks{
				Thinking: func(d ThinkingDelta) error {
					calls++
					if mode == "consumer-error" {
						return io.ErrClosedPipe
					}
					if mode == "cancel" {
						cancel()
					}
					return nil
				},
				ThinkingDone: func() error { done++; return nil },
			})
			if err == nil || result.Text != "" || calls != 1 || done != 0 {
				t.Fatalf("%#v %v calls=%d done=%d", result, err, calls, done)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestModelsWithoutThinkingKeepExistingBehavior(t *testing.T) {
	stream := textFrames(`{"choices":[{"delta":{"reasoning":{"encrypted_content":"secret"},"content":"正文"},"finish_reason":"stop"}]}`, `[DONE]`)
	result, err := fakeClient(t, 200, "text/event-stream", stream, nil).StreamWithCallbacks(t.Context(), testConnection(), nil, 0, StreamCallbacks{
		Thinking:     func(ThinkingDelta) error { t.Error("fabricated thinking"); return nil },
		ThinkingDone: func() error { t.Error("fabricated thinking phase"); return nil },
	})
	if err != nil || result.Text != "正文" {
		t.Fatalf("%#v %v", result, err)
	}
}
