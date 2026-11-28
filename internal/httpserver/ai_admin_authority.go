package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"newpress.local/newpress/internal/auth"
)

var (
	errAIAdminInactive    = errors.New("AI administration module lease inactive")
	errAIEndpointApproval = errors.New("AI endpoint approval permission required")
)

// Each endpoint keeps its existing capability alternatives. In particular,
// settings managers need not have content-write rights to discover models.
type aiAdminAuthority struct {
	server          *Server
	request         *http.Request
	module          context.Context
	moduleID        string
	write           bool
	capabilities    []string
	approveEndpoint bool
	sourceCheck     func(context.Context) error
}

func (s *Server) newAIAdminAuthority(w http.ResponseWriter, r *http.Request, write bool, capabilities ...string) (*aiAdminAuthority, bool) {
	return s.newAIAdminModuleAuthority(w, r, "ai", write, capabilities...)
}

func (s *Server) newAIAdminModuleAuthority(w http.ResponseWriter, r *http.Request, moduleID string, write bool, capabilities ...string) (*aiAdminAuthority, bool) {
	module, enabled := s.siteSettings.ModuleContext(moduleID)
	a := &aiAdminAuthority{server: s, request: r, module: module, moduleID: moduleID, write: write, capabilities: capabilities}
	if !enabled {
		a.fail(w, errAIAdminInactive)
		return nil, false
	}
	return a, a.recheck(w)
}

func (a *aiAdminAuthority) access() error {
	s, r := a.server, a.request
	if err := r.Context().Err(); err != nil {
		return err
	}
	// Repository checks may block. Refresh the original session afterwards,
	// including when the source check fails, so revocation keeps precedence.
	var sourceErr error
	if a.sourceCheck != nil {
		sourceErr = a.sourceCheck(r.Context())
	}
	if !s.auth.Configured() {
		if !s.isLoopbackRequest(r) {
			return auth.ErrSessionNotFound
		}
		if a.write {
			return errContentWriteDisabled
		}
	} else {
		p, err := s.originalSessionPrincipal(r.Context(), r)
		if err != nil {
			return err
		}
		if p.Username == "" {
			return auth.ErrSessionNotFound
		}
		if !p.Can(auth.CapabilityAdminAccess) || !principalCanAny(p, a.capabilities...) {
			return auth.ErrSessionForbidden
		}
		if a.approveEndpoint && !p.Can(auth.CapabilitySettingsManage) {
			return errAIEndpointApproval
		}
		if a.write {
			if !s.sameOriginRequest(r) {
				return errAIEditorToolOrigin
			}
			tokens := r.Header.Values("X-CSRF-Token")
			if len(tokens) != 1 || !s.auth.CheckCSRF(p, tokens[0]) {
				return errAIEditorToolCSRF
			}
		}
	}
	if a.module.Err() != nil {
		return errAIAdminInactive
	}
	if err := r.Context().Err(); err != nil {
		return err
	}
	return sourceErr
}

func (a *aiAdminAuthority) fail(w http.ResponseWriter, err error) {
	if a.request.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	w.Header().Set("Cache-Control", "no-store")
	for _, name := range []string{"Content-Length", "ETag", "Last-Modified", "Location"} {
		w.Header().Del(name)
	}
	if !strings.HasPrefix(a.request.URL.Path, "/api/") {
		switch {
		case errors.Is(err, errAIWorkbenchSourceChanged):
			a.server.renderError(w, http.StatusConflict, "来源文章已变化，请刷新页面")
		case errors.Is(err, errAIWorkbenchSourceUnavailable):
			a.server.renderError(w, http.StatusServiceUnavailable, "暂时无法确认来源文章，请稍后重试")
		case errors.Is(err, errAIAdminInactive):
			message := "AI 模块已停用，请重新打开页面"
			if a.moduleID == "analytics" {
				message = "统计模块已停用，请重新打开页面"
			}
			a.server.renderError(w, http.StatusConflict, message)
		case errors.Is(err, auth.ErrSessionForbidden):
			a.server.renderError(w, http.StatusForbidden, "当前账号没有访问此页面的权限")
		default:
			a.server.editorAccessError(w, a.request, err)
		}
		return
	}
	switch {
	case errors.Is(err, errAIWorkbenchSourceChanged):
		code, message := "ai.source_changed", errAIWorkbenchSourceChanged.Error()
		if a.moduleID == "analytics" {
			code, message = "analytics.source_changed", "来源文章已变化，请重新读取统计"
		}
		a.server.writeAPIError(w, a.request, http.StatusConflict, code, message, nil)
	case errors.Is(err, errAIWorkbenchSourceUnavailable):
		code, message := "ai.source_unavailable", errAIWorkbenchSourceUnavailable.Error()
		if a.moduleID == "analytics" {
			code, message = "analytics.source_unavailable", "暂时无法确认来源文章，请稍后重试"
		}
		a.server.writeAPIError(w, a.request, http.StatusServiceUnavailable, code, message, nil)
	case errors.Is(err, errAIAdminInactive):
		code, message := "ai.inactive", "AI 模块已停用，操作已中止"
		if a.moduleID == "analytics" {
			code, message = "analytics.inactive", "统计模块已停用，操作已中止"
		}
		a.server.writeAPIError(w, a.request, http.StatusConflict, code, message, nil)
	case errors.Is(err, errAIEndpointApproval):
		a.server.writeAPIError(w, a.request, http.StatusForbidden, "ai.endpoint_permission", "只有管理员可以授权 HTTP 或本地接口", nil)
	default:
		a.server.aiEditorToolError(w, a.request, err)
	}
}

func (a *aiAdminAuthority) recheck(w http.ResponseWriter) bool {
	if err := a.access(); err != nil {
		a.fail(w, err)
		return false
	}
	return true
}

func (a *aiAdminAuthority) json(w http.ResponseWriter, value any) {
	writePrivateJSON(w, a.request, http.StatusOK, value, nil, a.access, func(err error) { a.fail(w, err) })
}
