package aieditor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

func textRequest(c Connection, messages []Message, maxTokens int) (string, map[string]any) {
	payload := map[string]any{"model": c.Model, "messages": messages, "stream": true}
	path := "/chat/completions"
	switch c.Protocol {
	case ProtocolResponses, ProtocolAzureResponses:
		payload = map[string]any{"model": c.Model, "input": messages, "stream": true, "store": false}
		if maxTokens > 0 {
			payload["max_output_tokens"] = maxTokens
		}
		return "/responses", payload
	case ProtocolAnthropic:
		var system []string
		conversation := []Message{}
		for _, message := range messages {
			if message.Role == "system" {
				system = append(system, message.Content)
			} else {
				conversation = append(conversation, message)
			}
		}
		payload["messages"] = conversation
		if len(system) > 0 {
			payload["system"] = strings.Join(system, "\n\n")
		}
		// Messages requires this field, unlike Chat Completions and Responses.
		if maxTokens <= 0 {
			maxTokens = 4096
		}
		path = "/messages"
	case ProtocolGeminiText:
		contents, system := []map[string]any{}, []map[string]string{}
		for _, message := range messages {
			if message.Role == "system" {
				system = append(system, map[string]string{"text": message.Content})
				continue
			}
			role := message.Role
			if role == "assistant" {
				role = "model"
			}
			contents = append(contents, map[string]any{"role": role, "parts": []map[string]string{{"text": message.Content}}})
		}
		payload = map[string]any{"contents": contents}
		if len(system) > 0 {
			payload["systemInstruction"] = map[string]any{"parts": system}
		}
		if maxTokens > 0 {
			payload["generationConfig"] = map[string]int{"maxOutputTokens": maxTokens}
		}
		return "/models/" + strings.TrimPrefix(c.Model, "models/") + ":streamGenerateContent?alt=sse", payload
	case ProtocolAzureLegacy:
		path = "/deployments/" + url.PathEscape(c.Model) + "/chat/completions?api-version=" + url.QueryEscape(c.APIVersion)
	}
	if maxTokens > 0 {
		payload["max_tokens"] = maxTokens
	}
	if c.Protocol != ProtocolAnthropic && (c.StreamUsage == nil || *c.StreamUsage) {
		payload["stream_options"] = map[string]bool{"include_usage": true}
	}
	return path, payload
}

func (c *Client) nativeModels(ctx context.Context, connection Connection) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	models, seen, pages := []Model{}, map[string]bool{}, map[string]bool{}
	next, total := "", 0
	for page := 0; page < 10 && len(models) < 1000; page++ {
		path := "/models?limit=100"
		if connection.Protocol == ProtocolGeminiText {
			path = "/models?pageSize=1000"
			if next != "" {
				path += "&pageToken=" + url.QueryEscape(next)
			}
		} else if next != "" {
			path += "&after_id=" + url.QueryEscape(next)
		}
		response, err := c.do(ctx, connection, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		response.Body.Close()
		total += len(data)
		if err != nil || total > 2<<20 {
			return nil, failure("invalid_models", "模型列表过大或传输中断，可手动填写模型 ID", http.StatusBadGateway)
		}
		var payload struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
			Models  []struct {
				Name        string   `json:"name"`
				DisplayName string   `json:"displayName"`
				Methods     []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if json.Unmarshal(data, &payload) != nil || connection.Protocol == ProtocolGeminiText && payload.Models == nil || connection.Protocol == ProtocolAnthropic && payload.Data == nil {
			return nil, failure("invalid_models", "接口未返回兼容的模型列表，可手动填写模型 ID", http.StatusBadGateway)
		}
		add := func(id, name string) {
			if validValue(id, 200) && validateModel(connection.Protocol, id) == nil && !seen[id] && len(models) < 1000 {
				seen[id] = true
				models = append(models, Model{ID: id, Name: truncate(name, 100)})
			}
		}
		next = ""
		if connection.Protocol == ProtocolGeminiText {
			for _, model := range payload.Models {
				if len(model.Methods) == 0 || slices.Contains(model.Methods, "generateContent") {
					add(strings.TrimPrefix(model.Name, "models/"), model.DisplayName)
				}
			}
			next = payload.NextPageToken
		} else {
			for _, model := range payload.Data {
				add(model.ID, model.Name)
			}
			if payload.HasMore {
				next = payload.LastID
			}
		}
		if next == "" || pages[next] {
			break
		}
		pages[next] = true
	}
	if len(models) == 0 {
		return nil, failure("no_models", "该 Key 暂无可用文本模型，可核对权限或手动填写模型 ID", http.StatusBadGateway)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

type nativeTextStream struct {
	protocol          string
	result            *Result
	emit              func(string) error
	emitThinking      func(string) error
	finishThinking    func() error
	updateUsage       func(json.RawMessage) error
	done              bool
	responseThoughts  map[responseThoughtKey]*strings.Builder
	responseBodies    map[[2]int]*strings.Builder
	lastThought       *responseThoughtKey
	anthropicThoughts map[int]bool
}

func (s *nativeTextStream) supported() bool {
	return s.protocol == ProtocolResponses || s.protocol == ProtocolAzureResponses || s.protocol == ProtocolAnthropic || s.protocol == ProtocolGeminiText
}

func invalidTextStream() error {
	return failure("invalid_stream", "服务商返回了无法解析的数据，已保留收到的正文", http.StatusBadGateway)
}
func refusedText() error {
	return failure("provider_refusal", "模型未能完成这次写作，请调整需求后重试；已收到的内容已保留", http.StatusBadGateway)
}
func interruptedText() error {
	return failure("provider_stream_error", "生成被服务商中断，已保留收到的正文，请检查额度或稍后重试", http.StatusBadGateway)
}

func (s *nativeTextStream) write(text string) error {
	return s.emit(text)
}

func (s *nativeTextStream) think(text string) error {
	if s.emitThinking != nil {
		return s.emitThinking(text)
	}
	return nil
}

func (s *nativeTextStream) thoughtDone() error {
	if s.finishThinking != nil {
		return s.finishThinking()
	}
	return nil
}

func (s *nativeTextStream) consume(data []byte) error {
	var envelope struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return invalidTextStream()
	}
	if envelope.Type == "error" || len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		return interruptedText()
	}
	switch s.protocol {
	case ProtocolAnthropic:
		return s.anthropic(data, envelope.Type)
	case ProtocolGeminiText:
		return s.gemini(data)
	default:
		return s.responses(data, envelope.Type)
	}
}

type responseTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responseTextItem struct {
	Type    string             `json:"type"`
	Content []responseTextPart `json:"content"`
	Summary []responseTextPart `json:"summary"`
}

type responseText struct {
	Status     string             `json:"status"`
	Output     []responseTextItem `json:"output"`
	Usage      json.RawMessage    `json:"usage"`
	Incomplete struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type responseThoughtKey struct {
	output, part int
	kind         string
}

// Responses repeats text in done events and the final response. Reconcile each
// public part independently so summaries are neither repeated nor lost when a
// provider sends only a final snapshot.
func (s *nativeTextStream) responseThought(key responseThoughtKey, text string, snapshot bool) error {
	if text == "" {
		return nil
	}
	if s.responseThoughts == nil {
		s.responseThoughts = make(map[responseThoughtKey]*strings.Builder)
	}
	seen := s.responseThoughts[key]
	if seen == nil {
		seen = &strings.Builder{}
		s.responseThoughts[key] = seen
	}
	if snapshot {
		if !strings.HasPrefix(text, seen.String()) {
			return nil
		}
		text = text[seen.Len():]
	}
	if text == "" {
		return nil
	}
	seen.WriteString(text)
	if s.lastThought != nil && *s.lastThought != key {
		if err := s.think("\n\n"); err != nil {
			return err
		}
	}
	s.lastThought = &key
	return s.think(text)
}

func (s *nativeTextStream) responseThoughtItem(index int, item responseTextItem) error {
	if item.Type != "reasoning" {
		return nil
	}
	for i, part := range item.Summary {
		if part.Type == "summary_text" {
			if err := s.responseThought(responseThoughtKey{index, i, "summary"}, part.Text, true); err != nil {
				return err
			}
		}
	}
	for i, part := range item.Content {
		if part.Type == "reasoning_text" {
			if err := s.responseThought(responseThoughtKey{index, i, "content"}, part.Text, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// Content may be repeated as deltas, part/item snapshots and a final response.
// Reconcile each part independently, including a suffix missed during streaming.
func (s *nativeTextStream) responseBody(output, part int, text string, snapshot bool) error {
	if s.responseBodies == nil {
		s.responseBodies = make(map[[2]int]*strings.Builder)
	}
	key := [2]int{output, part}
	seen := s.responseBodies[key]
	if seen == nil {
		seen = &strings.Builder{}
		s.responseBodies[key] = seen
	}
	if snapshot {
		if !strings.HasPrefix(text, seen.String()) {
			return nil
		}
		text = text[seen.Len():]
	}
	if err := s.write(text); err != nil {
		return err
	}
	seen.WriteString(text)
	return nil
}

func (s *nativeTextStream) responseBodyItem(index int, item responseTextItem) error {
	if item.Type != "message" {
		return nil
	}
	for i, part := range item.Content {
		if part.Type == "refusal" {
			return refusedText()
		}
		if part.Type == "output_text" {
			if err := s.responseBody(index, i, part.Text, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *nativeTextStream) responses(data []byte, kind string) error {
	var event struct {
		Delta        string           `json:"delta"`
		Text         string           `json:"text"`
		OutputIndex  int              `json:"output_index"`
		SummaryIndex int              `json:"summary_index"`
		ContentIndex int              `json:"content_index"`
		Part         responseTextPart `json:"part"`
		Item         responseTextItem `json:"item"`
		Response     responseText     `json:"response"`
	}
	if json.Unmarshal(data, &event) != nil {
		return invalidTextStream()
	}
	switch kind {
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.done", "response.reasoning_text.done":
		key := responseThoughtKey{event.OutputIndex, event.SummaryIndex, "summary"}
		if strings.HasPrefix(kind, "response.reasoning_text.") {
			key.part, key.kind = event.ContentIndex, "content"
		}
		if strings.HasSuffix(kind, ".delta") {
			return s.responseThought(key, event.Delta, false)
		}
		if err := s.responseThought(key, event.Text, true); err != nil {
			return err
		}
		return s.thoughtDone()
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		if event.Part.Type == "summary_text" {
			return s.responseThought(responseThoughtKey{event.OutputIndex, event.SummaryIndex, "summary"}, event.Part.Text, true)
		}
	case "response.output_item.added", "response.output_item.done":
		if err := s.responseThoughtItem(event.OutputIndex, event.Item); err != nil {
			return err
		}
		return s.responseBodyItem(event.OutputIndex, event.Item)
	case "response.output_text.delta":
		return s.responseBody(event.OutputIndex, event.ContentIndex, event.Delta, false)
	case "response.output_text.done":
		return s.responseBody(event.OutputIndex, event.ContentIndex, event.Text, true)
	case "response.content_part.added", "response.content_part.done":
		if event.Part.Type == "output_text" {
			return s.responseBody(event.OutputIndex, event.ContentIndex, event.Part.Text, true)
		}
		if event.Part.Type == "refusal" {
			return refusedText()
		}
	case "response.refusal.delta", "response.refusal.done":
		return refusedText()
	case "response.failed":
		if err := s.updateUsage(event.Response.Usage); err != nil {
			return err
		}
		return interruptedText()
	case "response.completed", "response.incomplete", "":
		response := event.Response
		if kind == "" && json.Unmarshal(data, &response) != nil {
			return invalidTextStream()
		}
		if err := s.updateUsage(response.Usage); err != nil {
			return err
		}
		if response.Status == "failed" || response.Status == "cancelled" {
			return interruptedText()
		}
		if response.Status != "completed" && response.Status != "incomplete" {
			return invalidTextStream()
		}
		for i, item := range response.Output {
			if err := s.responseThoughtItem(i, item); err != nil {
				return err
			}
			if err := s.responseBodyItem(i, item); err != nil {
				return err
			}
		}
		if response.Status == "incomplete" && response.Incomplete.Reason != "max_output_tokens" {
			return refusedText()
		}
		s.result.FinishReason = "stop"
		if response.Status == "incomplete" {
			s.result.FinishReason = "length"
		}
		s.done = true
	}
	return nil
}

func (s *nativeTextStream) anthropic(data []byte, kind string) error {
	type block struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	}
	var event struct {
		Index int `json:"index"`
		Delta struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			Thinking   string `json:"thinking"`
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		ContentBlock block           `json:"content_block"`
		Content      []block         `json:"content"`
		StopReason   string          `json:"stop_reason"`
		Usage        json.RawMessage `json:"usage"`
		Message      struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &event) != nil {
		return invalidTextStream()
	}
	stop := func(reason string) error {
		switch reason {
		case "refusal":
			return refusedText()
		case "end_turn", "stop_sequence":
			s.result.FinishReason = "stop"
		case "max_tokens":
			s.result.FinishReason = "length"
		default:
			s.result.FinishReason = reason
		}
		return nil
	}
	switch kind {
	case "message_start":
		return s.updateUsage(event.Message.Usage)
	case "content_block_start":
		if event.ContentBlock.Type == "thinking" {
			if s.anthropicThoughts == nil {
				s.anthropicThoughts = make(map[int]bool)
			}
			s.anthropicThoughts[event.Index] = true
			return s.think(event.ContentBlock.Thinking)
		}
		if event.ContentBlock.Type == "text" {
			return s.write(event.ContentBlock.Text)
		}
	case "content_block_delta":
		if event.Delta.Type == "thinking_delta" {
			if s.anthropicThoughts == nil {
				s.anthropicThoughts = make(map[int]bool)
			}
			s.anthropicThoughts[event.Index] = true
			return s.think(event.Delta.Thinking)
		}
		if event.Delta.Type == "text_delta" {
			return s.write(event.Delta.Text)
		}
	case "content_block_stop":
		if s.anthropicThoughts[event.Index] {
			delete(s.anthropicThoughts, event.Index)
			return s.thoughtDone()
		}
	case "message_delta":
		if err := s.updateUsage(event.Usage); err != nil {
			return err
		}
		return stop(event.Delta.StopReason)
	case "message_stop":
		if s.result.FinishReason == "" {
			return invalidTextStream()
		}
		s.done = true
	case "message":
		if err := s.updateUsage(event.Usage); err != nil {
			return err
		}
		if err := stop(event.StopReason); err != nil {
			return err
		}
		for _, part := range event.Content {
			if part.Type == "thinking" {
				if err := s.think(part.Thinking); err != nil {
					return err
				}
			}
			if part.Type == "text" {
				if err := s.write(part.Text); err != nil {
					return err
				}
			}
		}
		s.done = s.result.FinishReason != ""
	}
	return nil
}

func (s *nativeTextStream) gemini(data []byte) error {
	var event struct {
		Candidates []struct {
			Index   int `json:"index"`
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		Usage json.RawMessage `json:"usageMetadata"`
	}
	if json.Unmarshal(data, &event) != nil {
		return invalidTextStream()
	}
	if err := s.updateUsage(event.Usage); err != nil {
		return err
	}
	if s.done {
		return nil
	}
	if event.PromptFeedback.BlockReason != "" {
		return refusedText()
	}
	for _, candidate := range event.Candidates {
		if candidate.Index != 0 {
			continue
		}
		if candidate.FinishReason != "" && candidate.FinishReason != "STOP" && candidate.FinishReason != "MAX_TOKENS" {
			return refusedText()
		}
		for _, part := range candidate.Content.Parts {
			if part.Thought {
				if err := s.think(part.Text); err != nil {
					return err
				}
			} else {
				if err := s.write(part.Text); err != nil {
					return err
				}
			}
		}
		if candidate.FinishReason != "" {
			s.result.FinishReason = "stop"
			if candidate.FinishReason == "MAX_TOKENS" {
				s.result.FinishReason = "length"
			}
			s.done = true
		}
	}
	return nil
}
