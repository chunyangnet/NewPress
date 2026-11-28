package httpserver

import (
	"context"
	"errors"
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
	access, ok := s.newAIAdminAuthority(w, r, false, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	data := s.baseViewFor(w, "AI 工作台", "站点运维、运营简报与内容分析", "/admin/ai/")
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-ai-workbench"
	data.Admin = s.adminView("ai-workbench", p, readOnly)
	if !access.recheck(w) {
		return
	}
	s.renderChecked(w, http.StatusOK, "admin-ai-workbench", data, r, func() bool { return access.recheck(w) }, access.access)
}

func (s *Server) adminAIWorkbench(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySystemRead); !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, false, auth.CapabilitySystemRead)
	if !ok {
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
	access.json(w, map[string]any{"tasks": aiworkbench.Tasks(), "providers": providers, "default_provider": settings.DefaultProvider, "timezone": s.siteLocation(r.Context()).String(), "retention_days": aiworkbench.RetentionDays, "max_reports": aiworkbench.MaxReports})
}

func (s *Server) adminAIWorkbenchArticles(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySystemRead); !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, false, auth.CapabilitySystemRead)
	if !ok {
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
	if !access.recheck(w) {
		return
	}
	if err != nil {
		s.writeAPIError(w, r, 503, "ai.articles_unavailable", "公开文章暂时无法读取", nil)
		return
	}
	items := []map[string]any{}
	sources := &workbenchSources{server: s, versions: make(map[int64]int64)}
	for _, p := range result.Items {
		if p.Type == content.TypePost && content.PublicAt(p, time.Now()) {
			items = append(items, map[string]any{"id": p.ID, "title": p.Title})
			sources.add(p.ID, p.Version)
		}
	}
	access.sourceCheck = sources.check
	w.Header().Set("Cache-Control", "no-store")
	access.json(w, map[string]any{"items": items, "total": result.Total})
}

func (s *Server) adminAIWorkbenchPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, true, auth.CapabilitySystemRead)
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
	if !access.recheck(w) {
		return
	}
	if err := input.Validate(); err != nil {
		s.writeAPIError(w, r, 400, "ai.invalid_task", err.Error(), nil)
		return
	}
	var preview aiworkbench.Preview
	var previewErr error
	err := access.wait(func(ctx context.Context, check func() error) error {
		preview, previewErr = s.workbenchPreview(ctx, input)
		if previewErr == nil {
			source := preview
			preview, previewErr = s.aiPreviews.PutChecked(p.UserID, preview, time.Now(), func() error {
				sourceErr := source.CheckSource(ctx)
				if err := check(); err != nil {
					return err
				}
				return sourceErr
			})
		}
		return nil
	})
	if err != nil {
		access.fail(w, err)
		return
	}
	if previewErr != nil {
		if errors.Is(previewErr, errAIWorkbenchSourceChanged) || errors.Is(previewErr, errAIWorkbenchSourceUnavailable) {
			access.fail(w, previewErr)
			return
		}
		s.writeAPIError(w, r, 400, "ai.preview_unavailable", previewErr.Error(), nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	access.sourceCheck = preview.CheckSource
	access.json(w, preview)
}

func (s *Server) adminAIWorkbenchReports(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, false, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	access.json(w, map[string]any{"reports": s.aiReports.List(p.UserID, time.Now())})
}

func (s *Server) adminAIWorkbenchReport(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, false, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	report, err := s.aiReports.Get(p.UserID, r.PathValue("id"), time.Now())
	if !access.recheck(w) {
		return
	}
	if err != nil {
		s.writeAPIError(w, r, 404, "ai.report_not_found", aiworkbench.ErrNotFound.Error(), nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	access.json(w, report)
}

func (s *Server) adminAIWorkbenchDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, true, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var err error
	accessErr := access.wait(func(_ context.Context, check func() error) error {
		err = s.aiReports.DeleteChecked(p.UserID, r.PathValue("id"), time.Now(), check)
		return nil
	})
	if accessErr != nil {
		access.fail(w, accessErr)
		return
	}
	if errors.Is(err, aiworkbench.ErrNotFound) {
		s.writeAPIError(w, r, 404, "ai.report_not_found", err.Error(), nil)
		return
	}
	if err != nil {
		s.writeAPIError(w, r, 500, "ai.report_write", "报告删除失败，请重试", nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	access.json(w, map[string]bool{"deleted": true})
}

func (s *Server) adminAIWorkbenchGenerate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, true, auth.CapabilitySystemRead)
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
	if !access.recheck(w) {
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
	started := false
	defer func() {
		if !started {
			s.aiState.release(p.UserID)
		}
	}()
	deadline, stopDeadline := context.WithTimeout(r.Context(), aieditor.GenerationTimeout)
	defer stopDeadline()
	ctx, cancel := context.WithCancelCause(deadline)
	defer cancel(nil)
	stopModule := context.AfterFunc(access.module, func() { cancel(errAIAdminInactive) })
	defer stopModule()
	checked := *access
	checked.request = r.WithContext(ctx)
	guard := &aiGenerationGuard{ctx: ctx, cancel: cancel, access: checked.access}
	preview, err := s.aiPreviews.TakeChecked(p.UserID, input.Token, time.Now(), func() error { return guard.check(true) })
	if err == nil {
		checked.sourceCheck = preview.CheckSource
	}
	if accessErr := guard.check(true); accessErr != nil {
		access.fail(w, accessErr)
		return
	}
	if err != nil {
		s.writeAPIError(w, r, 409, "ai.preview_expired", err.Error(), nil)
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(aieditor.GenerationTimeout + 10*time.Second))
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	stream := &aiEventStream{writer: w, request: r, controller: controller, guard: guard, firstPayload: true}
	if err := stream.write(aiStreamEvent{"ready", map[string]string{"model": connection.Model}}); err != nil {
		w.Header().Del("X-Accel-Buffering")
		access.fail(w, err)
		return
	}
	events := make(chan aiStreamEvent, 4)
	started = true
	go func() {
		defer close(events)
		defer s.aiState.release(p.UserID)
		if err := guard.check(true); err != nil {
			return
		}
		firstEvent := true
		send := func(event aiStreamEvent) error {
			if err := guard.check(firstEvent || event.Name == "done" || event.Name == "error"); err != nil {
				return err
			}
			firstEvent = false
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case events <- event:
				return nil
			}
		}
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
			Thinking: func(aieditor.ThinkingDelta) error { observed.first(); return guard.check(false) },
			Usage: func(usage aieditor.Usage) error {
				observed.record.Usage = usage
				return send(aiStreamEvent{"usage", usage})
			},
		})
		observed.record.Usage = result.Usage
		if accessErr := guard.check(true); accessErr != nil {
			observed.fail(accessErr)
			return
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
		name, _ := s.aiSettings.Accounting(input.ProviderID, connection.Model)
		preview.Token = ""
		report := aiworkbench.Report{ID: aiworkbench.NewID(), Owner: p.UserID, Task: preview.Task, Title: preview.Title, CreatedAt: time.Now().UTC(), Provider: name, Model: connection.Model, Content: result.Text, Source: &preview}
		saveErr := s.aiReports.SaveChecked(report, time.Now(), func() error { return guard.check(true) })
		if accessErr := guard.check(true); accessErr != nil {
			observed.fail(accessErr)
			return
		}
		data := map[string]any{"report": report, "saved": saveErr == nil, "usage": result.Usage}
		if saveErr != nil {
			data["warning"] = "报告已生成，但保存失败。请复制或导出后重试。"
		}
		if err := send(aiStreamEvent{"done", data}); err != nil {
			observed.fail(err)
			return
		}
		observed.record.Status, observed.record.ErrorCode = "success", ""
	}()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	authority := time.NewTicker(time.Second)
	defer authority.Stop()
	for {
		select {
		case <-ctx.Done():
			stream.stop(context.Cause(ctx))
			return
		case <-authority.C:
			if err := guard.check(false); err != nil {
				stream.stop(err)
				return
			}
		case <-heartbeat.C:
			if err := stream.write(aiStreamEvent{"ping", struct{}{}}); err != nil {
				stream.stop(err)
				return
			}
		case event, ok := <-events:
			if !ok {
				stream.stop(context.Cause(ctx))
				return
			}
			if err := stream.write(event); err != nil {
				stream.stop(err)
				return
			}
			if event.Name == "done" || event.Name == "error" {
				return
			}
		}
	}
}
