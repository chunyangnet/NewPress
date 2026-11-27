package httpserver

import (
	"net/http"
	"strconv"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/cdn"
)

func (s *Server) cdnRoutingStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cdnRead(w, r); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, map[string]bool{}); !ok {
		return
	}
	state, e := s.cdn.RoutingStatus()
	if e != nil {
		s.cdnError(w, r, e)
		return
	}
	writeJSON(w, 200, state)
}
func (s *Server) cdnRoutingSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var body struct {
		Config         cdn.RoutingConfig `json:"config"`
		ClearTokens    []string          `json:"clear_tokens"`
		CDNVersion     int64             `json:"cdn_version"`
		ExpectedUserID int64             `json:"expected_user_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 256<<10, "256 KiB"); !ok || !s.pluginIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	if s.cdn == nil {
		s.writeAPIError(w, r, 503, "cdn.unavailable", "CDN 服务暂不可用", nil)
		return
	}
	state, e := s.cdn.SaveRouting(body.Config, body.ClearTokens, body.CDNVersion)
	if e != nil {
		s.cdnError(w, r, e)
		return
	}
	writeJSON(w, 200, state)
}
func (s *Server) cdnRoutingAction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var body struct {
		cdn.RoutingRequest
		ExpectedUserID int64 `json:"expected_user_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 16<<10, "16 KiB"); !ok || !s.pluginIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	if s.cdn == nil {
		s.writeAPIError(w, r, 503, "cdn.unavailable", "CDN 服务暂不可用", nil)
		return
	}
	if allowed, _ := s.accessRate.allow("cdn-routing:"+strconv.FormatInt(p.UserID, 10), 20, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "cdn.rate", "操作过于频繁，请稍后重试", nil)
		return
	}
	task, e := s.cdn.EnqueueRouting(body.RoutingRequest, p.Username)
	if e != nil {
		s.cdnError(w, r, e)
		return
	}
	writeJSON(w, 202, map[string]any{"task": task})
}
