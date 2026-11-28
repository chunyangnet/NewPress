package httpserver

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/module"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/wechat"
)

func (s *Server) featureStatus(ctx context.Context, feature, object string, a adminView) adminFeatureStatus {
	return s.featureStatusWithPost(ctx, feature, object, a, nil)
}

func (s *Server) featureStatusWithPost(ctx context.Context, feature, object string, a adminView, savedPost *content.Post) adminFeatureStatus {
	snapshot := s.siteSettings.Snapshot()
	v := adminFeatureStatus{Feature: feature, Object: object, Revision: snapshot.Revision,
		CheckedAt: time.Now().UTC().Format(time.RFC3339), UserID: a.Principal.UserID, ReadOnly: a.ReadOnly || a.Preview,
		Items: []featureStatusItem{}}
	switch feature {
	case "ai", "ai-queue":
		v.Items = []featureStatusItem{s.aiFeatureStatus(feature == "ai-queue", snapshot.Settings.Modules)}
		if feature == "ai-queue" && !canCreateContent(a.Principal) {
			v.Items[0].Checks = append(v.Items[0].Checks, featureInfo("actor", "当前账号", "当前账号没有排程写作权限。业务生效状态独立于账号操作权限。"))
		}
	case "wechat":
		v.Items = s.wechatFeatureStatus(ctx, snapshot.Settings.Modules)
	case "ai-related":
		item := s.moduleConfiguredStatus("ai", snapshot.Settings.Modules)
		item.ID, item.Title, item.Scope = "ai-related", "相关话题管理", "当前内容"
		item.Summary = "可手动维护本篇相关话题；自动生成另按模型与任务规则执行。"
		check, _ := s.aiPluginFeatureCheck()
		item.Checks = append(item.Checks, check, featureInfo("mode", "手动维护", "编辑已保存的话题不需要文本模型，也不受写作队列暂停影响。"))
		v.Items = []featureStatusItem{item}
	case "comments", "article-comments":
		v.Items = []featureStatusItem{s.commentsFeatureStatusWithPost(ctx, feature, object, snapshot.Settings, savedPost)}
	case "features":
		v.Items = []featureStatusItem{s.pageFeatureStatus(ctx, "feedback", "", snapshot.Settings.Modules), s.pageFeatureStatus(ctx, "links", "", snapshot.Settings.Modules)}
	case "feedback", "links":
		v.Items = []featureStatusItem{s.pageFeatureStatus(ctx, feature, object, snapshot.Settings.Modules)}
	case "progression":
		v.Items = s.progressionFeatureStatus(ctx)
	case "currency":
		v.Items = s.currencyFeatureStatus(ctx, object)
	case "recognition":
		v.Items = s.recognitionFeatureStatus(ctx)
	case "checkin":
		v.Items = []featureStatusItem{s.homeCheckInFeatureStatus(ctx)}
	case "ai-usage":
		v.Items = s.aiUsageFeatureStatus(snapshot.Settings.Modules)
	case "layout", "navigation":
		v.Items = s.presentationFeatureStatus(ctx, feature)
	default:
		v.Items = []featureStatusItem{s.moduleFeatureStatus(feature, snapshot.Settings.Modules)}
	}
	finishFeatureStatus(&v, a)
	return v
}

// Module routing and this projection share module.Allowed; server dependencies
// are also consumed by the module catalogue, so its explanation cannot drift.
func (s *Server) moduleFeatureStatus(id string, overrides map[string]bool) featureStatusItem {
	v := s.moduleConfiguredStatus(id, overrides)
	v.Checks = append(v.Checks, s.moduleRuntimeChecks(id)...)
	return v
}

func (s *Server) moduleConfiguredStatus(id string, overrides map[string]bool) featureStatusItem {
	d, _ := module.Lookup(id)
	v := featureStatusItem{ID: id, Title: d.Title, Scope: "全站", Summary: "模块已启用，具体操作仍按业务规则执行。"}
	if d.Control == "switch" {
		a := featureLink("启用"+d.Title+"模块", "/admin/modules/#module-card-"+id, auth.CapabilitySettingsManage)
		a.Module = id
		v.Checks = append(v.Checks, featureCheck("module", "模块开关", module.Allowed(overrides, id), "paused", "模块已暂停，已有配置和数据保留。启用将影响全站。", a))
	}
	return v
}

func (s *Server) aiUsageFeatureStatus(overrides map[string]bool) []featureStatusItem {
	report := s.moduleConfiguredStatus("analytics", overrides)
	report.ID, report.Title = "ai-usage", "AI 用量报表"
	report.Summary = "可查看已记录的 AI 用量；网站访问采集与 AI 请求采集分别管理。"
	report.Checks = append(report.Checks, featureCheck("store", "用量存储", s.aiTelemetry != nil, "error", "AI 用量存储尚未装配，请检查服务启动日志。", featureLink("查看系统状态", "/admin/system/", auth.CapabilitySystemRead)))
	if s.aiSettings == nil {
		return []featureStatusItem{report, featureFailure("ai-collection", "AI 请求采集", "AI 接口设置尚未装配。")}
	}
	cfg := s.aiSettings.Operations()
	collection := featureStatusItem{ID: "ai-collection", Title: "AI 请求采集", Scope: "后续 AI 请求", Summary: "后续请求会记录用量，费用需按各模型配置的价格估算。", Checks: []featureStatusCheck{
		featureCheck("collection", "采集 AI 用量", cfg.Enabled, "paused", "AI 用量采集已关闭，已有记录仍可查看。", featureFocus("配置 AI 用量采集", "/admin/settings/#site-settings-ai", "[data-ai-config-ops-enabled]", auth.CapabilitySettingsManage)),
		featureCheck("store", "用量存储", s.aiTelemetry != nil, "error", "AI 用量存储尚未装配，请检查服务启动日志。", featureLink("查看系统状态", "/admin/system/", auth.CapabilitySystemRead)),
		featureInfo("details", "请求明细", map[bool]string{true: "记录请求明细。", false: "不保留新请求明细，用量汇总独立保留。"}[cfg.Details]),
		featureInfo("costs", "费用估算", map[bool]string{true: "按已配置的模型价格估算；未配置价格不等于费用为零。", false: "费用估算已关闭。"}[cfg.Costs]),
		featureInfo("charts", "图表展示", map[bool]string{true: "展示统计图表。", false: "统计图表已隐藏，采集规则独立保留。"}[cfg.Charts]),
	}}
	return []featureStatusItem{report, collection}
}

func (s *Server) moduleRuntimeChecks(id string) []featureStatusCheck {
	checks := []featureStatusCheck{}
	system := featureLink("查看系统状态", "/admin/system/", auth.CapabilitySystemRead)
	switch id {
	case "questions":
		checks = append(checks, featureCheck("store", "问答存储", s.questions != nil, "error", "问答存储尚未装配，请检查服务启动日志。", system))
	case "community":
		checks = append(checks, featureCheck("server", "服务器圈子服务", s.config.CommunityEnabled, "needs_config", "服务器尚未开启圈子。设置 NEWPRESS_COMMUNITY_ENABLED=true 并重启服务后，点击重新检测。", system))
		if s.config.CommunityEnabled {
			checks = append(checks, featureCheck("store", "圈子存储", s.circles != nil, "error", "服务器已开启圈子，但存储未装配。请检查服务启动日志。", system))
		}
	case "analytics":
		checks = append(checks, featureCheck("server", "访问采集", s.config.AnalyticsEnabled, "needs_config", "服务器尚未开启访问采集。设置 NEWPRESS_ANALYTICS_ENABLED=true 并重启服务后，点击重新检测。", system))
	case "wechat":
		checks = append(checks, featureCheck("service", "公众号服务", s.wechat != nil && s.wechat.Store != nil, "error", "公众号服务尚未装配，请检查服务启动日志。", system))
	case "feedback":
		checks = append(checks, featureCheck("service", "反馈存储", s.feedback != nil, "error", "反馈存储尚未装配，请检查服务启动日志。", system))
	case "links":
		checks = append(checks, featureCheck("service", "链接存储", s.linkPages != nil, "error", "链接存储尚未装配，请检查服务启动日志。", system))
	}
	return checks
}

// The queue and status projection resolve exactly the same set of text models.
func (s *Server) writingModels(settings aieditor.Settings) []writingModelOption {
	models := []writingModelOption{}
	if s.aiSettings == nil {
		return models
	}
	for _, provider := range settings.Providers {
		if _, err := s.aiSettings.Resolve(aieditor.Connection{ProviderID: provider.ID}, true); err != nil {
			continue
		}
		for _, model := range provider.Models {
			models = append(models, writingModelOption{provider.ID + ":" + model, provider.Name + " · " + model, provider.ID == settings.DefaultProvider && model == provider.DefaultModel})
		}
	}
	return models
}

func (s *Server) aiFeatureStatus(queue bool, overrides map[string]bool) featureStatusItem {
	v := s.moduleFeatureStatus("ai", overrides)
	v.Title, v.Summary = "AI 文本能力", "文本模型配置已满足；连接与任务执行结果以实际记录为准。"
	if queue {
		v.ID, v.Title = "ai-queue", "AI 写作队列"
		check, enabled := s.aiPluginFeatureCheck()
		v.Checks = append(v.Checks, check)
		if enabled && s.aiJobs != nil {
			state, err := s.aiJobs.Snapshot()
			if err != nil {
				v.Checks = append(v.Checks, featureStatusCheck{ID: "queue", Label: "队列记录", State: "error", Reason: "队列记录暂时不可读，请检查插件数据目录。"})
			} else {
				v.Checks = append(v.Checks, featureCheck("queue", "领取新任务", !state.Paused, "paused", "队列已暂停领取，正在执行的任务会继续。", featureFocus("恢复队列", "/admin/plugins/ai-pro-max/#writing-queue", "#writing-queue-control [value=resume]", auth.CapabilityPluginsManage)))
			}
		}
		if enabled && s.aiJobs == nil {
			v.Checks = append(v.Checks, featureStatusCheck{ID: "queue", Label: "写作队列", State: "error", Reason: "写作队列尚未装配，请检查服务状态。"})
		}
	}
	models := 0
	if s.aiSettings != nil {
		models = len(s.writingModels(s.aiSettings.Snapshot()))
	}
	v.Checks = append(v.Checks, featureCheck("models", "文本模型配置", models > 0, "needs_config", "尚无可用的文本模型，请补全接口、密钥和模型配置。", featureLink("配置文本模型", "/admin/settings/#site-settings-ai", auth.CapabilitySettingsManage)))
	if queue {
		v.Summary = "队列可领取新任务，模型配置已满足；执行结果以任务记录为准。"
	}
	return v
}

func (s *Server) aiPluginFeatureCheck() (featureStatusCheck, bool) {
	installed, enabled, readable := false, false, s.plugins != nil
	if s.plugins != nil {
		items, err := s.plugins.List()
		readable = err == nil
		for _, item := range items {
			if item.Slug == plugin.AIProMax && item.Official && item.Error == "" {
				installed, enabled = true, item.Enabled
			}
		}
	}
	action := featureLink("管理插件", "/admin/plugins/", auth.CapabilityPluginsManage)
	if !readable {
		return featureStatusCheck{ID: "plugin", Label: "AI Pro Max 插件", State: "error", Reason: "无法读取插件状态，请检查插件管理。", Action: action}, false
	}
	state, reason := "needs_config", "尚未安装有效的 AI Pro Max 插件，请在插件管理中检查。"
	if installed {
		state, reason = "paused", "AI Pro Max 插件已停用。"
	}
	return featureCheck("plugin", "AI Pro Max 插件", installed && enabled, state, reason, action), enabled
}

func (s *Server) wechatFeatureStatus(ctx context.Context, overrides map[string]bool) []featureStatusItem {
	base := s.moduleFeatureStatus("wechat", overrides)
	if s.wechat == nil || s.wechat.Store == nil {
		return []featureStatusItem{base}
	}
	cfg, err := wechat.ReadConfig(ctx, s.wechat.Store)
	if err != nil {
		base.Checks = append(base.Checks, featureStatusCheck{ID: "config", Label: "已保存配置", State: "error", Reason: "公众号配置暂时不可读，当前输入不会被清除。"})
		return []featureStatusItem{base}
	}
	sync := base
	sync.ID, sync.Title = "wechat-sync", "公众号同步"
	sync.Checks = append([]featureStatusCheck{}, base.Checks...)
	config := func(label, name string) *featureStatusAction {
		return featureFocus(label, "/admin/wechat/#config", "[data-wechat-config] [name="+name+"]", auth.CapabilitySettingsManage)
	}
	sync.Checks = append(sync.Checks,
		featureCheck("sync", "同步开关", cfg.Enabled, "paused", "公众号同步已暂停，文章采集独立管理。", config("配置同步", "enabled")),
		featureCheck("credentials", "AppID 与密钥", cfg.AppID != "" && cfg.Secret != "", "needs_config", "尚未补全 AppID 或 AppSecret。保存后可测试已保存的连接。", config("补全账号配置", "app_id")))
	sync.Mode = "仅手动同步"
	if cfg.AutoSync {
		sync.Mode = "新文章自动同步"
	}
	if cfg.AutoPublish {
		sync.Mode += " · 自动发表"
	} else {
		sync.Mode += " · 存入草稿箱"
	}
	sync.Summary = "同步配置已满足；连接和发表结果以测试及任务记录为准。"
	sync.Checks = append(sync.Checks, featureInfo("mode", "运行模式", sync.Mode))
	collect := base
	collect.ID, collect.Title = "wechat-collect", "文章采集"
	collect.Checks = append([]featureStatusCheck{}, base.Checks...)
	collect.Checks = append(collect.Checks, featureCheck("collect", "采集开关", cfg.CollectEnabled, "paused", "文章采集已暂停，不影响公众号同步配置。", config("配置采集", "collect_enabled")))
	collect.Summary = "可预览并导入文章，不需要公众号同步密钥。"
	return []featureStatusItem{sync, collect}
}

func (s *Server) commentsFeatureStatus(ctx context.Context, feature, object string, settings site.Settings) featureStatusItem {
	return s.commentsFeatureStatusWithPost(ctx, feature, object, settings, nil)
}

func (s *Server) commentsFeatureStatusWithPost(ctx context.Context, feature, object string, settings site.Settings, savedPost *content.Post) featureStatusItem {
	v := s.moduleFeatureStatus("comments", settings.Modules)
	v.Summary = "全站评论已开启，每篇内容独立决定是否接受新评论。"
	labels := map[bool]string{true: "开放", false: "关闭"}
	defaults := fmt.Sprintf("新文章默认%s评论，新页面默认%s评论；仅影响新建内容，既有内容保持各自设置。", labels[settings.Writing.PostCommentsEnabled], labels[settings.Writing.PageCommentsEnabled])
	v.Checks = append(v.Checks, featureInfo("defaults", "新建默认值", defaults))
	if feature != "article-comments" {
		return v
	}
	v.Title, v.Scope = "本篇评论", "当前内容"
	if object == "" {
		v.Checks = append(v.Checks, featureStatusCheck{ID: "post", Label: "内容保存", State: "unknown", Reason: "这是尚未保存的新内容，评论设置将在保存后生效。"})
		return v
	}
	id, _ := strconv.ParseInt(object, 10, 64)
	var post content.Post
	var err error
	if savedPost != nil && savedPost.ID == id {
		post = *savedPost
	} else {
		post, err = s.repository.GetByID(ctx, id)
	}
	if err != nil {
		v.Checks = append(v.Checks, featureStatusCheck{ID: "post", Label: "本篇设置", State: "error", Reason: "无法读取本篇已保存的评论设置。"})
		return v
	}
	v.Checks = append(v.Checks, featureCheck("post", "接受新评论", post.CommentStatus == content.CommentOpen, "paused", "本篇已关闭新评论，全站评论设置不受影响。", featureFocus("调整本篇评论", "/admin/content/"+object+"/edit/", "[name=comment_status]", "")))
	v.Summary = "本篇允许新评论，提交仍受登录、审核和访问规则约束。"
	return v
}

func (s *Server) pageFeatureStatus(ctx context.Context, kind, slug string, overrides map[string]bool) featureStatusItem {
	v := s.moduleFeatureStatus(kind, overrides)
	v.Checks = append(v.Checks, featureCheck("template", "页面模板", s.pageAvailable(kind), "needs_config", "当前主题与核心均未提供此页面模板。请选择支持该页面的主题。", featureLink("管理主题", "/admin/themes/", auth.CapabilityAppearanceManage)))
	v.Summary = "模块和页面模板可用，每个页面独立控制开放状态。"
	if slug == "" {
		return v
	}
	v.Scope = "当前页面"
	name, enabled, readable := "", false, false
	if kind == "feedback" && s.feedback != nil {
		b, err := s.feedback.Get(ctx, slug)
		name, enabled, readable = b.Settings.Name, b.Settings.Enabled, err == nil
	}
	if kind == "links" && s.linkPages != nil {
		b, err := s.linkPages.Get(ctx, slug)
		name, enabled, readable = b.Settings.Name, b.Settings.Enabled, err == nil
	}
	if !readable {
		v.Checks = append(v.Checks, featureStatusCheck{ID: "page", Label: "页面设置", State: "error", Reason: "无法读取该页面的已保存设置。"})
		return v
	}
	v.Title = name
	href := "/admin/features/?edit=" + slug
	if kind == "links" {
		href = "/admin/features/links/?edit=" + slug
	}
	v.Checks = append(v.Checks, featureCheck("page", "对外开放", enabled, "paused", "此页面已关闭，已有内容保留，管理员仍可配置。", featureFocus("开放此页面", href+"#feature-interactions", "input[name=enabled]", auth.CapabilitySettingsManage)))
	v.Summary = "此页面已开放，具体互动仍按页面权限与规则执行。"
	return v
}

func (s *Server) pageUnavailableReason(kind string) string {
	if !s.moduleEnabled(kind) {
		return "模块已暂停"
	}
	for _, check := range s.moduleRuntimeChecks(kind) {
		if check.State != "ready" {
			return check.Reason
		}
	}
	if !s.pageAvailable(kind) {
		return "页面模板缺失"
	}
	return ""
}

func (s *Server) presentationFeatureStatus(ctx context.Context, feature string) []featureStatusItem {
	v := featureStatusItem{ID: feature, Title: "入口与业务状态", Scope: "展示入口", Summary: "导航与布局决定入口展示，业务启停在对应业务中管理。"}
	v.Checks = []featureStatusCheck{featureInfo("scope", "影响范围", "隐藏导航或布局入口不会关闭业务，也不会删除内容。")}
	if feature == "layout" {
		snapshot := s.themes.SettingsSnapshot()
		v.Title = "首页布局"
		v.Checks = append(v.Checks, featureCheck("layout", "主题布局能力", s.themes.Active().Supports(theme.HomeLayoutV1), "needs_config", "当前主题未提供首页布局画布，请在主题设置中管理入口。", featureLink("主题设置", "/admin/appearance/", auth.CapabilityAppearanceManage)))
		if value, ok := snapshot.Values["sidebar_enabled"].(bool); ok {
			label := "侧栏已开启。"
			if !value {
				label = "侧栏已关闭，侧栏内模块不会显示；开启侧栏后保存布局生效。"
			}
			check := featureInfo("sidebar", "侧栏展示", label)
			if !value {
				check.Action = featureFocus("开启侧栏", "/admin/appearance/layout/", "[data-sidebar-enable]", auth.CapabilityAppearanceManage)
			}
			v.Checks = append(v.Checks, check)
		}
		return []featureStatusItem{v, s.homeCheckInFeatureStatus(ctx)}
	}
	return []featureStatusItem{v}
}
