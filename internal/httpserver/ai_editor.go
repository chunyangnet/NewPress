package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

type aiEditorState struct {
	mu     sync.Mutex
	active map[int64]bool
}

func (s *Server) SetAISettings(settings *aieditor.SettingsStore) {
	if settings != nil {
		s.aiSettings = settings
	}
}

func (s *Server) adminAIConnections(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	connections := s.aiSettings.Snapshot()
	connections.Telemetry = nil
	for i := range connections.Providers {
		connections.Providers[i].Pricing = nil
		connections.Providers[i].Balance = aieditor.BalanceConfig{}
	}
	writeJSON(w, http.StatusOK, connections)
}

func (s *Server) adminAISettings(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.aiSettings.Snapshot())
}

func (s *Server) adminAISettingsSave(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input aieditor.Settings
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 512<<10, "512 KiB"); !ok {
		return
	}
	saved, err := s.aiSettings.Save(input)
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (state *aiEditorState) acquire(user int64) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.active[user] || len(state.active) >= 4 {
		return false
	}
	if state.active == nil {
		state.active = make(map[int64]bool)
	}
	state.active[user] = true
	return true
}

func (state *aiEditorState) release(user int64) {
	state.mu.Lock()
	defer state.mu.Unlock()
	delete(state.active, user)
}

func (s *Server) aiEditorCategories(ctx context.Context) ([]aieditor.Category, error) {
	terms, err := s.repository.ListTerms(ctx, content.TermQuery{Taxonomy: content.TaxonomyCategory, Limit: 500})
	if err != nil {
		return nil, err
	}
	list := make([]aieditor.Category, 0, len(terms.Items))
	for _, term := range terms.Items {
		list = append(list, aieditor.Category{ID: term.ID, Name: term.Name})
	}
	return list, nil
}

func (s *Server) aiEditorError(w http.ResponseWriter, r *http.Request, err error) {
	var provider *aieditor.Error
	if errors.As(err, &provider) {
		s.writeAPIError(w, r, provider.Status, "ai."+provider.Code, provider.Message, nil)
	} else if errors.Is(err, context.DeadlineExceeded) {
		s.writeAPIError(w, r, http.StatusGatewayTimeout, "ai.timeout", "AI 请求超时，请稍后重试", nil)
	} else {
		s.writeAPIError(w, r, http.StatusBadGateway, "ai.unavailable", "AI 请求暂时不可用，请稍后重试", nil)
	}
}

func (s *Server) adminAIPrompts(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompts": aieditor.Prompts()})
}

func (s *Server) adminAIModels(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		aieditor.Connection
		AllowInsecure bool `json:"allow_insecure"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 16<<10, "16 KiB"); !ok {
		return
	}
	// Model discovery can take longer than the normal page write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(25 * time.Second))
	var connection aieditor.Connection
	var err error
	if input.AllowInsecure {
		if !principal.Can(auth.CapabilitySettingsManage) {
			s.writeAPIError(w, r, http.StatusForbidden, "ai.endpoint_permission", "只有管理员可以授权 HTTP 或本地接口", nil)
			return
		}
		connection, err = aieditor.ApproveProbeConnection(input.Connection)
	} else {
		connection, err = s.aiSettings.Resolve(input.Connection, false)
	}
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	models, err := s.aiClient.Models(r.Context(), connection)
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "base_url": connection.BaseURL})
}

func (s *Server) adminAIPrepare(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		Title       string   `json:"title"`
		Slug        string   `json:"slug"`
		Excerpt     string   `json:"excerpt"`
		Tags        []string `json:"tags"`
		CategoryIDs []int64  `json:"category_ids"`
		CoverPrompt string   `json:"cover_prompt"`
		Markdown    string   `json:"markdown"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 1<<20, "1 MiB"); !ok {
		return
	}
	categories, err := s.aiEditorCategories(r.Context())
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	doc, err := aieditor.PrepareDocument(aieditor.Document{Title: input.Title, Slug: input.Slug, Excerpt: input.Excerpt, Tags: input.Tags, CategoryIDs: input.CategoryIDs, CoverPrompt: input.CoverPrompt, Markdown: input.Markdown}, categories)
	if err != nil {
		s.writeAPIError(w, r, http.StatusBadRequest, "ai.invalid_document", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

type aiStreamEvent struct {
	Name string
	Data any
}

func (s *Server) adminAIGenerate(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		aieditor.Connection
		aieditor.Generation
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 256<<10, "256 KiB"); !ok {
		return
	}
	connection, err := s.aiSettings.Resolve(input.Connection, true)
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	categories, err := s.aiEditorCategories(r.Context())
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	messages, err := input.Generation.Messages(categories)
	if err != nil {
		s.writeAPIError(w, r, http.StatusBadRequest, "ai.invalid_brief", err.Error(), nil)
		return
	}
	if !s.aiState.acquire(principal.UserID) {
		w.Header().Set("Retry-After", "10")
		s.writeAPIError(w, r, http.StatusTooManyRequests, "ai.busy", "已有生成任务进行中，请先停止它或稍后重试", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), aieditor.GenerationTimeout)
	defer cancel()
	controller := http.NewResponseController(w)
	// Limit this response only; ordinary requests retain their normal timeout.
	_ = controller.SetWriteDeadline(time.Now().Add(aieditor.GenerationTimeout + 10*time.Second))
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	writeEvent := func(event aiStreamEvent) bool {
		encoded, err := json.Marshal(event.Data)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Name, encoded); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !writeEvent(aiStreamEvent{"ready", map[string]string{"model": connection.Model}}) {
		s.aiState.release(principal.UserID)
		return
	}
	events := make(chan aiStreamEvent, 4)
	send := func(event aiStreamEvent) error {
		select {
		case events <- event:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	go func() {
		observed := s.observeAI(input.ProviderID, connection, "text", principal.UserID)
		var terminal aiStreamEvent
		defer func() {
			observed.finish(ctx)
			s.aiState.release(principal.UserID)
			// A terminal event permits the client to start the next generation.
			// Publish it only after accounting and slot cleanup have finished.
			if terminal.Name != "" {
				_ = send(terminal)
			}
		}()
		result, err := s.aiClient.StreamWithCallbacks(ctx, connection, messages, input.MaxTokens, aieditor.StreamCallbacks{
			Delta: func(text string) error {
				if text != "" {
					observed.first()
				}
				return send(aiStreamEvent{"delta", map[string]string{"text": text}})
			},
			Thinking: func(delta aieditor.ThinkingDelta) error {
				observed.first()
				return send(aiStreamEvent{"thinking", delta})
			},
			ThinkingDone: func() error {
				return send(aiStreamEvent{"thinking_done", struct{}{}})
			},
			Usage: func(usage aieditor.Usage) error {
				observed.record.Usage = usage
				return send(aiStreamEvent{"usage", usage})
			},
		})
		observed.record.Usage = result.Usage
		if err != nil {
			observed.fail(err)
			message := "生成连接中断，已保留收到的正文，可编辑后填入或重新生成"
			var known *aieditor.Error
			if errors.As(err, &known) {
				message = known.Message
			} else if errors.Is(err, context.DeadlineExceeded) {
				message = "生成超过 6 分钟，已停止并保留收到的正文"
			}
			terminal = aiStreamEvent{"error", map[string]any{"message": message, "usage": result.Usage}}
			return
		}
		doc, err := aieditor.ParseDocument(result.Text, categories)
		if err != nil {
			observed.record.ErrorCode = "invalid_document"
			terminal = aiStreamEvent{"error", map[string]any{"message": err.Error(), "usage": result.Usage}}
			return
		}
		complete := result.FinishReason == "stop" || result.FinishReason == ""
		observed.record.Status, observed.record.ErrorCode = "success", ""
		if !complete {
			doc.Warnings = append(doc.Warnings, "模型提前结束，正文可能未完成；可编辑后填入，或增加 Token 上限再生成。")
		}
		terminal = aiStreamEvent{"done", map[string]any{"document": doc, "complete": complete, "usage": result.Usage}}
	}()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			if r.Context().Err() == nil {
				writeEvent(aiStreamEvent{"error", map[string]string{"message": "生成超时，已停止并保留收到的正文"}})
			}
			return
		case <-heartbeat.C:
			if !writeEvent(aiStreamEvent{"ping", map[string]bool{"waiting": true}}) {
				return
			}
		case event := <-events:
			if !writeEvent(event) || event.Name == "done" || event.Name == "error" {
				return
			}
		}
	}
}
