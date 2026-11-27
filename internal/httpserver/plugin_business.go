package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/pluginjobs"
)

type pluginFragment struct {
	plugin.Fragment
	Name string
}
type pluginSettingView struct {
	plugin.Setting
	Value   string
	Checked bool
}
type pluginGrantView struct {
	Key, Label string
	Granted    bool
}
type pluginRuntimeView struct {
	Manifest        plugin.Manifest
	Config          plugin.Configuration
	Fields          []pluginSettingView
	Grants          []pluginGrantView
	Status          plugin.ExecutionStatus
	Action          bool
	Tasks           pluginjobs.Page
	TaskEnabled     bool
	EventEnabled    bool
	EventSubscribed bool
	TaskError       string
	Groups          []pluginSettingGroup
}

type pluginSettingGroup struct {
	ID, Title, Icon string
	Fields          []pluginSettingView
}

func publicPluginPost(p content.Post, paths ...contentPathBuilder) any {
	return struct {
		ID        int64  `json:"id"`
		Title     string `json:"title"`
		URL       string `json:"url"`
		Updated   int64  `json:"updated_at"`
		Published int64  `json:"published_at"`
		Excerpt   string `json:"excerpt"`
	}{p.ID, p.Title, resolvedContentPath(p, paths...), p.UpdatedAt.Unix(), p.PublishedAt.Unix(), p.Excerpt}
}
func (s *Server) pluginReadContent(ctx context.Context, id int64) (any, error) {
	p, e := s.repository.GetByID(ctx, id)
	if e != nil || p.Type != content.TypePost || !content.PublicAt(p, time.Now()) {
		return nil, errors.New("公开文章不存在")
	}
	return publicPluginPost(p, s.contentPath), nil
}
func (s *Server) pluginRender(ctx context.Context, p content.Post) []pluginFragment {
	if s.plugins == nil || p.Type != content.TypePost || !content.PublicAt(p, time.Now()) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	items, e := s.plugins.List()
	if e != nil {
		return nil
	}
	out := []pluginFragment{}
	for _, item := range items {
		if !item.Enabled || !item.Official || item.Error != "" || len(item.Manifest.Hooks["content.render"]) == 0 {
			continue
		}
		if len(out) >= 8 || ctx.Err() != nil {
			break
		}
		result, e := s.plugins.Dispatch(ctx, item.Slug, plugin.BusinessInput{Event: "content.render", Post: publicPluginPost(p, s.contentPath)}, s.pluginReadContent)
		if e == nil && result.Fragment != nil {
			out = append(out, pluginFragment{*result.Fragment, item.Manifest.Name})
		}
	}
	return out
}
func (s *Server) adminPluginRuntime(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("slug") == plugin.CommentOrigin {
		s.adminPluginPage(w, r, true)
		return
	}
	if r.PathValue("slug") == plugin.AIProMax {
		s.adminAIProMaxPage(w, r)
		return
	}
	p, ro, ok := s.requireAdminPageCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	manifest, c, status, e := s.plugins.Configuration(r.PathValue("slug"))
	if e != nil {
		s.renderError(w, 404, "此插件没有可配置的业务接口")
		return
	}
	v := pluginRuntimeView{Manifest: manifest, Config: c, Status: status, Action: len(manifest.Hooks["admin.action"]) > 0}
	page, err := plugin.SettingsPageFor(manifest)
	if err != nil {
		s.renderError(w, 422, "插件设置定义无效")
		return
	}
	for _, group := range page.Groups {
		view := pluginSettingGroup{ID: group.ID, Title: group.Title, Icon: group.Icon}
		for _, field := range group.Fields {
			view.Fields = append(view.Fields, pluginSettingView{Setting: field, Value: fmt.Sprint(c.Values[field.Key]), Checked: c.Values[field.Key] == true})
		}
		v.Groups = append(v.Groups, view)
	}
	for _, field := range manifest.Settings {
		v.Fields = append(v.Fields, pluginSettingView{Setting: field, Value: fmt.Sprint(c.Values[field.Key]), Checked: c.Values[field.Key] == true})
	}
	labels := map[string]string{"content.read": "读取公开文章摘要", "settings.read": "读取本插件配置", "settings.write": "保存本插件配置（管理员操作）", "cache": "本插件独立存储（最多 64 KiB）", "render.fragment": "在文章中展示内容", "http.outbound": "访问声明的外部服务", "http.send": "发送 HTTPS Webhook（PG 模式，需开启发送）", "content.write": "后台更新指定内容文字字段（PG 模式）", "jobs.enqueue": "从管理员操作创建后台任务"}
	for _, cap := range manifest.Capabilities {
		v.Grants = append(v.Grants, pluginGrantView{cap, labels[cap], slices.Contains(c.Grants, cap)})
	}
	if len(v.Grants) > 0 {
		v.Groups = append(v.Groups, pluginSettingGroup{ID: "permissions", Title: "权限", Icon: "shield-check"})
	}
	v.Groups = append(v.Groups, pluginSettingGroup{ID: "records", Title: "运行记录", Icon: "activity"})
	if s.pluginTasks != nil && !ro {
		v.TaskEnabled = len(manifest.Hooks["task.execute.v1"]) == 1
		v.EventEnabled = len(manifest.Hooks["event.deliver.v1"]) == 1
		if epoch, epochErr := s.plugins.CurrentTaskEpoch(manifest.Slug); epochErr == nil {
			v.EventSubscribed = v.EventEnabled && epoch.ActorID > 0 && epoch.ActorID == p.UserID
		}
		v.Tasks, err = s.pluginTasks.List(r.Context(), pluginjobs.Query{Plugin: manifest.Slug})
		if err != nil {
			v.TaskError = "任务记录暂时无法读取，请稍后刷新重试。"
		}
		v.Groups = append(v.Groups, pluginSettingGroup{ID: "tasks", Title: "后台任务", Icon: "activity"})
	}
	data := s.baseViewFor(w, manifest.Name+" · 插件配置", "配置官方扩展与权限。", "/admin/plugins/"+manifest.Slug+"/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-plugins"
	data.Admin = s.adminView("plugins", p, ro)
	data.Admin.DetailTitle = manifest.Name
	data.Admin.PluginSlug = manifest.Slug
	data.PluginRuntime = v
	s.render(w, 200, "admin-plugin-runtime", data)
}
func (s *Server) pluginBusinessSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		Configuration   plugin.Configuration `json:"configuration"`
		SubscribeEvents bool                 `json:"subscribe_events"`
		ExpectedUserID  int64                `json:"expected_user_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 64<<10, "64 KiB"); !ok || !s.pluginIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	configure := func() error { return s.plugins.Configure(r.PathValue("slug"), body.Configuration) }
	if body.SubscribeEvents {
		user, err := s.auth.GetUser(r.Context(), p.UserID)
		if err != nil && !errors.Is(err, auth.ErrUserNotFound) && !errors.Is(err, auth.ErrUserStoreNotAvailable) {
			s.writeAPIError(w, r, http.StatusServiceUnavailable, "plugins.subscription_unavailable", "暂时无法核验订阅账户，请稍后重试", nil)
			return
		}
		if err != nil || !pluginTaskUserAllowed(user) {
			s.writeAPIError(w, r, http.StatusForbidden, "plugins.subscription_identity", "事件订阅需要当前有效的持久管理员账户", nil)
			return
		}
		configure = func() error { return s.plugins.ConfigureAs(r.PathValue("slug"), body.Configuration, p.UserID) }
	}
	if e := configure(); e != nil {
		status := 422
		if errors.Is(e, plugin.ErrConfigurationConflict) {
			status = 409
		}
		if errors.Is(e, plugin.ErrTaskControlUnavailable) || errors.Is(e, plugin.ErrTaskControlCorrupt) || errors.Is(e, plugin.ErrTaskControlCapacity) {
			status = 503
		}
		s.writeAPIError(w, r, status, "plugins.configuration", e.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "version": body.Configuration.Version + 1})
}
func (s *Server) pluginBusinessAction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		Payload        json.RawMessage `json:"payload"`
		ExpectedUserID int64           `json:"expected_user_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 32<<10, "32 KiB"); !ok || !s.pluginIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	if allowed, _ := s.accessRate.allow("plugin-action:"+fmt.Sprint(p.UserID), 30, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "plugins.rate", "操作过于频繁，请稍后再试。", nil)
		return
	}
	slug := r.PathValue("slug")
	// Capture before guest execution: a settings/lifecycle race cannot grant
	// this invocation a new authorization lifetime.
	epoch, epochErr := s.plugins.CurrentTaskEpoch(slug)
	enqueue := func(ctx context.Context, key string, payload json.RawMessage) (any, error) {
		if epochErr != nil || s.pluginTasks == nil {
			return nil, pluginjobs.ErrAuthorization
		}
		user, err := s.auth.GetUser(ctx, p.UserID)
		if err != nil {
			return nil, err
		}
		if !pluginTaskUserAllowed(user) {
			return nil, pluginjobs.ErrAuthorization
		}
		current, err := s.plugins.CurrentTaskEpoch(slug)
		if err != nil || current.Token != epoch.Token {
			return nil, pluginjobs.ErrAuthorization
		}
		job, err := s.pluginTasks.Enqueue(ctx, pluginjobs.Input{Plugin: slug, Epoch: epoch.Token, Kind: "task.execute.v1", Key: key, ActorID: p.UserID, Payload: payload}, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		return pluginjobs.Summarize(job), nil
	}
	result, e := s.plugins.DispatchAdmin(r.Context(), slug, plugin.BusinessInput{Event: "admin.action", Payload: body.Payload}, s.pluginReadContent, enqueue)
	if e != nil {
		s.writeAPIError(w, r, 422, "plugins.execution", "执行未完成，请确认插件已启用、权限已保存，或在设置页查看运行状态。", nil)
		return
	}
	writeJSON(w, 200, result)
}
