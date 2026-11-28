package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"newpress.local/newpress/internal/auth"
)

func (s *Server) newSystemStatusAuthority(w http.ResponseWriter, r *http.Request) (*aiAdminAuthority, bool) {
	// Core diagnostics are independent of optional module switches. Reuse the
	// original-session predicate, waiting guard and private output semantics.
	access := &aiAdminAuthority{server: s, request: r, module: context.Background(), capabilities: []string{auth.CapabilitySystemRead}}
	return access, access.recheck(w)
}

func (s *Server) authorizedSystemStatus(access *aiAdminAuthority) (systemStatusView, error) {
	var report systemStatusView
	var reportErr error
	err := access.wait(func(ctx context.Context, _ func() error) error {
		report, reportErr = s.systemStatusReportChecked(ctx)
		return nil
	})
	if err != nil {
		return systemStatusView{}, err
	}
	return report, reportErr
}

func (s *Server) systemStatusError(w http.ResponseWriter, r *http.Request, access *aiAdminAuthority, err error) {
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, errSystemStatusCollection) {
		access.fail(w, err)
		return
	}
	if !access.recheck(w) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		s.writeAPIError(w, r, http.StatusServiceUnavailable, "system_status.unavailable", "系统状态暂时无法采集，请稍后重试", nil)
	} else {
		s.renderError(w, http.StatusServiceUnavailable, "系统状态暂时无法采集，请稍后重试")
	}
}
