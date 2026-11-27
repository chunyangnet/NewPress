package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aiworkbench"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func (s *Server) SetAIReports(store *aiworkbench.Store) {
	if store != nil {
		s.aiReports = store
	}
}

func (s *Server) registerAIWorkbenchRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/ai/{$}", s.adminAIWorkbenchPage)
	routes := []struct {
		path, methods  string
		get, post, del http.HandlerFunc
	}{
		{"/api/v1/admin/ai/workbench", "GET", s.adminAIWorkbench, nil, nil},
		{"/api/v1/admin/ai/workbench/articles", "GET", s.adminAIWorkbenchArticles, nil, nil},
		{"/api/v1/admin/ai/workbench/preview", "POST", nil, s.adminAIWorkbenchPreview, nil},
		{"/api/v1/admin/ai/workbench/generate", "POST", nil, s.adminAIWorkbenchGenerate, nil},
		{"/api/v1/admin/ai/workbench/reports", "GET", s.adminAIWorkbenchReports, nil, nil},
		{"/api/v1/admin/ai/workbench/reports/{id}", "GET, DELETE", s.adminAIWorkbenchReport, nil, s.adminAIWorkbenchDelete},
	}
	for _, route := range routes {
		for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			handler := s.adminAPIMethodNotAllowed(route.methods)
			switch method {
			case "GET":
				if route.get != nil {
					handler = route.get
				}
			case "POST":
				if route.post != nil {
					handler = route.post
				}
			case "DELETE":
				if route.del != nil {
					handler = route.del
				}
			}
			mux.HandleFunc(method+" "+route.path, handler)
		}
	}
}

func (s *Server) adminAIWorkbenchPage(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	data := s.baseViewFor(w, "AI 工作台", "站点运维、运营简报与内容分析", "/admin/ai/")
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-ai-workbench"
	data.Admin = s.adminView("ai-workbench", p, readOnly)
	s.render(w, http.StatusOK, "admin-ai-workbench", data)
}

func (s *Server) adminAIWorkbench(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySystemRead); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	type provider struct {
		ID           string   `json:"id"`
		Name         string   `json:"name"`
		Models       []string `json:"models"`
		DefaultModel string   `json:"default_model"`
	}
	settings := s.aiSettings.Snapshot()
	providers := []provider{}
	for _, p := range settings.Providers {
		if _, err := s.aiSettings.Resolve(aieditor.Connection{ProviderID: p.ID}, true); err == nil {
			providers = append(providers, provider{p.ID, p.Name, p.Models, p.DefaultModel})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"tasks": aiworkbench.Tasks(), "providers": providers, "default_provider": settings.DefaultProvider, "timezone": s.siteLocation(r.Context()).String(), "retention_days": aiworkbench.RetentionDays, "max_reports": aiworkbench.MaxReports})
}

func (s *Server) adminAIWorkbenchArticles(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySystemRead); !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"search": true})
	if !ok {
		return
	}
	if utf8.RuneCountInString(q.Get("search")) > 100 {
		s.writeAPIError(w, r, 400, "ai.invalid_search", "搜索词最多 100 字", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := s.repository.SearchPublished(ctx, content.PublishedQuery{Search: q.Get("search"), Limit: 20})
	if err != nil {
		s.writeAPIError(w, r, 503, "ai.articles_unavailable", "公开文章暂时无法读取", nil)
		return
	}
	items := []map[string]any{}
	for _, p := range result.Items {
		if content.PublicAt(p, time.Now()) {
			items = append(items, map[string]any{"id": p.ID, "title": p.Title})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"items": items, "total": result.Total})
}

func (s *Server) adminAIWorkbenchPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input aiworkbench.Request
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 64<<10, "64 KiB"); !ok {
		return
	}
	if err := input.Validate(); err != nil {
		s.writeAPIError(w, r, 400, "ai.invalid_task", err.Error(), nil)
		return
	}
	preview, err := s.workbenchPreview(r.Context(), input)
	if err == nil {
		preview, err = s.aiPreviews.Put(p.UserID, preview, time.Now())
	}
	if err != nil {
		s.writeAPIError(w, r, 400, "ai.preview_unavailable", err.Error(), nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, preview)
}

func (s *Server) adminAIWorkbenchReports(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"reports": s.aiReports.List(p.UserID, time.Now())})
}

func (s *Server) adminAIWorkbenchReport(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	report, err := s.aiReports.Get(p.UserID, r.PathValue("id"), time.Now())
	if err != nil {
		s.writeAPIError(w, r, 404, "ai.report_not_found", aiworkbench.ErrNotFound.Error(), nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, report)
}

func (s *Server) adminAIWorkbenchDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	err := s.aiReports.Delete(p.UserID, r.PathValue("id"), time.Now())
	if errors.Is(err, aiworkbench.ErrNotFound) {
		s.writeAPIError(w, r, 404, "ai.report_not_found", err.Error(), nil)
		return
	}
	if err != nil {
		s.writeAPIError(w, r, 500, "ai.report_write", "报告删除失败，请重试", nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) adminAIWorkbenchGenerate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		Token      string `json:"token"`
		ProviderID string `json:"provider_id"`
		Model      string `json:"model"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 2048, "2 KiB"); !ok {
		return
	}
	if len(input.Token) != 32 || input.ProviderID == "" {
		s.writeAPIError(w, r, 400, "ai.invalid_preview", "请先预览资料并选择已配置的文本接口", nil)
		return
	}
	connection, err := s.aiSettings.Resolve(aieditor.Connection{ProviderID: input.ProviderID, Model: input.Model}, true)
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	if !s.aiState.acquire(p.UserID) {
		w.Header().Set("Retry-After", "10")
		s.writeAPIError(w, r, 429, "ai.busy", "已有 AI 任务正在生成，请先停止或稍后重试", nil)
		return
	}
	preview, err := s.aiPreviews.Take(p.UserID, input.Token, time.Now())
	if err != nil {
		s.aiState.release(p.UserID)
		s.writeAPIError(w, r, 409, "ai.preview_expired", err.Error(), nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), aieditor.GenerationTimeout)
	defer cancel()
	controller := http.NewResponseController(w)
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
		s.aiState.release(p.UserID)
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
		defer s.aiState.release(p.UserID)
		observed := s.observeAI(input.ProviderID, connection, "text", p.UserID)
		observed.record.Purpose = preview.Task
		defer observed.finish(ctx)
		system, user := preview.Prompt()
		bytes := 0
		result, err := s.aiClient.StreamWithCallbacks(ctx, connection, []aieditor.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, 4096, aieditor.StreamCallbacks{
			Delta: func(text string) error {
				bytes += len(text)
				if bytes > aiworkbench.MaxReportBytes {
					return errors.New("report output limit")
				}
				if text != "" {
					observed.first()
				}
				return send(aiStreamEvent{"delta", map[string]string{"text": text}})
			},
			Thinking: func(aieditor.ThinkingDelta) error { observed.first(); return nil },
			Usage: func(usage aieditor.Usage) error {
				observed.record.Usage = usage
				return send(aiStreamEvent{"usage", usage})
			},
		})
		observed.record.Usage = result.Usage
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			observed.fail(err)
			message := "生成中断，已收到的文字可复制或导出；请重新预览后重试。"
			var known *aieditor.Error
			if errors.As(err, &known) {
				message = known.Message
			}
			if errors.Is(err, context.DeadlineExceeded) {
				message = "生成超过 6 分钟，已停止。"
			}
			_ = send(aiStreamEvent{"error", map[string]string{"message": message}})
			return
		}
		if strings.TrimSpace(result.Text) == "" || len(result.Text) > aiworkbench.MaxReportBytes || result.FinishReason != "" && result.FinishReason != "stop" {
			observed.record.ErrorCode = "incomplete_report"
			_ = send(aiStreamEvent{"error", map[string]string{"message": "模型未返回完整报告，已保留收到的文字，请缩短材料后重试。"}})
			return
		}
		observed.record.Status, observed.record.ErrorCode = "success", ""
		name, _ := s.aiSettings.Accounting(input.ProviderID, connection.Model)
		preview.Token = ""
		report := aiworkbench.Report{ID: aiworkbench.NewID(), Owner: p.UserID, Task: preview.Task, Title: preview.Title, CreatedAt: time.Now().UTC(), Provider: name, Model: connection.Model, Content: result.Text, Source: &preview}
		if err := s.aiReports.Save(report, time.Now()); err != nil {
			_ = send(aiStreamEvent{"done", map[string]any{"report": report, "saved": false, "warning": "报告已生成，但保存失败。请复制或导出后重试。", "usage": result.Usage}})
			return
		}
		_ = send(aiStreamEvent{"done", map[string]any{"report": report, "saved": true, "usage": result.Usage}})
	}()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			if r.Context().Err() == nil {
				writeEvent(aiStreamEvent{"error", map[string]string{"message": "生成超时，请重新预览后重试。"}})
			}
			return
		case <-heartbeat.C:
			if !writeEvent(aiStreamEvent{"ping", struct{}{}}) {
				return
			}
		case event := <-events:
			if !writeEvent(event) || event.Name == "done" || event.Name == "error" {
				return
			}
		}
	}
}
