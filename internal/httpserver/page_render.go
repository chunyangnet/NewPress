package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/coreui"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/version"
)

func (s *Server) render(writer http.ResponseWriter, status int, page string, data pageView) {
	s.renderChecked(writer, status, page, data, nil, nil, nil)
}

// A private preview can lose authority while rendering widgets or templates.
// Keep the normal render buffer and check before any response bytes are sent.
func (s *Server) renderChecked(writer http.ResponseWriter, status int, page string, data pageView, request *http.Request, beforeOutput func() bool, access func() error) {
	data.contentPaths = s.permalinkBuilder()
	page, core := s.prepareRenderer(page, &data)
	s.prepareAdminFeatureStatus(responseContext(writer), page, &data)
	s.prepareRecognition(responseContext(writer), &data)
	data.VerificationEnabled = s.verification != nil && s.verification.Store.Enabled()
	hasSnippets := s.prepareSnippets(responseContext(writer), &data)
	s.prepareResources(responseContext(writer), &data)
	s.prepareArticleWidgets(responseContext(writer), &data)
	s.prepareArticleComponents(responseContext(writer), &data)
	s.prepareResponsiveImages(&data)
	s.preparePublicSEO(writer, status, &data)
	s.prepareSiteIntegrations(writer, status, &data, page)
	s.prepareStorageDelivery(writer)
	if !data.IsAdmin {
		prepareComponentFrames(writer, data.Post.ContentHTML)
		prepareComponentFrames(writer, data.ShopDescriptionHTML)
	}
	if !core {
		s.prepareCDNDelivery(writer, &data)
	}
	s.prepareCoreDelivery(writer, &data)
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	settings := s.siteSnapshot(responseContext(writer)).Settings
	if hasSnippets || data.IsAdmin || data.IsPreview || privatePage(data.Kind) || status >= http.StatusBadRequest {
		writer.Header().Set("Cache-Control", "no-store")
	} else {
		writer.Header().Set("Cache-Control", publicCacheControl(settings, 0))
	}
	// Identity expiry/revocation, level visibility and a newly hidden tip amount
	// must be re-evaluated on the next request, including sites with public caching.
	if (s.recognition != nil || s.progression != nil) && (data.Kind == "single" || data.Kind == "author") {
		writer.Header().Set("Cache-Control", "no-store")
	}
	if data.IsPreview {
		writer.Header().Set("Referrer-Policy", "no-referrer")
	}
	if data.IsPreview || (!data.IsAdmin && !settings.SEO.SearchEngineVisible) {
		writer.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	}
	var output theme.RenderBuffer
	var err error
	if core {
		err = coreui.Render(&output, page, data)
	} else {
		err = s.renderTheme(&output, page, data)
	}
	if err != nil {
		slog.Error("render page", "page", page, "core", core, "error", err)
		output.Reset()
		status = http.StatusInternalServerError
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		_ = coreui.Error(&output, "页面暂时不可用", "页面渲染失败，请稍后重试。管理员仍可进入后台检查主题。")
	}
	payload := output.Bytes()
	if err == nil && !core {
		payload = withSelectAssets(payload, page, data.CoreCDNAssetBase)
	}
	if err == nil && page == "admin-device-mockup" {
		payload = withDeviceMockupAssets(payload)
	}
	componentHTML := data.Post.ContentHTML + data.ShopDescriptionHTML
	if err == nil && !data.IsAdmin && strings.Contains(componentHTML, `data-np-config=`) {
		payload = withArticleComponentAssets(payload, data.CoreCDNAssetBase)
	} else if err == nil && !data.IsAdmin && strings.Contains(componentHTML, `data-np-widget="post"`) {
		payload = withArticleWidgetStyles(payload, data.CoreCDNAssetBase)
	}
	if err == nil && !data.IsAdmin && strings.Contains(data.Post.ContentHTML, `data-npr-resource=`) {
		payload = withResourceAssets(payload, data.CoreCDNAssetBase)
		payload = withResourcePreviewStates(payload, data)
	}
	// Asset insertion happens after template rendering. Apply the same ceiling
	// to those bytes before committing any part of the public response.
	if len(payload) > theme.MaxRenderBytes {
		err = theme.ErrRenderLimit
		slog.Error("render page", "page", page, "core", core, "error", err)
		output.Reset()
		status = http.StatusInternalServerError
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		_ = coreui.Error(&output, "页面暂时不可用", "页面内容超过展示上限，请缩小模板输出范围。")
		payload = output.Bytes()
	}
	if beforeOutput != nil && !beforeOutput() {
		return
	}
	outputWriter := writer
	if access != nil {
		writer.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		outputWriter = privateResponse(writer, request, access)
	}
	outputWriter.WriteHeader(status)
	_, writeErr := outputWriter.Write(payload)
	if writeErr != nil && access != nil {
		panic(http.ErrAbortHandler)
	}
	if observed, ok := writer.(*analyticsResponseWriter); ok && err == nil && writeErr == nil {
		observed.complete = true
	}
}

func (s *Server) renderError(writer http.ResponseWriter, status int, message string) {
	s.renderStatus(writer, status, message, "", "")
}

func (s *Server) baseView(title, description, path string) pageView {
	return s.baseViewContext(context.Background(), title, description, path)
}

func (s *Server) baseViewContext(ctx context.Context, title, description, path string) pageView {
	settings := s.siteSnapshot(ctx).Settings
	identity := settings.Identity
	if description == "" {
		description = identity.Description
		if description == "" {
			description = identity.Tagline
		}
	}
	pageTitle := title
	if title != "首页" {
		pageTitle += " " + identity.TitleSeparator + " " + identity.Title
	} else {
		pageTitle = identity.Title
	}
	themeSettings := s.themes.SettingsSnapshot()
	iconURL := s.siteIconURL()
	socialImageURL := identity.SocialDefaultImage
	if strings.HasPrefix(socialImageURL, "/") {
		socialImageURL = s.absolutePublicURL(socialImageURL)
	}
	generator := ""
	if settings.Operations.GeneratorMeta {
		generator = version.Name + " " + version.Version
	}
	robots := ""
	if !settings.SEO.SearchEngineVisible {
		robots = "noindex, nofollow, noarchive"
	}
	canonical := ""
	if path != "" {
		canonical = s.absolutePublicURL(path)
	}
	location := site.TimeLocation(settings)
	navigation := s.publicNavigationContext(ctx, path)
	community, _ := themeSettings.Values["community_nav"].(bool)
	if s.themes.Active().View != "" {
		// Independent themes may fall back to core pages, which use core defaults.
		defaults, _ := coreui.Defaults()
		community, _ = defaults["community_nav"].(bool)
	}
	if market, _ := themeSettings.Values["market_nav"].(bool); market && !navigation.MobileConfigured && !navigation.PrimaryConfigured {
		navigation.Mobile = []navigationItemView{{Label: "首页", URL: "/", Icon: "house"}, {Label: "商城", URL: "/shop/", Icon: "shopping-bag"}, {Label: "发现", URL: "/explore/", Icon: "compass"}, {Label: "我的", URL: "/account/", Icon: "user-round"}}
	}
	navigation.MobileDock = s.enabledMobileDock(mobileDockItems(navigation.Mobile, path, community && s.moduleEnabled("community")))
	return pageView{
		Site: siteView{
			Title: identity.Title, Tagline: identity.Tagline, Description: identity.Description,
			PublicURL: strings.TrimRight(s.config.PublicURL, "/"), ActiveTheme: s.themes.Active().Slug,
			ThemeVersion: s.themes.Active().Version, Locale: identity.Locale, Timezone: location.String(),
			IconURL: iconURL, SocialImageURL: socialImageURL, Generator: generator, Robots: robots,
			MaintenanceMessage: settings.Operations.MaintenanceMessage, PublicAPIEnabled: settings.API.PublicPostsEnabled,
		},
		LoginProviders:   s.publicLoginProviders(),
		CodeLoginEnabled: settings.RegistrationPolicy().AllowsCodeLogin(),
		ModuleOverrides:  settings.Modules,
		PageTitle:        pageTitle,
		Description:      description,
		Canonical:        canonical,
		Version:          version.Version,
		GoVersion:        runtime.Version(),
		Now:              time.Now().In(location),
		ThemeValues:      themeSettings.Values,
		ThemeClass:       s.themes.SettingsBodyClasses(),
		ThemeCSSURL:      "/theme-settings.css?v=" + s.themes.SettingsCSSSnapshot().Revision,
		Navigation:       navigation,
	}
}

func (s *Server) adminView(current string, principal auth.Principal, readOnly bool) adminView {
	return s.adminViewWithReadiness(current, principal, readOnly, nil)
}

// System diagnostics have already collected these probes under their shared
// worker budget. Preparing the sidebar must not repeat them synchronously.
func (s *Server) adminViewWithReadiness(current string, principal auth.Principal, readOnly bool, readiness []systemStatusCheck) adminView {
	return s.adminViewWithSnapshots(current, principal, readOnly, readiness, nil)
}

// A page that already listed plugins can share that request's display snapshot
// with its summary and navigation. A non-nil pointer also preserves an empty list.
func (s *Server) adminViewWithSnapshots(current string, principal auth.Principal, readOnly bool, readiness []systemStatusCheck, installed *[]plugin.Installed) adminView {
	preview := !s.auth.Configured()
	username := principal.Label()
	if username == "" {
		username = "本机只读预览"
	}
	settings := s.themes.SettingsSnapshot()
	siteSettings := s.siteSettings.Snapshot()
	plugins := []plugin.Installed(nil)
	themes := []theme.Manifest(nil)
	if preview || principal.Can(auth.CapabilityPluginsManage) || principal.Can(auth.CapabilitySystemRead) {
		if installed != nil {
			plugins = *installed
		} else if s.plugins != nil {
			plugins, _ = s.plugins.List()
		}
	}
	if preview || principal.Can(auth.CapabilityAppearanceManage) || principal.Can(auth.CapabilitySystemRead) {
		themes, _ = s.themes.List()
	}
	mediaSnapshot := s.media.Snapshot()
	fieldCount := 0
	for _, group := range settings.Groups {
		fieldCount += len(group.Fields)
	}
	checks := []adminCheck{
		{Label: "管理认证", Detail: "Argon2id、内存会话、SameSite Cookie 与 CSRF", Status: "pass"},
		{Label: "主题设置", Detail: fmt.Sprintf("%d 个结构化字段，修订 %s", fieldCount, settings.Revision), Status: "pass"},
		{Label: "站点设置", Detail: fmt.Sprintf("%d 个类型化字段，修订 %s", siteSettings.FieldCount, siteSettings.Revision), Status: "pass"},
		{Label: "插件沙箱", Detail: "WASM 默认无文件、网络、进程和环境权限", Status: "pass"},
		{Label: "媒体存储", Detail: fmt.Sprintf("%d 项媒体，%s；SHA-256 去重与原子索引", mediaSnapshot.Count, formatBytes(mediaSnapshot.TotalBytes)), Status: "pass"},
	}
	if !s.auth.Configured() {
		checks[0] = adminCheck{Label: "管理认证", Detail: "尚未配置密码哈希，仅允许回环地址只读预览", Status: "warning"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	postCount := 0
	if items, err := s.repository.ListAdmin(ctx, content.AdminQuery{AuthorID: contentAuthorScope(principal), Limit: 1}); err == nil {
		postCount = items.Total
	}
	userCount := 0
	if s.auth.PersistentUsers() {
		if users, err := s.auth.ListUsers(ctx, auth.UserQuery{Limit: 1}); err == nil {
			userCount = users.Total
			checks = append(checks, adminCheck{Label: "身份与权限", Detail: fmt.Sprintf("%d 个持久账号；角色与权限在每次会话校验时刷新", userCount), Status: "pass"})
		} else {
			checks = append(checks, adminCheck{Label: "身份与权限", Detail: "持久用户仓库暂时不可用", Status: "error"})
		}
	}
	ownedSessions := []auth.SessionInfo(nil)
	visibleSessions := []auth.SessionInfo(nil)
	if preview || principal.Can(auth.CapabilitySessionsRead) {
		var sessionErr error
		ownedSessions, sessionErr = s.auth.ListSessionsContext(ctx, principal, "", false)
		visibleSessions = ownedSessions
		if sessionErr == nil && principal.Can(auth.CapabilitySessionsManage) {
			visibleSessions, sessionErr = s.auth.ListSessionsContext(ctx, principal, "", true)
		}
		if sessionErr != nil {
			checks = append(checks, adminCheck{Label: "登录会话", Detail: "会话仓库暂时不可用", Status: "error"})
		} else {
			checks = append(checks, adminCheck{
				Label: "登录会话", Detail: fmt.Sprintf("%d 个可见活动会话；设备元数据有界记录，支持逐会话撤销", len(visibleSessions)), Status: "pass",
			})
		}
	}
	termCount := 0
	categoryCount := 0
	tagCount := 0
	if items, err := s.repository.ListTerms(ctx, content.TermQuery{Limit: 1}); err == nil {
		categoryCount = items.Counts[content.TaxonomyCategory]
		tagCount = items.Counts[content.TaxonomyTag]
		termCount = categoryCount + tagCount
		checks = append(checks, adminCheck{
			Label: "内容地图", Detail: fmt.Sprintf("%d 个分类节点与 %d 个标签信号", categoryCount, tagCount), Status: "pass",
		})
	} else {
		checks = append(checks, adminCheck{Label: "内容地图", Detail: "分类与标签指标暂时不可用", Status: "error"})
	}
	ready := func(id string, probe func() error) bool {
		if readiness != nil {
			for _, check := range readiness {
				if check.ID == id {
					return check.Status == "pass"
				}
			}
			return false
		}
		return probe() == nil
	}
	if !ready("content", func() error { return s.repository.Ready(ctx) }) {
		checks = append(checks, adminCheck{Label: "内容存储", Detail: "就绪检查失败", Status: "error"})
	} else {
		checks = append(checks, adminCheck{Label: "内容存储", Detail: "读取路径与依赖就绪", Status: "pass"})
	}
	if !ready("media", s.media.Ready) {
		checks[4] = adminCheck{Label: "媒体存储", Detail: "对象目录或索引不可用", Status: "error"}
	}
	navigationMenuCount := 0
	navigationItemCount := 0
	navigationLocationCount := 0
	if snapshot, err := s.navigation.Snapshot(ctx); err == nil {
		navigationMenuCount = snapshot.MenuCount
		navigationItemCount = snapshot.ItemCount
		navigationLocationCount = len(snapshot.Locations)
		checks = append(checks, adminCheck{
			Label: "导航路径", Detail: fmt.Sprintf("%d 个菜单、%d 个项目，%d/%d 个主题位置已分配", navigationMenuCount, navigationItemCount, navigationLocationCount, len(navigation.Locations())), Status: "pass",
		})
	} else {
		checks = append(checks, adminCheck{Label: "导航路径", Detail: "导航仓库暂时不可用", Status: "error"})
	}
	commentCount := 0
	pendingComments := 0
	if snapshot, err := s.comments.Snapshot(ctx); err == nil {
		commentCount = snapshot.Total
		pendingComments = snapshot.StatusCounts[comment.StatusPending]
		checks = append(checks, adminCheck{
			Label: "评论审核", Detail: fmt.Sprintf("%d 条评论，%d 条待审；纯文本渲染、来源摘要与乐观锁已启用", commentCount, pendingComments), Status: "pass",
		})
	} else {
		checks = append(checks, adminCheck{Label: "评论审核", Detail: "评论仓库暂时不可用", Status: "error"})
	}
	var pluginSettings []plugin.SettingsPage
	if (preview || principal.Can(auth.CapabilityPluginsManage)) && s.plugins != nil {
		pluginSettings = plugin.SettingsPagesFromInstalled(plugins)
	}
	return adminView{
		Current: current, Configured: s.auth.Configured(), Authenticated: principal.Username != "",
		PluginSettings: pluginSettings,
		Preview:        preview, ReadOnly: readOnly, Principal: principal, Username: username, Login: principal.Username,
		RoleName: principal.RoleName, CSRFToken: principal.CSRFToken,
		PersistentUsers: s.auth.PersistentUsers(),
		Settings:        settings, SiteSettings: siteSettings, Uptime: time.Since(s.startedAt).Round(time.Second).String(), StartedAt: s.startedAt,
		PostCount: postCount, PluginCount: len(plugins), ThemeCount: len(themes), TermCount: termCount,
		CategoryCount: categoryCount, TagCount: tagCount, UserCount: userCount,
		NavigationMenuCount: navigationMenuCount, NavigationItemCount: navigationItemCount, NavigationLocationCount: navigationLocationCount,
		SessionCount: len(visibleSessions), OwnSessionCount: len(ownedSessions),
		MediaCount: mediaSnapshot.Count, MediaBytes: mediaSnapshot.TotalBytes, MediaSize: formatBytes(mediaSnapshot.TotalBytes),
		CommentCount: commentCount, PendingComments: pendingComments, Checks: checks,
	}
}
