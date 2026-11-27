package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/plugin"
)

type aiProMaxView struct {
	Notice         string
	Enabled        bool
	TextModels     int
	ImageModels    int
	TextProviders  []aiProMaxProvider
	ImageProviders []aiProMaxProvider
	Requests       int64
	Successful     int64
	Failed         int64
	Images         int64
	PeriodStart    string
	PeriodEnd      string
	Features       []aiProMaxFeature
	Queue          writingQueueView
}

type aiProMaxProvider struct {
	Name, Model, Status string
}

type aiProMaxFeature struct {
	Title, Description, URL, Status, Icon string
	Available                             bool
}

// Both the site module and the current plugin installation own model access.
// The lease is cancelled when that installation is disabled or replaced.
type aiProMaxAccess struct{ installation, module context.Context }

func (a aiProMaxAccess) active() bool { return a.installation.Err() == nil && a.module.Err() == nil }

func (a aiProMaxAccess) forRequest(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	stopPlugin := context.AfterFunc(a.installation, cancel)
	stopModule := context.AfterFunc(a.module, cancel)
	return ctx, func() { stopPlugin(); stopModule(); cancel() }
}

func (s *Server) aiProMaxLease() (aiProMaxAccess, error) {
	module, ok := s.siteSettings.ModuleContext("ai")
	if s.plugins == nil || !ok {
		return aiProMaxAccess{}, plugin.ErrInactive
	}
	var generation uint64
	if err := s.plugins.WithData(plugin.AIProMax, true, 0, func(_ string, g uint64) error { generation = g; return nil }); err != nil {
		return aiProMaxAccess{}, err
	}
	installation, err := s.plugins.ActiveContext(plugin.AIProMax, generation)
	return aiProMaxAccess{installation, module}, err
}

func (s *Server) adminAIProMaxPage(w http.ResponseWriter, r *http.Request) {
	principal, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	if s.plugins == nil {
		s.renderFeatureRecovery(w, r, "ai-queue", "", s.adminView("plugins", principal, readOnly), http.StatusServiceUnavailable, "")
		return
	}
	items, err := s.plugins.List()
	if err != nil {
		s.renderFeatureRecovery(w, r, "ai-queue", "", s.adminView("plugins", principal, readOnly), http.StatusInternalServerError, "")
		return
	}
	installed := false
	enabled := false
	for _, item := range items {
		if item.Slug == plugin.AIProMax && item.Error == "" && item.Official {
			installed, enabled = true, item.Enabled
			break
		}
	}
	if !installed {
		s.renderFeatureRecovery(w, r, "ai-queue", "", s.adminView("plugins", principal, readOnly), http.StatusNotFound, "")
		return
	}

	view := aiProMaxView{
		Enabled:       enabled,
		TextProviders: []aiProMaxProvider{}, ImageProviders: []aiProMaxProvider{},
		Features: []aiProMaxFeature{
			{Title: "AI 写作与草稿修订", Description: "生成文章，或润色、改写、扩写、精简整份草稿。支持段落选区和自动识别语言。", URL: "/admin/content/new/", Status: "已接入", Icon: "pen-line", Available: true},
			{Title: "摘要与 SEO 元数据", Description: "生成摘要、英文别名、标签、分类建议和配图提示，并在编辑器中核对。", URL: "/admin/content/new/", Status: "已接入", Icon: "scan-search", Available: true},
			{Title: "AI 图片工作台", Description: "使用已配置的图片模型生成配图，保存至媒体库后添加到文章。", URL: "/admin/content/new/", Status: "已接入", Icon: "image", Available: true},
			{Title: "运营与内容分析", Description: "运行运维诊断、内容总结、SEO 检查、选题规划和成本分析。", URL: "/admin/ai/", Status: "已接入", Icon: "chart-no-axes-column", Available: true},
			{Title: "模型用量与费用", Description: "查看模型请求、用量、响应时间与费用估算。", URL: "/admin/analytics/ai/", Status: "已接入", Icon: "cpu", Available: true},
			{Title: "AI 评论回复草稿", Description: "根据文章和评论生成可编辑的站方回复建议；审核者检查并手工提交。", URL: "/admin/comments/", Status: "已接入", Icon: "message-circle", Available: true},
			{Title: "自动评论审核与角色对话", Description: "配置审核词、角色唤醒和预约回复。", URL: "#automation", Status: "已接入", Icon: "message-square", Available: true},
			{Title: "定时写作队列", Description: "按分类安排选题，到点生成后交由编辑审核。", URL: "#writing-queue", Status: "已接入", Icon: "list", Available: true},
			{Title: "相关话题内容类型", Description: "为原文生成相关问题及独立阅读页。", URL: "#automation", Status: "已接入", Icon: "panels-top-left", Available: true},
			{Title: "图片识别与自动图片队列", Description: "使用媒体库图片作为上下文，生成封面与正文配图及压缩版本。", URL: "#automation", Status: "已接入", Icon: "scan-search", Available: true},
			{Title: "PostMeta 自动化触发", Description: "发布或采集时用 meta.ai_pro_max 覆盖站点触发规则。", URL: "#automation", Status: "已接入", Icon: "route", Available: true},
			{Title: "电商与自定义内容类型", Description: "安装 WordPress 配套插件，为商品及所选公开内容类型补全字段。", URL: "#automation", Status: "需配对", Icon: "shopping-bag", Available: true},
		},
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("notice")); err == nil && n >= 0 && n <= 100 {
		if n == 0 {
			view.Notice = "所选内容没有需要补全的字段。"
		} else {
			view.Notice = fmt.Sprintf("已检查 %d 项需要补全的内容，可在下方查看任务。", n)
		}
	}
	settings := s.aiSettings.Snapshot()
	view.Queue = s.writingQueueView(r, principal, readOnly, enabled, settings)
	for _, provider := range settings.Providers {
		item := aiProMaxProvider{Name: provider.Name, Model: provider.DefaultModel, Status: "配置待检查"}
		if aieditorIsImageProtocol(provider.Protocol) {
			if _, err := s.aiSettings.ResolveImage(aieditor.Connection{ProviderID: provider.ID}); err == nil {
				item.Status = "已配置"
			}
			view.ImageProviders = append(view.ImageProviders, item)
			view.ImageModels += len(provider.Models)
		} else {
			if _, err := s.aiSettings.Resolve(aieditor.Connection{ProviderID: provider.ID}, true); err == nil {
				item.Status = "已配置"
			}
			view.TextProviders = append(view.TextProviders, item)
			view.TextModels += len(provider.Models)
		}
	}
	location := s.siteLocation(r.Context())
	end := time.Now().In(location)
	start := end.AddDate(0, 0, -6)
	view.PeriodStart, view.PeriodEnd = start.Format("2006-01-02"), end.Format("2006-01-02")
	if s.aiTelemetry != nil {
		usage := s.aiTelemetry.Report(aieditor.UsageFilter{Start: view.PeriodStart, End: view.PeriodEnd, Limit: 1})
		view.Requests = usage.Totals.Requests
		view.Successful = usage.Totals.Success
		view.Failed = usage.Totals.Errors + usage.Totals.Cancelled
		view.Images = usage.Totals.Images
	}

	data := s.baseViewFor(w, "AI Pro Max", "统一进入 AI 写作、生图、运营工作台与用量统计。", "/admin/plugins/ai-pro-max/")
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-ai-pro-max"
	data.Admin = s.adminView("plugins", principal, readOnly)
	data.Admin.DetailTitle, data.Admin.PluginSlug = "AI Pro Max", plugin.AIProMax
	data.AIProMax = view
	s.render(w, http.StatusOK, "admin-ai-pro-max", data)
}

func aieditorIsImageProtocol(protocol string) bool {
	switch protocol {
	case aieditor.ProtocolOpenAIImages, aieditor.ProtocolGemini, aieditor.ProtocolGeminiInteractions, aieditor.ProtocolSeedream:
		return true
	default:
		return false
	}
}
