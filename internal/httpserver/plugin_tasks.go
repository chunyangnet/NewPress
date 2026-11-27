package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/pluginjobs"
)

// PluginTaskExecutor uses the same public content projection as synchronous
// plugin calls, but resolves the task owner from current durable account state.
func (s *Server) PluginTaskExecutor() *plugin.TaskExecutor {
	return &plugin.TaskExecutor{Manager: s.plugins, Read: s.pluginReadContent, Authorize: s.authorizePluginTask}
}

func (s *Server) authorizePluginTask(ctx context.Context, job pluginjobs.Job) error {
	if s.auth == nil || s.pluginTasks == nil || job.ActorID <= 0 {
		return pluginjobs.ErrAuthorization
	}
	check := s.pluginTasks.CheckClaim
	if shared, ok := s.pluginTasks.(interface {
		CheckSharedClaim(context.Context, pluginjobs.Job, time.Time) error
	}); ok {
		check = shared.CheckSharedClaim
	}
	if err := check(ctx, job, time.Now().UTC()); err != nil {
		return err
	}
	user, err := s.auth.GetUser(ctx, job.ActorID)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			return pluginjobs.ErrAuthorization
		}
		return err
	}
	if !pluginTaskUserAllowed(user) {
		return pluginjobs.ErrAuthorization
	}
	return nil
}

func (s *Server) SetPluginTaskStore(store pluginjobs.ManagedStore) { s.pluginTasks = store }

func (s *Server) pluginTaskList(w http.ResponseWriter, r *http.Request) {
	_, readOnly, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if readOnly {
		s.writeAPIError(w, r, 403, "plugins.authentication", "请登录后查看插件任务", nil)
		return
	}
	if s.pluginTasks == nil {
		s.writeAPIError(w, r, 503, "plugins.tasks_unavailable", "插件任务暂不可用", nil)
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"status": true, "limit": true, "offset": true, "archived": true})
	if !ok {
		return
	}
	query := pluginjobs.Query{Plugin: r.PathValue("slug"), Status: values.Get("status")}
	switch values.Get("archived") {
	case "", "false":
	case "true":
		query.Archived = true
	default:
		s.pluginTaskError(w, r, pluginjobs.ErrInvalid)
		return
	}
	for key, dest := range map[string]*int{"limit": &query.Limit, "offset": &query.Offset} {
		if value := values.Get(key); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil {
				s.writeAPIError(w, r, 422, "plugins.tasks_query", "任务分页参数无效", nil)
				return
			}
			*dest = n
		}
	}
	page, err := s.pluginTasks.List(r.Context(), query)
	if err != nil {
		s.pluginTaskError(w, r, err)
		return
	}
	writeJSON(w, 200, page)
}

func (s *Server) pluginTaskCreate(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var body struct {
		ExpectedUserID int64           `json:"expected_user_id"`
		Key            string          `json:"key"`
		Payload        json.RawMessage `json:"payload"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 36<<10, "36 KiB"); !ok || !s.pluginIdentity(w, r, principal, body.ExpectedUserID) {
		return
	}
	if s.pluginTasks == nil || s.plugins == nil {
		s.pluginTaskError(w, r, errors.New("unavailable"))
		return
	}
	if allowed, _ := s.accessRate.allow("plugin-task:"+strconv.FormatInt(principal.UserID, 10), 30, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "plugins.rate", "操作过于频繁，请稍后再试", nil)
		return
	}
	slug := r.PathValue("slug")
	manifest, _, _, err := s.plugins.Configuration(slug)
	if err != nil || len(manifest.Hooks["task.execute.v1"]) != 1 {
		s.writeAPIError(w, r, 422, "plugins.task_hook", "插件未声明后台任务接口", nil)
		return
	}
	epoch, err := s.plugins.CurrentTaskEpoch(slug)
	if err != nil {
		if !errors.Is(err, plugin.ErrInactive) {
			s.pluginTaskError(w, r, err)
			return
		}
		s.writeAPIError(w, r, 409, "plugins.task_inactive", "请先启用插件并保存授权", nil)
		return
	}
	if len(body.Payload) == 0 {
		body.Payload = json.RawMessage(`{}`)
	}
	// A lifecycle change after this read invalidates the task at execution.
	// Do not hold the plugin manager lock across PostgreSQL network operations.
	job, err := s.pluginTasks.Enqueue(r.Context(), pluginjobs.Input{Plugin: slug, Epoch: epoch.Token, Kind: "task.execute.v1", Key: body.Key, ActorID: principal.UserID, Payload: body.Payload}, time.Now().UTC())
	if err != nil {
		s.pluginTaskError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, pluginjobs.Summarize(job))
}

func (s *Server) pluginTaskChange(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var body struct {
		ExpectedUserID int64  `json:"expected_user_id"`
		Version        int64  `json:"version"`
		Action         string `json:"action"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 4096, "4 KiB"); !ok || !s.pluginIdentity(w, r, principal, body.ExpectedUserID) {
		return
	}
	if s.pluginTasks == nil {
		s.writeAPIError(w, r, 503, "plugins.tasks_unavailable", "插件任务暂不可用", nil)
		return
	}
	if body.Version < 1 || (body.Action != "retry" && body.Action != "cancel") {
		s.pluginTaskError(w, r, pluginjobs.ErrInvalid)
		return
	}
	if allowed, _ := s.accessRate.allow("plugin-task:"+strconv.FormatInt(principal.UserID, 10), 30, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "plugins.rate", "操作过于频繁，请稍后再试", nil)
		return
	}
	if err := s.pluginTasks.Change(r.Context(), r.PathValue("slug"), r.PathValue("id"), body.Version, body.Action, time.Now().UTC()); err != nil {
		s.pluginTaskError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) pluginTaskError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 503, "plugins.tasks_unavailable", "插件任务暂不可用"
	if errors.Is(err, pluginjobs.ErrInvalid) {
		status, code, message = 422, "plugins.tasks_invalid", "任务参数无效"
	}
	if errors.Is(err, pluginjobs.ErrCapacity) {
		status, code, message = 429, "plugins.tasks_capacity", "任务或历史保留配额已满，当前操作未完成；已有记录仍可查询和处理"
	}
	if errors.Is(err, pluginjobs.ErrConflict) {
		status, code, message = 409, "plugins.tasks_conflict", "任务状态已改变，请刷新后重试"
	}
	if errors.Is(err, pluginjobs.ErrAuthorization) {
		status, code, message = 409, "plugins.tasks_authorization", "插件授权或接口已变化，请重新核对后创建新请求"
	}
	if errors.Is(err, pluginjobs.ErrQuarantined) {
		status, code, message = 503, "plugins.tasks_maintenance", "插件记录已隔离，请由管理员完成离线核对与修复后再接受新的任务"
	}
	s.writeAPIError(w, r, status, code, message, nil)
}

func (s *Server) pluginTaskArchive(w http.ResponseWriter, r *http.Request) {
	s.archivePluginHistory(w, r, false)
}

func (s *Server) pluginFanoutArchive(w http.ResponseWriter, r *http.Request) {
	s.archivePluginHistory(w, r, true)
}

func (s *Server) archivePluginHistory(w http.ResponseWriter, r *http.Request, fanouts bool) {
	principal, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var body struct {
		ExpectedUserID int64     `json:"expected_user_id"`
		Before         time.Time `json:"before"`
		Limit          int       `json:"limit"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 4096, "4 KiB"); !ok || !s.pluginIdentity(w, r, principal, body.ExpectedUserID) {
		return
	}
	store, ok := s.pluginTasks.(pluginjobs.ArchiveStore)
	if !ok {
		s.writeAPIError(w, r, 503, "plugins.tasks_unavailable", "任务归档暂不可用", nil)
		return
	}
	now := time.Now().UTC()
	if allowed, _ := s.accessRate.allow("plugin-task-archive:"+strconv.FormatInt(principal.UserID, 10), 10, now); !allowed {
		s.writeAPIError(w, r, 429, "plugins.rate", "操作过于频繁，请稍后再试", nil)
		return
	}
	query := pluginjobs.ArchiveQuery{Before: body.Before, Limit: body.Limit, Tasks: !fanouts, Fanouts: fanouts}
	if !fanouts {
		query.Plugin = r.PathValue("slug")
	}
	result, err := store.Archive(r.Context(), query, now)
	if err != nil {
		s.pluginTaskError(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}

func pluginTaskUserAllowed(user auth.User) bool {
	return user.ID > 0 && user.Status == auth.UserStatusActive &&
		user.Can(auth.CapabilityAdminAccess) && user.Can(auth.CapabilityPluginsManage)
}
