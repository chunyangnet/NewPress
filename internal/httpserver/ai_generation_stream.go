package httpserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
)

func (s *Server) streamAIGeneration(w http.ResponseWriter, r *http.Request, principal auth.Principal, module context.Context, connection aieditor.Connection, provider string, messages []aieditor.Message, categories []aieditor.Category, maxTokens int) {
	if !s.aiState.acquire(principal.UserID) {
		w.Header().Set("Retry-After", "10")
		s.writeAPIError(w, r, http.StatusTooManyRequests, "ai.busy", "已有生成任务进行中，请先停止它或稍后重试", nil)
		return
	}
	started := false
	defer func() {
		if !started {
			s.aiState.release(principal.UserID)
		}
	}()
	deadline, stopDeadline := context.WithTimeout(r.Context(), aieditor.GenerationTimeout)
	defer stopDeadline()
	ctx, cancel := context.WithCancelCause(deadline)
	defer cancel(nil)
	stopModule := context.AfterFunc(module, func() { cancel(errAIGenerationInactive) })
	defer stopModule()
	checkedRequest := r.WithContext(ctx)
	guard := &aiGenerationGuard{ctx: ctx, cancel: cancel, access: func() error { return s.aiGenerationAccess(checkedRequest, module) }}
	if !s.recheckAIGeneration(w, r, module) {
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(aieditor.GenerationTimeout + 10*time.Second))
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	stream := &aiEventStream{writer: w, request: r, controller: controller, guard: guard, firstPayload: true}
	writeEvent, stop := stream.write, stream.stop
	if err := writeEvent(aiStreamEvent{"ready", map[string]string{"model": connection.Model}}); err != nil {
		// No bytes were sent: preserve the JSON status and retry semantics.
		w.Header().Del("X-Accel-Buffering")
		s.aiGenerationError(w, r, err)
		return
	}
	events := make(chan aiStreamEvent, 4)
	started = true
	go func() {
		defer close(events)
		// Hold the slot until the producer has actually stopped, even when
		// the HTTP handler already returned after cancellation or a write error.
		var terminal aiStreamEvent
		var send func(aiStreamEvent) error
		defer func() {
			s.aiState.release(principal.UserID)
			// Accounting finishes before the slot is released and completion is sent.
			if terminal.Name != "" {
				_ = send(terminal)
			}
		}()
		if err := guard.check(true); err != nil {
			return
		}
		observed := s.observeAI(provider, connection, "text", principal.UserID)
		defer observed.finish(ctx)
		firstEvent := true
		send = func(event aiStreamEvent) error {
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
		result, err := s.aiClient.StreamWithCallbacks(ctx, connection, messages, maxTokens, aieditor.StreamCallbacks{
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
			ThinkingDone: func() error { return send(aiStreamEvent{"thinking_done", struct{}{}}) },
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
		if !complete {
			doc.Warnings = append(doc.Warnings, "模型提前结束，正文可能未完成；可编辑后填入，或增加 Token 上限再生成。")
		}
		terminal = aiStreamEvent{"done", map[string]any{"document": doc, "complete": complete, "usage": result.Usage}}
		observed.record.Status, observed.record.ErrorCode = "success", ""
	}()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	authority := time.NewTicker(time.Second)
	defer authority.Stop()
	for {
		select {
		case <-ctx.Done():
			stop(context.Cause(ctx))
			return
		case <-authority.C:
			if err := guard.check(false); err != nil {
				stop(err)
				return
			}
		case <-heartbeat.C:
			if err := writeEvent(aiStreamEvent{"ping", map[string]bool{"waiting": true}}); err != nil {
				stop(err)
				return
			}
		case event, ok := <-events:
			if !ok {
				stop(context.Cause(ctx))
				return
			}
			if err := writeEvent(event); err != nil {
				stop(err)
				return
			}
			if event.Name == "done" || event.Name == "error" {
				return
			}
		}
	}
}
