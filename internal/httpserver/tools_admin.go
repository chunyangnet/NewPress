package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"sort"
	"time"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/version"
)

const adminToolsReportTimeout = 2 * time.Second

type adminToolsView struct {
	StorageMode           string
	StorageLabel          string
	ContentPersistence    string
	LocalContentVolatile  bool
	LocalBackupAvailable  bool
	MaxUpload             string
	AutoMigrate           bool
	SeedDemo              bool
	SiteSettingsRevision  string
	SiteSettingsFields    int
	ThemeSettingsRevision string
	ThemeSettingsFields   int
	NavigationRevision    string
	AvailableExports      int
	BackupUnits           []adminToolBackupUnit
	RestoreSteps          []adminToolRestoreStep
	NavigationImport      adminNavigationImportView
	Audit                 adminAuditView
}

type adminToolBackupUnit struct {
	Index    string
	Name     string
	Artifact string
	Detail   string
	Tone     string
	Required bool
}

type adminToolRestoreStep struct {
	Index  string
	Name   string
	Detail string
}

type supportReport struct {
	Schema        int                        `json:"schema"`
	Kind          string                     `json:"kind"`
	GeneratedAt   time.Time                  `json:"generated_at"`
	Software      supportReportSoftware      `json:"software"`
	Storage       supportReportStorage       `json:"storage"`
	Revisions     supportReportRevisions     `json:"revisions"`
	Counts        supportReportCounts        `json:"counts"`
	Configuration supportReportConfiguration `json:"configuration"`
	Readiness     supportReportReadiness     `json:"readiness"`
	Checks        []supportReportCheck       `json:"checks"`
}

type supportReportSoftware struct {
	NewPress supportReportCore  `json:"newpress"`
	Theme    supportReportTheme `json:"theme"`
}

type supportReportCore struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	APIVersion string `json:"api_version"`
	Commit     string `json:"commit,omitempty"`
	BuildDate  string `json:"build_date,omitempty"`
	GoVersion  string `json:"go_version"`
}

type supportReportTheme struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type supportReportStorage struct {
	Mode                       string `json:"mode"`
	ContentTaxonomy            string `json:"content_taxonomy"`
	IdentityCommentsNavigation string `json:"identity_comments_navigation"`
	Media                      string `json:"media"`
	Settings                   string `json:"settings"`
}

type supportReportRevisions struct {
	SiteSettings  supportReportSettingsRevision `json:"site_settings"`
	ThemeSettings supportReportSettingsRevision `json:"theme_settings"`
	Media         string                        `json:"media"`
	Navigation    string                        `json:"navigation"`
}

type supportReportSettingsRevision struct {
	Revision   string `json:"revision"`
	FieldCount int    `json:"field_count"`
}

type supportReportCounts struct {
	Content             int `json:"content"`
	Categories          int `json:"categories"`
	Tags                int `json:"tags"`
	Media               int `json:"media"`
	Comments            int `json:"comments"`
	PendingComments     int `json:"pending_comments"`
	NavigationMenus     int `json:"navigation_menus"`
	NavigationItems     int `json:"navigation_items"`
	NavigationLocations int `json:"navigation_locations"`
	Users               int `json:"users"`
	Sessions            int `json:"sessions"`
	Plugins             int `json:"plugins"`
	ActivePlugins       int `json:"active_plugins"`
	Themes              int `json:"themes"`
}

type supportReportConfiguration struct {
	MaxUploadBytes         int64 `json:"max_upload_bytes"`
	AutoMigrate            bool  `json:"auto_migrate"`
	SeedDemo               bool  `json:"seed_demo"`
	AdminConfigured        bool  `json:"admin_configured"`
	PersistentUsers        bool  `json:"persistent_users"`
	PublicPostsAPIEnabled  bool  `json:"public_posts_api_enabled"`
	AdminContentAPIEnabled bool  `json:"admin_content_api_enabled"`
	MaintenanceMode        bool  `json:"maintenance_mode"`
	TrustedProxyCIDRCount  int   `json:"trusted_proxy_cidr_count"`
}

type supportReportReadiness struct {
	Overall  string `json:"overall"`
	Passed   int    `json:"passed"`
	Warnings int    `json:"warnings"`
	Errors   int    `json:"errors"`
}

type supportReportCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type navigationExportDocument struct {
	Schema        int                            `json:"schema"`
	Kind          string                         `json:"kind"`
	ExportedAt    time.Time                      `json:"exported_at"`
	Source        navigationExportSource         `json:"source"`
	Revision      string                         `json:"revision"`
	LocationSlots []navigationExportLocationSlot `json:"location_slots"`
	Menus         []navigationExportMenu         `json:"menus"`
}

type navigationExportSource struct {
	ThemeSlug    string `json:"theme_slug"`
	ThemeVersion string `json:"theme_version"`
}

type navigationExportLocationSlot struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	MenuSlug string `json:"menu_slug"`
}

type navigationExportMenu struct {
	Name        string                 `json:"name"`
	Slug        string                 `json:"slug"`
	Description string                 `json:"description,omitempty"`
	Locations   []string               `json:"locations"`
	Items       []navigationExportItem `json:"items"`
}

type navigationExportItem struct {
	Key          string           `json:"key"`
	ParentKey    string           `json:"parent_key"`
	Position     int              `json:"position"`
	Type         string           `json:"type"`
	ObjectID     int64            `json:"object_id"`
	Label        string           `json:"label"`
	URL          string           `json:"url"`
	Description  string           `json:"description,omitempty"`
	Target       string           `json:"target"`
	Rel          string           `json:"rel"`
	Icon         string           `json:"icon"`
	Accent       string           `json:"accent"`
	Presentation string           `json:"presentation"`
	Badge        navigation.Badge `json:"badge,omitzero"`
}

func (s *Server) adminTools(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminPageCapability(
		writer,
		request,
		auth.CapabilitySystemRead,
		auth.CapabilitySettingsManage,
		auth.CapabilityAppearanceManage,
		auth.CapabilityNavigationManage,
	)
	if !ok {
		return
	}
	query, ok := s.strictAPIQuery(writer, request, map[string]bool{
		"notice": true, "audit_cursor": true, "audit_action": true, "audit_object_type": true,
	})
	if !ok {
		return
	}
	auditQuery, err := auditToolsQuery(query)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "audit.invalid_query", "审计筛选或游标不正确", nil)
		return
	}
	s.renderAdminTools(writer, request, http.StatusOK, principal, readOnly, adminNavigationImportView{}, toolsNoticeFromQuery(query.Get("notice")), auditQuery)
}

func (s *Server) renderAdminTools(writer http.ResponseWriter, request *http.Request, status int, principal auth.Principal, readOnly bool, importView adminNavigationImportView, notice adminNotice, auditQueries ...audit.Query) {
	data := s.baseViewFor(writer, "工具与迁移", "生成无敏感信息支持报告，导出可移植配置，并说明备份与恢复范围", "/admin/tools/")
	data.BodyClass = "is-admin is-admin-tools"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("tools", principal, readOnly)
	auditQuery := audit.Query{Limit: adminAuditViewLimit}
	if len(auditQueries) > 0 {
		auditQuery = auditQueries[0]
	}
	data.Tools = s.toolsView(request.Context(), data.Admin, auditQuery)
	data.Tools.NavigationImport = importView
	data.Admin.Notice = notice.Message
	data.Admin.NoticeTone = notice.Tone
	s.render(writer, status, "admin-tools", data)
}

func (s *Server) adminToolsSupportReport(writer http.ResponseWriter, request *http.Request) {
	if _, _, ok := s.requireAdminPageCapability(writer, request, auth.CapabilitySystemRead); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	report := s.buildSupportReport(request.Context())
	if err := writeAdminToolJSONAttachment(writer, "newpress-support-report.json", report); err != nil {
		slog.Error("write support report", "error", err)
	}
}

func (s *Server) adminToolsNavigationExport(writer http.ResponseWriter, request *http.Request) {
	if _, _, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityNavigationManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), adminToolsReportTimeout)
	defer cancel()
	snapshot, err := s.navigation.Snapshot(ctx)
	if err != nil {
		slog.Error("snapshot navigation export", "error", err)
		s.writeAPIError(writer, request, http.StatusServiceUnavailable, "tools.navigation_export_unavailable", "导航配置暂时不可导出", nil)
		return
	}
	document := s.navigationExport(snapshot, time.Now().UTC())
	if err := writeAdminToolJSONAttachment(writer, "newpress-navigation-export.json", document); err != nil {
		slog.Error("write navigation export", "error", err)
	}
}

func (s *Server) toolsView(ctx context.Context, admin adminView, auditQuery audit.Query) adminToolsView {
	siteSettings := s.siteSettings.Snapshot()
	themeSettings := s.themes.SettingsSnapshot()
	navigationRevision := "unavailable"
	reportCtx, cancel := context.WithTimeout(ctx, adminToolsReportTimeout)
	defer cancel()
	if snapshot, err := s.navigation.Snapshot(reportCtx); err == nil {
		navigationRevision = navigationSnapshotRevision(snapshot)
	}
	mode := "local"
	label := "LOCAL JSON + MEMORY"
	contentPersistence := "内容与分类当前驻留内存；重启后会按 seed 策略重建"
	localContentVolatile := true
	if _, persistent := s.repository.(*content.FileRepository); persistent {
		label = "LOCAL PERSISTENT FILES"
		contentPersistence = "文章、页面、分类、标签、修订与自动保存持久保存在 content.json；重启后继续使用"
		localContentVolatile = false
	}
	if s.config.DatabaseURL != "" {
		mode = "postgresql"
		label = "POSTGRESQL + FILE SIDECAR"
		contentPersistence = "内容、分类、身份、评论与导航由 PostgreSQL 持久化"
		localContentVolatile = false
	}
	availableExports := 0
	if admin.Can(auth.CapabilitySystemRead) {
		availableExports += 2
	}
	for _, capability := range []string{
		auth.CapabilitySettingsManage,
		auth.CapabilityAppearanceManage,
		auth.CapabilityNavigationManage,
	} {
		if admin.Can(capability) {
			availableExports++
		}
	}
	view := adminToolsView{
		StorageMode: mode, StorageLabel: label, ContentPersistence: contentPersistence,
		LocalContentVolatile: localContentVolatile, MaxUpload: formatBytes(s.effectiveMaxUploadBytes()),
		LocalBackupAvailable: mode == "local" && !localContentVolatile,
		AutoMigrate:          s.config.AutoMigrate, SeedDemo: s.config.SeedDemo,
		SiteSettingsRevision: siteSettings.Revision, SiteSettingsFields: siteSettings.FieldCount,
		ThemeSettingsRevision: themeSettings.Revision, ThemeSettingsFields: themeSettingsFieldCount(themeSettings.Groups),
		NavigationRevision: navigationRevision, AvailableExports: availableExports,
		BackupUnits: s.adminToolBackupUnits(), RestoreSteps: adminToolRestoreSteps(),
	}
	if admin.Can(auth.CapabilitySystemRead) {
		view.Audit = s.auditToolsView(ctx, auditQuery)
	}
	return view
}

func (s *Server) adminToolBackupUnits() []adminToolBackupUnit {
	if s.config.DatabaseURL != "" {
		return []adminToolBackupUnit{
			{Index: "01", Name: "PostgreSQL 一致性快照", Artifact: "pg_dump / managed snapshot", Detail: "内容、分类、用户与角色、评论正文修订/审计、导航及其版本记录。恢复前必须确认迁移版本兼容。", Tone: "database", Required: true},
			{Index: "02", Name: "站点设置", Artifact: "${NEWPRESS_DATA_DIR}/site-settings.json", Detail: "站点身份、阅读、讨论、API、SEO 与运维策略；与数据库快照分开保存。", Tone: "settings", Required: true},
			{Index: "03", Name: "主题设置", Artifact: "${NEWPRESS_DATA_DIR}/theme-settings/<slug>.json", Detail: "结构化主题值与修订；必须与对应主题版本一起恢复。", Tone: "theme", Required: true},
			{Index: "04", Name: "媒体对象与索引", Artifact: "${NEWPRESS_DATA_DIR}/media/", Detail: "原始对象、内容寻址索引与元数据不在 PostgreSQL 中，必须作为完整目录备份。", Tone: "media", Required: true},
			{Index: "05", Name: "部署制品", Artifact: "image / binary / themes / plugins", Detail: "固定 NewPress、主题和插件版本，使数据库与文件侧车能够在同一运行时重放。", Tone: "deploy", Required: false},
		}
	}
	return []adminToolBackupUnit{
		{Index: "01", Name: "身份与角色", Artifact: "${NEWPRESS_DATA_DIR}/users.json", Detail: "持久账号、角色映射与密码哈希。文件应保持私密，并与公开导出严格分离。", Tone: "database", Required: true},
		{Index: "02", Name: "评论仓库", Artifact: "${NEWPRESS_DATA_DIR}/comments.json", Detail: "评论树、审核状态、来源摘要、站方/编辑事实、乐观锁版本，以及 pre-edit/pre-restore 修订记录。", Tone: "settings", Required: true},
		{Index: "03", Name: "导航仓库", Artifact: "${NEWPRESS_DATA_DIR}/navigation.json", Detail: "菜单、位置分配与持久项目 ID。跨环境迁移优先使用上方可移植导航导出。", Tone: "theme", Required: true},
		{Index: "04", Name: "共享审计记录", Artifact: "${NEWPRESS_DATA_DIR}/audit.jsonl", Detail: "用户、评论和导航的 commit 摘要绑定上述三个事实；必须在同一停止写入窗口或卷快照中成组恢复。", Tone: "audit", Required: true},
		{Index: "05", Name: "站点与主题设置", Artifact: "site-settings.json + theme-settings/", Detail: "核心策略与主题呈现值是两个独立修订域，必须同时保留。", Tone: "settings", Required: true},
		{Index: "06", Name: "媒体对象与索引", Artifact: "${NEWPRESS_DATA_DIR}/media/", Detail: "完整复制目录，避免仅备份索引而丢失原始对象。", Tone: "media", Required: true},
		{Index: "07", Name: "内容与分类", Artifact: "${NEWPRESS_DATA_DIR}/content.json", Detail: "文章、页面、分类、标签、全部修订和身份专属自动保存是一个原子快照；与评论、导航和媒体一起备份。", Tone: "database", Required: true},
		{Index: "08", Name: "私信与通知", Artifact: "${NEWPRESS_DATA_DIR}/messages.json", Detail: "账号之间的私信、通知及已读位置。包含私密正文，需加密保存。", Tone: "settings", Required: true},
		{Index: "09", Name: "账号互动", Artifact: "${NEWPRESS_DATA_DIR}/engagement.json", Detail: "账号收藏夹、点赞和关注关系；与用户和内容 ID 一同恢复。", Tone: "database", Required: true},
		{Index: "10", Name: "部署制品", Artifact: "binary / themes / plugins", Detail: "记录 NewPress、主题与插件精确版本，保留可重现的启动配置。", Tone: "deploy", Required: false},
	}
}

func adminToolRestoreSteps() []adminToolRestoreStep {
	return []adminToolRestoreStep{
		{Index: "01", Name: "身份与角色", Detail: "先恢复登录账号；文件模式须把 users/comments/navigation 与 audit.jsonl 作为同一停止写入快照恢复，再确认至少一位管理员可用。"},
		{Index: "02", Name: "站点与主题设置", Detail: "恢复两个独立修订域，并确保活动主题版本与设置文档匹配。"},
		{Index: "03", Name: "内容与分类", Detail: "恢复 PostgreSQL 或同一快照中的 content.json；文件模式会同时恢复分类、文章、修订与未过期自动保存。"},
		{Index: "04", Name: "评论", Detail: "在内容 ID 稳定后恢复评论树、审核状态、来源摘要、站方/编辑事实与 pre-edit/pre-restore 修订记录。"},
		{Index: "05", Name: "导航", Detail: "恢复原仓库或使用可移植导出重建菜单，再校验主题位置。"},
		{Index: "06", Name: "媒体对象与索引", Detail: "成对恢复对象与索引，抽样验证下载、Range、类型与公开 URL。"},
		{Index: "07", Name: "私信与通知", Detail: "文件模式恢复同一快照中的 messages.json，数据库模式随数据库恢复；确认账号 ID 稳定后检查双方会话和已读状态。"},
		{Index: "08", Name: "账号互动", Detail: "恢复 engagement.json 或数据库中的 np_engagement；验证收藏夹、关注动态及其他账号隔离。"},
		{Index: "09", Name: "就绪与支持报告复核", Detail: "执行 /health/ready，下载新支持报告，对比计数、修订与完整性检查。"},
	}
}

func (s *Server) buildSupportReport(parent context.Context) supportReport {
	ctx, cancel := context.WithTimeout(parent, adminToolsReportTimeout)
	defer cancel()

	contentResult, contentListErr := s.repository.ListAdmin(ctx, content.AdminQuery{Limit: 1})
	termResult, termListErr := s.repository.ListTerms(ctx, content.TermQuery{Limit: 1})
	contentReadyErr := s.repository.Ready(ctx)
	mediaSnapshot := s.media.Snapshot()
	mediaReadyErr := s.media.Ready()
	commentSnapshot, commentSnapshotErr := s.comments.Snapshot(ctx)
	commentReadyErr := s.comments.Ready(ctx)
	navigationSnapshot, navigationSnapshotErr := s.navigation.Snapshot(ctx)
	navigationReadyErr := s.navigation.Ready(ctx)

	userCount := 0
	identityReady := true
	if s.auth.PersistentUsers() {
		users, err := s.auth.ListUsers(ctx, auth.UserQuery{Limit: 1})
		if err != nil {
			identityReady = false
		} else {
			userCount = users.Total
		}
	} else if s.auth.Configured() {
		userCount = 1
	}

	sessionCount, sessionErr := s.auth.ActiveSessionCountContext(ctx)
	plugins, pluginsErr := s.plugins.List()
	themes, themesErr := s.themes.List()
	activePlugins := 0
	for _, installed := range plugins {
		if installed.Enabled && installed.Error == "" {
			activePlugins++
		}
	}

	contentCount := 0
	if contentListErr == nil {
		contentCount = contentResult.Total
	}
	categoryCount := 0
	tagCount := 0
	if termListErr == nil {
		categoryCount = termResult.Counts[content.TaxonomyCategory]
		tagCount = termResult.Counts[content.TaxonomyTag]
	}
	commentCount := 0
	pendingComments := 0
	if commentSnapshotErr == nil {
		commentCount = commentSnapshot.Total
		pendingComments = commentSnapshot.StatusCounts[comment.StatusPending]
	}
	navigationMenus := 0
	navigationItems := 0
	navigationLocations := 0
	navigationRevision := "unavailable"
	if navigationSnapshotErr == nil {
		navigationMenus = navigationSnapshot.MenuCount
		navigationItems = navigationSnapshot.ItemCount
		navigationLocations = len(navigationSnapshot.Locations)
		navigationRevision = navigationSnapshotRevision(navigationSnapshot)
	}

	storage := supportReportStorage{
		Mode: "local", ContentTaxonomy: "memory", IdentityCommentsNavigation: "json_files",
		Media: "filesystem", Settings: "json_files",
	}
	if _, persistent := s.repository.(*content.FileRepository); persistent {
		storage.ContentTaxonomy = "json_file"
	}
	if s.config.DatabaseURL != "" {
		storage.Mode = "postgresql"
		storage.ContentTaxonomy = "postgresql"
		storage.IdentityCommentsNavigation = "postgresql"
	}

	siteSettings := s.siteSettings.Snapshot()
	themeSettings := s.themes.SettingsSnapshot()
	activeTheme := s.themes.Active()
	settings := siteSettings.Settings
	checks := []supportReportCheck{
		supportCheck("content_repository", "内容仓库", contentListErr == nil && contentReadyErr == nil, "内容列表与就绪探针"),
		supportCheck("taxonomy_repository", "分类与标签", termListErr == nil && contentReadyErr == nil, "分类计数与内容依赖"),
		supportCheck("identity_repository", "身份与角色", identityReady, "仅验证仓库可读性，不输出身份字段"),
		supportCheck("session_repository", "登录会话", sessionErr == nil, "仅验证活动会话总数，不输出会话标识或认证材料"),
		supportCheck("media_library", "媒体资源", mediaReadyErr == nil, "对象目录与索引"),
		supportCheck("comment_repository", "评论仓库", commentSnapshotErr == nil && commentReadyErr == nil, "评论快照与就绪探针"),
		supportCheck("navigation_repository", "导航仓库", navigationSnapshotErr == nil && navigationReadyErr == nil, "菜单快照与就绪探针"),
		supportCheck("theme_registry", "主题注册表", themesErr == nil, "活动主题与已安装主题清单"),
		supportCheck("plugin_registry", "插件注册表", pluginsErr == nil, "插件清单与启用状态"),
	}
	if storage.Mode == "local" {
		if storage.ContentTaxonomy == "json_file" {
			checks = append(checks, supportCheck("content_persistence", "内容持久化", contentReadyErr == nil, "文章、分类、修订与自动保存已写入本地原子快照"))
		} else {
			checks = append(checks, supportReportCheck{Key: "content_persistence", Label: "内容持久化", Status: "warning", Detail: "当前测试仓库驻留内存"})
		}
	}
	if !s.auth.Configured() {
		checks = append(checks, supportReportCheck{Key: "admin_authentication", Label: "管理认证", Status: "warning", Detail: "尚未配置管理身份，仅允许回环地址只读预览"})
	} else {
		checks = append(checks, supportReportCheck{Key: "admin_authentication", Label: "管理认证", Status: "pass", Detail: "管理身份与会话保护已启用"})
	}
	readiness := summarizeSupportChecks(checks)

	return supportReport{
		Schema: 1, Kind: "newpress.support_report", GeneratedAt: time.Now().UTC(),
		Software: supportReportSoftware{
			NewPress: supportReportCore{
				Name: version.Name, Version: version.Version, APIVersion: version.APIVersion,
				Commit: version.Commit, BuildDate: version.BuildDate, GoVersion: runtime.Version(),
			},
			Theme: supportReportTheme{Slug: activeTheme.Slug, Name: activeTheme.Name, Version: activeTheme.Version},
		},
		Storage: storage,
		Revisions: supportReportRevisions{
			SiteSettings:  supportReportSettingsRevision{Revision: siteSettings.Revision, FieldCount: siteSettings.FieldCount},
			ThemeSettings: supportReportSettingsRevision{Revision: themeSettings.Revision, FieldCount: themeSettingsFieldCount(themeSettings.Groups)},
			Media:         mediaSnapshot.Revision, Navigation: navigationRevision,
		},
		Counts: supportReportCounts{
			Content: contentCount, Categories: categoryCount, Tags: tagCount,
			Media: mediaSnapshot.Count, Comments: commentCount, PendingComments: pendingComments,
			NavigationMenus: navigationMenus, NavigationItems: navigationItems, NavigationLocations: navigationLocations,
			Users: userCount, Sessions: sessionCount,
			Plugins: len(plugins), ActivePlugins: activePlugins, Themes: len(themes),
		},
		Configuration: supportReportConfiguration{
			MaxUploadBytes: s.effectiveMaxUploadBytes(), AutoMigrate: s.config.AutoMigrate, SeedDemo: s.config.SeedDemo,
			AdminConfigured: s.auth.Configured(), PersistentUsers: s.auth.PersistentUsers(),
			PublicPostsAPIEnabled: settings.API.PublicPostsEnabled, AdminContentAPIEnabled: settings.API.AdminContentEnabled,
			MaintenanceMode:       settings.Operations.MaintenanceEnabled,
			TrustedProxyCIDRCount: len(s.config.TrustedProxyCIDRs),
		},
		Readiness: readiness,
		Checks:    checks,
	}
}

func supportCheck(key, label string, passed bool, detail string) supportReportCheck {
	status := "error"
	if passed {
		status = "pass"
	}
	return supportReportCheck{Key: key, Label: label, Status: status, Detail: detail}
}

func summarizeSupportChecks(checks []supportReportCheck) supportReportReadiness {
	result := supportReportReadiness{Overall: "pass"}
	for _, check := range checks {
		switch check.Status {
		case "pass":
			result.Passed++
		case "warning":
			result.Warnings++
		default:
			result.Errors++
		}
	}
	if result.Errors > 0 {
		result.Overall = "error"
	} else if result.Warnings > 0 {
		result.Overall = "warning"
	}
	return result
}

func (s *Server) navigationExport(snapshot navigation.Snapshot, exportedAt time.Time) navigationExportDocument {
	menus := append([]navigation.Menu(nil), snapshot.Menus...)
	sort.SliceStable(menus, func(left, right int) bool {
		if menus[left].Slug != menus[right].Slug {
			return menus[left].Slug < menus[right].Slug
		}
		return menus[left].Name < menus[right].Name
	})
	menuSlugs := make(map[int64]string, len(menus))
	for _, menu := range menus {
		menuSlugs[menu.ID] = menu.Slug
	}
	slots := make([]navigationExportLocationSlot, 0, len(navigation.Locations()))
	for _, location := range navigation.Locations() {
		slots = append(slots, navigationExportLocationSlot{
			Key: location, Label: navigation.LocationLabel(location), MenuSlug: menuSlugs[snapshot.Locations[location]],
		})
	}
	exportMenus := make([]navigationExportMenu, 0, len(menus))
	for _, menu := range menus {
		flat := navigation.Flatten(menu)
		itemKeys := make(map[int64]string, len(flat))
		for index, flattened := range flat {
			itemKeys[flattened.Item.ID] = navigationExportItemKey(index)
		}
		items := make([]navigationExportItem, 0, len(flat))
		siblingPositions := make(map[int64]int, len(flat))
		for index, flattened := range flat {
			item := flattened.Item
			position := siblingPositions[item.ParentID]
			siblingPositions[item.ParentID] = position + 1
			items = append(items, navigationExportItem{
				Key: navigationExportItemKey(index), ParentKey: itemKeys[item.ParentID],
				Position: position, Type: item.Type, ObjectID: item.ObjectID,
				Label: item.Label, URL: item.URL, Description: item.Description,
				Target: item.Target, Rel: item.Rel, Icon: item.Icon, Accent: item.Accent,
				Presentation: item.Presentation, Badge: item.Badge,
			})
		}
		exportMenus = append(exportMenus, navigationExportMenu{
			Name: menu.Name, Slug: menu.Slug, Description: menu.Description,
			Locations: append([]string(nil), menu.Locations...), Items: items,
		})
	}
	activeTheme := s.themes.Active()
	return navigationExportDocument{
		Schema: 1, Kind: "newpress.navigation_export", ExportedAt: exportedAt.UTC(),
		Source:   navigationExportSource{ThemeSlug: activeTheme.Slug, ThemeVersion: activeTheme.Version},
		Revision: navigationSnapshotRevision(snapshot), LocationSlots: slots, Menus: exportMenus,
	}
}

func navigationExportItemKey(index int) string {
	return fmt.Sprintf("item-%03d", index+1)
}

func themeSettingsFieldCount(groups []theme.SettingGroup) int {
	count := 0
	for _, group := range groups {
		count += len(group.Fields)
	}
	return count
}

func toolsNoticeFromQuery(value string) adminNotice {
	switch value {
	case "navigation-imported":
		return adminNotice{Message: "导航导入已通过快照并发检查并原子替换，公开主题已切换到新修订", Tone: "success"}
	default:
		return adminNotice{}
	}
}

func writeAdminToolJSONAttachment(writer http.ResponseWriter, filename string, value any) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Pragma", "no-cache")
	writer.Header().Set("Expires", "0")
	_, err := writer.Write(encoded.Bytes())
	return err
}
