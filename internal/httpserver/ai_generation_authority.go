package httpserver

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"newpress.local/newpress/internal/auth"
)

var errAIGenerationInactive = errors.New("AI generation module lease inactive")

func (s *Server) aiGenerationAccess(r *http.Request, module context.Context) error {
	p, err := s.contentPrincipal(r, true, false)
	if err != nil {
		return err
	}
	if !s.sameOriginRequest(r) {
		return errAIEditorToolOrigin
	}
	tokens := r.Header.Values("X-CSRF-Token")
	if len(tokens) != 1 || !s.auth.CheckCSRF(p, tokens[0]) {
		return errAIEditorToolCSRF
	}
	if module.Err() != nil {
		return errAIGenerationInactive
	}
	return r.Context().Err()
}

func (s *Server) recheckAIGeneration(w http.ResponseWriter, r *http.Request, module context.Context) bool {
	if err := s.aiGenerationAccess(r, module); err != nil {
		s.aiGenerationError(w, r, err)
		return false
	}
	return true
}

func (s *Server) aiGenerationError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	if errors.Is(err, errAIGenerationInactive) {
		s.writeAPIError(w, r, http.StatusConflict, "ai.inactive", "AI 模块已停用，生成已中止", nil)
		return
	}
	s.aiEditorToolError(w, r, err)
}

// The producer and HTTP writer share one throttled check. A denial cancels the
// upstream request and stays terminal even if the session or module recovers.
type aiGenerationGuard struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelCauseFunc
	access    func() error
	checkedAt time.Time
}

func (g *aiGenerationGuard) check(force bool) error {
	if err := context.Cause(g.ctx); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := context.Cause(g.ctx); err != nil {
		return err
	}
	if !force && !g.checkedAt.IsZero() && time.Since(g.checkedAt) < time.Second {
		return nil
	}
	err := g.access()
	g.checkedAt = time.Now()
	if cause := context.Cause(g.ctx); cause != nil {
		return cause
	}
	if err != nil {
		g.cancel(err)
	}
	return err
}

// After SSE headers, send only a generic terminal error: never model output,
// partial usage, provider diagnostics or session-store details after denial.
func aiGenerationStopEvent(err error) aiStreamEvent {
	code, message := "ai.interrupted", "生成已中止，请重新发起请求"
	switch {
	case errors.Is(err, errAIWorkbenchSourceChanged):
		code, message = "ai.source_changed", errAIWorkbenchSourceChanged.Error()
	case errors.Is(err, errAIWorkbenchSourceUnavailable):
		code, message = "ai.source_unavailable", errAIWorkbenchSourceUnavailable.Error()
	case errors.Is(err, auth.ErrSessionNotFound):
		code, message = "auth.authentication_required", "登录已失效，请重新登录后生成"
	case errors.Is(err, auth.ErrSessionForbidden), errors.Is(err, errContentWriteDisabled):
		code, message = "auth.capability_required", "当前账号已无生成权限"
	case errors.Is(err, auth.ErrSessionUnavailable):
		code, message = "auth.session_unavailable", "暂时无法确认登录权限，请稍后重试"
	case errors.Is(err, errAIGenerationInactive), errors.Is(err, errAIAdminInactive):
		code, message = "ai.inactive", "AI 模块已停用，生成已中止"
	case errors.Is(err, errAIEditorToolOrigin), errors.Is(err, errAIEditorToolCSRF):
		code, message = "auth.request_changed", "请求校验已失效，请从本站编辑器重试"
	case errors.Is(err, context.DeadlineExceeded):
		code, message = "ai.timeout", "生成超时，已停止并保留收到的正文"
	}
	return aiStreamEvent{"error", map[string]string{"code": code, "message": message}}
}
