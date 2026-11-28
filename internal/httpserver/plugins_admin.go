package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/commentorigin"
	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/version"
)

type pluginCard struct {
	Slug                                  string
	Manifest                              plugin.Manifest
	Installed, Enabled, Official, Bundled bool
	Digest, Error                         string
	Capabilities                          []string
	LatestVersion                         string
	UpdateAvailable, ReviewRequired       bool
	SettingsURL                           string
	Category                              plugin.Category
	SearchText                            string
}
type pluginCategoryView struct {
	plugin.Category
	Count int
}
type pluginRoleView struct {
	commentorigin.Rule
	Label string
}
type pluginAdminView struct {
	Cards              []pluginCard
	Categories         []pluginCategoryView
	Market             plugin.MarketView
	Origin             commentorigin.Status
	Roles              []pluginRoleView
	Editing            bool
	Installed, Enabled int
	TrustedProxies     bool
	ClientSource       clientIPDiagnostic
	SettingsPage       plugin.SettingsPage
}

func (s *Server) SetPluginServices(market *plugin.Market, origin *commentorigin.Service) {
	s.pluginMarket = market
	s.commentOrigin = origin
	if origin != nil {
		origin.SetLocationFunc(func() *time.Location { return s.siteLocation() })
	}
}
func (s *Server) registerPluginRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /plugin-assets/comment-origin/flags/{name}", s.commentOriginFlag)
	mux.HandleFunc("GET /admin/plugins/{$}", s.adminPlugins)
	mux.HandleFunc("GET /admin/plugin-records/{$}", s.adminPluginSharedReview)
	mux.HandleFunc("GET /admin/plugin-webhooks/{$}", s.adminPluginWebhooks)
	mux.HandleFunc("GET /api/v1/admin/plugin-webhooks", s.pluginWebhookList)
	mux.HandleFunc("GET /api/v1/admin/plugin-webhook-session", s.pluginWebhookSession)
	mux.HandleFunc("POST /api/v1/admin/plugin-webhooks/{id}/review", s.pluginWebhookReview)
	mux.HandleFunc("POST /api/v1/admin/plugins", s.pluginAction)
	mux.HandleFunc("GET /admin/plugins/{slug}/{$}", s.adminPluginRuntime)
	mux.HandleFunc("POST /api/v1/ai-pro-max/bridge", s.submitBridge)
	mux.HandleFunc("GET /api/v1/ai-pro-max/bridge/{key}", s.bridgeResult)
	mux.HandleFunc("POST /api/v1/ai-pro-max/bridge/{key}/ack", s.acknowledgeBridge)
	mux.HandleFunc("POST /admin/plugins/ai-pro-max/rules/{$}", s.proRulesForm)
	mux.HandleFunc("POST /admin/plugins/ai-pro-max/content/{$}", s.proSingleContent)
	mux.HandleFunc("POST /api/v1/admin/plugins/ai-pro-max/metadata", s.editorMetadata)
	mux.HandleFunc("POST /api/v1/admin/plugins/ai-pro-max/paragraph", s.proParagraph)
	mux.HandleFunc("GET /admin/plugins/ai-pro-max/articles/{id}/topics/{$}", s.proTopicEditor)
	mux.HandleFunc("POST /admin/plugins/ai-pro-max/articles/{id}/topics/{$}", s.proTopicSave)
	mux.HandleFunc("GET /ai-topics/{id}/{topic}/{$}", s.publicAITopic)
	mux.HandleFunc("POST /admin/plugins/ai-pro-max/queue/{$}", s.adminAIWritingQueueAction)
	mux.HandleFunc("POST /api/v1/admin/plugins/ai-pro-max/topics", s.adminAIWritingTopics)
	mux.HandleFunc("GET /admin/plugins/ai-pro-max/queue/{id}/draft/{$}", s.adminAIWritingQueueDraft)
	mux.HandleFunc("POST /api/v1/admin/plugins/{slug}/configuration", s.pluginBusinessSave)
	mux.HandleFunc("POST /api/v1/admin/plugins/{slug}/tasks", s.pluginTaskCreate)
	mux.HandleFunc("GET /api/v1/admin/plugins/{slug}/tasks", s.pluginTaskList)
	mux.HandleFunc("GET /api/v1/admin/plugin-task-records", s.pluginTaskRecordInspect)
	mux.HandleFunc("GET /api/v1/admin/plugin-task-quarantines", s.pluginTaskQuarantines)
	mux.HandleFunc("GET /api/v1/admin/plugin-shared-records", s.pluginSharedRecordInspect)
	mux.HandleFunc("GET /api/v1/admin/plugin-shared-quarantines", s.pluginSharedQuarantines)
	mux.HandleFunc("GET /api/v1/admin/plugin-shared-review-session", s.pluginSharedReviewSession)
	mux.HandleFunc("POST /api/v1/admin/plugins/{slug}/tasks/archive", s.pluginTaskArchive)
	mux.HandleFunc("POST /api/v1/admin/plugins/fanouts/archive", s.pluginFanoutArchive)
	mux.HandleFunc("POST /api/v1/admin/plugins/{slug}/tasks/{id}", s.pluginTaskChange)
	mux.HandleFunc("GET /api/v1/admin/plugins/{slug}/tasks/{id}/review", s.pluginTaskReview)
	mux.HandleFunc("POST /api/v1/admin/plugins/{slug}/tasks/{id}/reissues", s.pluginTaskReissue)
	mux.HandleFunc("POST /api/v1/admin/plugins/{slug}/execute", s.pluginBusinessAction)
	mux.HandleFunc("PUT /api/v1/admin/plugins/comment-origin/settings", s.pluginOriginSave)
	mux.HandleFunc("POST /api/v1/admin/plugins/comment-origin/test", s.pluginOriginTest)
	mux.HandleFunc("GET /api/v1/admin/plugins/comment-origin/records/{id}", s.pluginOriginRecord)
}

func (s *Server) adminPlugins(w http.ResponseWriter, r *http.Request) {
	s.adminPluginPage(w, r, false)
}

func (s *Server) adminPluginPage(w http.ResponseWriter, r *http.Request, editing bool) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	allowedQuery := map[string]bool{}
	if !editing {
		allowedQuery["market"] = true
	}
	q, ok := s.strictAPIQuery(w, r, allowedQuery)
	if !ok {
		return
	}
	if market := q.Get("market"); market != "" && market != "1" {
		s.renderError(w, 400, "市场检查参数无效")
		return
	}
	if s.plugins == nil || s.pluginMarket == nil {
		s.renderError(w, 503, "插件管理暂不可用")
		return
	}
	if q.Get("market") == "1" && s.pluginMarket.View().Configured {
		// The cached page renders immediately; its automatic follow-up checks
		// the signed source and replaces only the market content in place.
		// A failed check keeps the last verified catalog and exposes its error.
		_ = s.pluginMarket.RefreshIfStale(r.Context(), 30*time.Second)
	}
	items, err := s.plugins.List()
	if err != nil {
		s.renderError(w, 500, "无法读取已安装插件")
		return
	}
	view := pluginAdminView{Market: s.pluginMarket.View(), TrustedProxies: len(s.config.TrustedProxyCIDRs) > 0}
	view.ClientSource = s.clientIPDiagnostic(r, !readOnly && p.Can(auth.CapabilityCommentsModerate))
	cards := map[string]int{}
	add := func(manifest plugin.Manifest, bundled bool, digest string) {
		cards[manifest.Slug] = len(view.Cards)
		view.Cards = append(view.Cards, pluginCard{Slug: manifest.Slug, Manifest: manifest, Official: true, Bundled: bundled, Digest: digest})
	}
	for _, manifest := range plugin.BundledManifests() {
		add(manifest, true, "")
	}
	for _, release := range view.Market.Plugins {
		if release.Release != nil && release.Release.Channel != "stable" {
			continue
		}
		if _, exists := cards[release.Manifest.Slug]; !exists {
			add(release.Manifest, false, release.SHA256)
		}
	}
	for _, item := range items {
		index, exists := cards[item.Slug]
		if !exists {
			index = len(view.Cards)
			view.Cards = append(view.Cards, pluginCard{Slug: item.Slug, Manifest: plugin.Manifest{Name: item.Slug}})
		}
		c := &view.Cards[index]
		if item.Error == "" {
			c.Manifest = item.Manifest
		}
		c.Installed = true
		c.Enabled = item.Enabled
		c.Error = item.Error
		c.Official = item.Official
		c.ReviewRequired = item.VersionState.ReviewRequired
		view.Installed++
		if item.Enabled {
			view.Enabled++
		}
	}
	capabilityLabels := map[string]string{
		"content.read": "读取站点内容", "content.write": "修改站点内容",
		"settings.read": "读取插件设置", "settings.write": "保存插件设置",
		"cache": "缓存数据", "jobs.enqueue": "处理后台任务",
		"http.outbound": "查询外部服务", "http.send": "发送 HTTPS Webhook（需独立开启）", "render.fragment": "展示页面内容",
		"media.storage": "同步媒体到对象存储",
	}
	settingsPages := plugin.SettingsPagesFromInstalled(items)
	categoryCounts := make(map[string]int)
	for i := range view.Cards {
		c := &view.Cards[i]
		c.Category = plugin.CategoryFor(c.Manifest)
		categoryCounts[c.Category.Slug]++
		c.SearchText = strings.Join([]string{c.Slug, c.Manifest.Name, c.Manifest.Description, c.Manifest.Author, c.Manifest.Category, c.Category.Name, strings.Join(c.Manifest.Tags, " ")}, " ")
		for _, page := range settingsPages {
			if c.Installed && c.Slug == page.Slug {
				c.SettingsURL = page.URL
				break
			}
		}
		if c.Installed && !c.Bundled && c.Error == "" {
			for _, release := range view.Market.Plugins {
				if release.Manifest.Slug != c.Slug || release.Release == nil || release.Release.Channel != "stable" || !release.Release.DataCompatible || version.Compatible(release.Manifest.NewPress) != nil {
					continue
				}
				if cmp, err := version.Compare(release.Manifest.Version, c.Manifest.Version); err == nil && cmp > 0 {
					if newer, _ := version.Compare(release.Manifest.Version, c.LatestVersion); c.LatestVersion == "" || newer > 0 {
						c.LatestVersion, c.UpdateAvailable = release.Manifest.Version, true
					}
				}
			}
		}
		for _, capability := range view.Cards[i].Manifest.Capabilities {
			if label := capabilityLabels[capability]; label != "" {
				view.Cards[i].Capabilities = append(view.Cards[i].Capabilities, label)
			}
		}
	}
	for _, category := range plugin.Categories() {
		if count := categoryCounts[category.Slug]; count > 0 {
			view.Categories = append(view.Categories, pluginCategoryView{Category: category, Count: count})
		}
	}
	if editing {
		if s.commentOrigin == nil {
			s.renderError(w, 503, "插件配置暂不可用")
			return
		}
		view.SettingsPage, _ = plugin.SettingsPageFor(plugin.BuiltinManifest())
		view.Origin, err = s.commentOrigin.Status()
		if err != nil {
			s.renderError(w, 404, "请先安装评论来源插件")
			return
		}
		view.Editing = true
		roles := auth.BuiltInRoles()
		if s.auth.PersistentUsers() {
			roles, err = s.auth.ListRoles(r.Context())
			if err != nil {
				s.renderError(w, 500, "无法加载用户组")
				return
			}
		}
		roles = append([]auth.Role{{Slug: "guest", Name: "访客（未登录）"}}, roles...)
		for _, role := range roles {
			rule := commentorigin.Rule{Role: role.Slug}
			for _, saved := range view.Origin.Settings.Rules {
				if saved.Role == role.Slug {
					rule = saved
					break
				}
			}
			view.Roles = append(view.Roles, pluginRoleView{rule, role.Name})
		}
	}
	data := s.baseViewFor(w, "官方插件", "按需安装官方扩展。", "/admin/plugins/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-plugins"
	data.Admin = s.adminViewWithSnapshots("plugins", p, readOnly, nil, &items)
	if view.Editing {
		data.Admin.DetailTitle = "评论来源"
		data.Admin.PluginSlug = "comment-origin"
	}
	data.PluginAdmin = view
	s.render(w, http.StatusOK, "admin-plugins", data)
}

func (s *Server) pluginError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusUnprocessableEntity
	if errors.Is(err, commentorigin.ErrConflict) || errors.Is(err, plugin.ErrTaskControlConflict) {
		status = http.StatusConflict
	}
	if errors.Is(err, plugin.ErrTaskControlUnavailable) || errors.Is(err, plugin.ErrTaskControlCorrupt) || errors.Is(err, plugin.ErrTaskControlCapacity) {
		status = http.StatusServiceUnavailable
	}
	s.writeAPIError(w, r, status, "plugins.operation_failed", err.Error(), nil)
}
func (s *Server) pluginIdentity(w http.ResponseWriter, r *http.Request, p auth.Principal, expected int64) bool {
	if expected != p.UserID {
		s.writeAPIError(w, r, 409, "plugins.identity_changed", "登录账号已变化，输入已保留，请刷新并核对账号后重试。", nil)
		return false
	}
	return true
}

func (s *Server) pluginAction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		Action         string `json:"action"`
		Slug           string `json:"slug"`
		Digest         string `json:"digest"`
		ExpectedUserID int64  `json:"expected_user_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 4096, "4 KiB"); !ok || !s.pluginIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	if s.pluginMarket == nil {
		s.pluginError(w, r, errors.New("插件市场暂不可用"))
		return
	}
	var err error
	if body.Action == "refresh" || body.Action == "install" {
		s.versionMu.Lock()
		defer s.versionMu.Unlock()
		if s.extensions != nil && s.extensions.Busy() {
			s.extensionError(w, r, extension.ErrBusy)
			return
		}
		if s.updates != nil {
			snapshot, stateErr := s.updates.Snapshot()
			if stateErr != nil {
				s.extensionError(w, r, stateErr)
				return
			}
			if snapshot.State.BlocksChanges() {
				s.extensionError(w, r, extension.ErrBusy)
				return
			}
		}
	}
	switch body.Action {
	case "refresh":
		err = s.pluginMarket.Refresh(r.Context())
	case "install":
		if plugin.IsBundledSlug(body.Slug) && body.Digest == "" {
			err = s.plugins.InstallBundled(body.Slug)
		} else {
			err = s.pluginMarket.Install(r.Context(), body.Slug, body.Digest)
		}
	case "enable":
		var items []plugin.Installed
		items, err = s.plugins.List()
		allowed := false
		for _, item := range items {
			if item.Manifest.Slug == body.Slug && item.Official {
				allowed = true
			}
		}
		if err == nil && !allowed {
			err = errors.New("仅允许启用经官方市场安装的插件")
		}
		if err == nil {
			err = s.plugins.SetEnabled(body.Slug, true)
		}
	case "disable":
		err = s.plugins.SetEnabled(body.Slug, false)
	case "retry":
		if body.Slug != plugin.CommentOrigin {
			err = errors.New("该插件不支持重试查询")
		} else {
			err = s.commentOrigin.Retry()
		}
	case "uninstall":
		err = s.plugins.Uninstall(body.Slug)
	default:
		err = errors.New("不支持该插件操作")
	}
	if err != nil {
		s.pluginError(w, r, err)
		return
	}
	slog.Info("official plugin operation", "action", body.Action, "slug", body.Slug, "actor", p.Username)
	if body.Slug == plugin.ObjectStorage && s.objectStorage != nil {
		s.objectStorage.Wake()
	}
	if body.Slug == plugin.CDN && s.cdn != nil {
		s.cdn.Wake()
	}
	writeJSON(w, 200, map[string]any{"ok": true, "notice": "操作已完成"})
}

func (s *Server) pluginOriginSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		Settings       commentorigin.Settings `json:"settings"`
		ClearKey       bool                   `json:"clear_key"`
		ExpectedUserID int64                  `json:"expected_user_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 32<<10, "32 KiB"); !ok || !s.pluginIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	// Do not persist arbitrary/misspelled group slugs supplied by a browser.
	roles := auth.BuiltInRoles()
	if s.auth.PersistentUsers() {
		var err error
		roles, err = s.auth.ListRoles(r.Context())
		if err != nil {
			s.pluginError(w, r, errors.New("无法核对角色"))
			return
		}
	}
	valid := map[string]bool{"guest": true}
	for _, role := range roles {
		valid[role.Slug] = true
	}
	for _, rule := range body.Settings.Rules {
		if !valid[rule.Role] {
			s.pluginError(w, r, errors.New("角色已变化，请刷新后核对"))
			return
		}
	}
	settings, err := s.commentOrigin.Save(body.Settings, body.ClearKey)
	if err != nil {
		s.pluginError(w, r, err)
		return
	}
	status, _ := s.commentOrigin.Status()
	writeJSON(w, 200, map[string]any{"settings": settings, "key_saved": status.KeySaved, "notice": "评论来源设置已保存"})
}

func (s *Server) pluginOriginTest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		IP             string `json:"ip"`
		ExpectedUserID int64  `json:"expected_user_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 1024, "1 KiB"); !ok || !s.pluginIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	if allowed, _ := s.accessRate.allow("plugin-ip-test:"+p.Username, 10, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "plugins.rate_limited", "测试过于频繁，请稍后再试。", nil)
		return
	}
	location, err := s.commentOrigin.Test(r.Context(), body.IP)
	if err != nil {
		s.pluginError(w, r, err)
		return
	}
	writeJSON(w, 200, location)
}

func (s *Server) pluginOriginRecord(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !p.Can(auth.CapabilityCommentsModerate) {
		s.writeAPIError(w, r, 403, "plugins.private_record_forbidden", "查看真实 IP 还需要评论审核权限。", nil)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		s.pluginError(w, r, errors.New("评论编号无效"))
		return
	}
	if _, err = s.comments.Get(r.Context(), id); err != nil {
		s.writeAPIError(w, r, 404, "comments.not_found", "评论不存在", nil)
		return
	}
	record, err := s.commentOrigin.PrivateRecord(id)
	if err != nil {
		s.writeAPIError(w, r, 404, "plugins.origin_missing", "这条评论没有插件来源记录。", nil)
		return
	}
	writeJSON(w, 200, struct {
		commentorigin.Record
		SourceLabel      string `json:"source_label"`
		SourceIssueLabel string `json:"source_issue_label,omitempty"`
	}{record, clientIPSourceLabel(record.Source), clientIPIssueLabel(record.SourceIssue)})
}

func (s *Server) captureCommentOrigin(r *http.Request, id int64) {
	if s.commentOrigin != nil {
		result := s.clientIPs.resolve(r)
		source := commentorigin.Source{Method: result.Source, Issue: result.Issue}
		if result.Reliable {
			source.IP = result.Address.String()
		}
		if err := s.commentOrigin.CaptureSource(id, source, boundedCommentUserAgent(r.UserAgent())); err != nil {
			slog.Warn("comment origin capture failed", "comment_id", id)
		}
	}
}

func (s *Server) forgetCommentOrigin(id int64) {
	if s.commentOrigin != nil {
		if err := s.commentOrigin.Forget(id); err != nil {
			slog.Warn("comment origin cleanup deferred", "comment_id", id)
		}
	}
}

func (s *Server) commentOriginBadges(r *http.Request, item comment.Comment) commentorigin.Badges {
	if s.commentOrigin == nil {
		return commentorigin.Badges{}
	}
	roles := []string{"guest"}
	if item.UserID > 0 {
		user, err := s.auth.GetUser(r.Context(), item.UserID)
		if err != nil {
			return commentorigin.Badges{}
		}
		roles = nil
		for _, role := range user.Roles {
			roles = append(roles, role.Slug)
		}
		if len(roles) == 0 {
			return commentorigin.Badges{}
		}
	} else if item.StaffReply {
		roles = []string{"administrator"}
	}
	return s.commentOrigin.Badges(item.ID, roles)
}
func (s *Server) publicCommentWithOrigin(r *http.Request, item comment.Comment) publicCommentAPIResponse {
	v := publicCommentResponse(item)
	v.Origin = s.commentOriginBadges(r, item)
	badges, tips := s.commentRecognition(r.Context(), item.PostID, []comment.Comment{item})
	v.Badges, v.Support = badges[item.ID], tips[item.ID]
	return v
}
func (s *Server) decorateCommentOrigins(r *http.Request, nodes []*publicCommentNode, items []comment.Comment) {
	byID := map[int64]commentorigin.Badges{}
	for _, item := range items {
		byID[item.ID] = s.commentOriginBadges(r, item)
	}
	var visit func([]*publicCommentNode)
	visit = func(nodes []*publicCommentNode) {
		for _, n := range nodes {
			n.Origin = byID[n.ID]
			visit(n.Children)
		}
	}
	visit(nodes)
}
