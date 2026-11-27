package httpserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/pluginjobs"
)

func (s *Server) pluginTaskReview(w http.ResponseWriter, r *http.Request) {
	principal, readOnly, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	if readOnly || principal.UserID < 1 {
		s.writeAPIError(w, r, 403, "plugins.reissue_account", "请使用持久管理员账号登录后核对插件任务", nil)
		return
	}
	if _, ok = s.strictAPIQuery(w, r, map[string]bool{}); !ok {
		return
	}
	store, ok := s.pluginTasks.(pluginjobs.ReviewStore)
	if !ok {
		s.writeAPIError(w, r, 503, "plugins.tasks_unavailable", "任务核对暂不可用", nil)
		return
	}
	details, err := store.GetReview(r.Context(), r.PathValue("slug"), r.PathValue("id"))
	if err != nil {
		s.pluginTaskError(w, r, err)
		return
	}
	_, allowed := s.pluginReissueEpoch(r.PathValue("slug"), details.Task.Kind)
	details.Reissuable = details.Reissuable && allowed
	writeJSON(w, http.StatusOK, struct {
		pluginjobs.ReviewDetails
		CurrentUserID int64 `json:"current_user_id"`
	}{details, principal.UserID})
}

func (s *Server) pluginReissueEpoch(slug, kind string) (string, bool) {
	if s.plugins == nil {
		return "", false
	}
	manifest, _, _, err := s.plugins.Configuration(slug)
	if err != nil || len(manifest.Hooks[kind]) != 1 {
		return "", false
	}
	epoch, err := s.plugins.CurrentTaskEpoch(slug)
	if err != nil {
		return "", false
	}
	return epoch.Token, true
}

func (s *Server) pluginTaskReissue(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	if principal.UserID < 1 {
		s.writeAPIError(w, r, 403, "plugins.reissue_account", "请使用持久管理员账号登录后重新投递插件任务", nil)
		return
	}
	var body struct {
		ExpectedUserID int64           `json:"expected_user_id"`
		SourceVersion  int64           `json:"source_version"`
		Kind           string          `json:"kind"`
		Key            string          `json:"key"`
		Payload        json.RawMessage `json:"payload"`
		Reason         string          `json:"reason"`
		Confirmed      bool            `json:"confirmed"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 36<<10, "36 KiB"); !ok ||
		!s.pluginIdentity(w, r, principal, body.ExpectedUserID) {
		return
	}
	if !body.Confirmed || body.SourceVersion < 1 || len(body.Payload) == 0 ||
		(body.Kind != "task.execute.v1" && body.Kind != "event.deliver.v1") {
		s.writeAPIError(w, r, 422, "plugins.reissue_review", "请核对来源任务、填写新参数与原因，并明确确认重新投递", nil)
		return
	}
	store, ok := s.pluginTasks.(pluginjobs.ReviewStore)
	if !ok {
		s.writeAPIError(w, r, 503, "plugins.tasks_unavailable", "任务重新投递暂不可用", nil)
		return
	}
	if allowed, _ := s.accessRate.allow("plugin-task:"+strconv.FormatInt(principal.UserID, 10), 30, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "plugins.rate", "操作过于频繁，请稍后再试", nil)
		return
	}
	epoch, allowed := s.pluginReissueEpoch(r.PathValue("slug"), body.Kind)
	// Reissue checks its durable command receipt first. An existing acceptance
	// can be confirmed even after this epoch changes or the plugin is disabled;
	// denied authorization can never admit fresh work.
	receipt, err := store.Reissue(r.Context(), pluginjobs.ReissueRequest{
		SourceID: r.PathValue("id"), SourceVersion: body.SourceVersion,
		Input: pluginjobs.Input{Plugin: r.PathValue("slug"), Epoch: epoch, Kind: body.Kind, Key: body.Key,
			ActorID: principal.UserID, Payload: body.Payload},
		Reason: body.Reason, AllowNew: allowed,
	}, time.Now().UTC())
	if err != nil {
		s.pluginTaskError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, receipt)
}
