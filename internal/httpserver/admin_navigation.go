package httpserver

import "newpress.local/newpress/internal/module"

// Admin navigation is shared by the sidebar, breadcrumb and command search.
// URLs and capability checks stay independent of the visual grouping.
type adminNavItem struct {
	ID, Title, URL, Icon string
	Active               bool
	Badge                int
}

type adminNavGroup struct {
	ID, Title, Icon string
	Active          bool
	Items           []adminNavItem
}

func (view adminView) Navigation() []adminNavGroup {
	current := view.Current
	if current == "modules" {
		current = "settings"
	}
	groups := []adminNavGroup{}
	add := func(id, title, icon string, items ...adminNavItem) {
		group := adminNavGroup{ID: id, Title: title, Icon: icon}
		for _, item := range items {
			if item.URL == "" {
				continue
			}
			item.Active = current == item.ID && (item.ID != "plugins" || view.PluginSlug == "") || current == "plugins" && view.PluginSlug != "" && item.ID == "plugin:"+view.PluginSlug
			group.Active = group.Active || item.Active
			group.Items = append(group.Items, item)
		}
		if len(group.Items) > 0 {
			groups = append(groups, group)
		}
	}
	item := func(id, title, url, icon string, capabilities ...string) adminNavItem {
		if id == "circles" && view.CanManageCircles {
			capabilities = nil
		}
		if id == "layout" && !view.LayoutAvailable {
			return adminNavItem{}
		}
		for _, capability := range capabilities {
			if !view.Can(capability) {
				return adminNavItem{}
			}
		}
		if id == "users" && !view.PersistentUsers {
			return adminNavItem{}
		}
		if current != id && !module.Allowed(view.SiteSettings.Settings.Modules, module.Owner(url)) {
			return adminNavItem{}
		}
		entry := adminNavItem{ID: id, Title: title, URL: url, Icon: icon}
		if id == "comments" {
			entry.Badge = view.PendingComments
		}
		return entry
	}
	add("overview", "工作台", "gauge",
		item("dashboard", "工作台", "/admin/", "gauge", "dashboard:read"))
	add("content", "内容", "newspaper",
		item("content", "文章与页面", "/admin/content/", "newspaper", "content:read"),
		item("flash", "快报管理", "/admin/flash/", "newspaper", "content:write"),
		item("announcements", "公告管理", "/admin/announcements/", "megaphone", "content:write"),
		item("media", "媒体库", "/admin/media/", "image", "media:read"),
		item("taxonomy", "分类与标签", "/admin/taxonomy/", "tags", "taxonomy:manage"),
		item("series", "专题与系列", "/admin/series/", "panels-top-left", "taxonomy:manage"),
		item("snippets", "共享片段", "/admin/snippets/", "panels-top-left", "content:read"),
		item("wechat", "微信公众号", "/admin/wechat/", "send", "settings:manage"),
		item("import", "内容导入", "/admin/import/", "file-archive", "settings:manage", "content:write", "taxonomy:manage"))
	add("interaction", "社区", "message-circle",
		item("questions", "问答管理", "/admin/questions/", "circle-help", "comments:moderate"),
		item("circles", "圈子工作台", "/admin/circles/", "users", "settings:manage"),
		item("comments", "评论", "/admin/comments/", "message-circle", "comments:moderate"))
	add("users", "用户", "users",
		item("users", "用户与权限", "/admin/users/", "users", "users:read"),
		item("registration-codes", "注册码工作台", "/admin/registration-codes/", "key-round", "settings:manage"),
		item("progression", "成长与会员", "/admin/progression/", "award", "settings:manage"),
		item("recognition", "互动与身份", "/admin/recognition/", "award", "settings:manage"))
	add("commerce", "交易", "shopping-bag",
		item("payments", "支付与订单", "/admin/payments/", "wallet", "settings:manage"),
		item("shop", "商城管理", "/admin/shop/", "shopping-bag", "settings:manage"),
		item("currency", "虚拟币与奖励", "/admin/currency/", "coins", "settings:manage"))
	add("operations", "运营", "activity",
		item("ai-workbench", "AI 工作台", "/admin/ai/", "sparkles", "system:read"),
		item("analytics", "访问统计", "/admin/analytics/", "activity", "system:read"),
		item("ai-usage", "AI 消耗", "/admin/analytics/ai/", "cpu", "system:read"),
		item("ai-requests", "请求明细", "/admin/analytics/ai/requests/", "list", "system:read"))
	add("site", "站点", "panels-top-left",
		item("layout", "可视化布局", "/admin/appearance/layout/", "panels-top-left", "appearance:manage"),
		item("appearance", "外观与主题", "/admin/appearance/", "diamond", "appearance:manage"),
		item("navigation", "导航菜单", "/admin/navigation/", "route", "navigation:manage"),
		item("features", "功能页面", "/admin/features/", "plus", "settings:manage"),
		item("seo", "搜索与链接", "/admin/seo/", "scan-search", "settings:manage"),
		item("settings", "站点设置", "/admin/settings/", "sliders-horizontal", "settings:manage"))
	pluginItems := []adminNavItem{item("themes", "主题市场", "/admin/themes/", "panels-top-left", "appearance:manage"), item("plugins", "插件管理", "/admin/plugins/", "code-2", "plugins:manage")}
	for _, page := range view.PluginSettings {
		pluginItems = append(pluginItems, item("plugin:"+page.Slug, page.Title, page.URL, "sliders-horizontal", "plugins:manage"))
	}
	add("plugins", "扩展", "code-2", pluginItems...)
	tools := adminNavItem{}
	for _, cap := range []string{"system:read", "settings:manage", "appearance:manage", "navigation:manage"} {
		if view.Can(cap) {
			tools = item("tools", "工具与备份", "/admin/tools/", "database")
			break
		}
	}
	add("system", "系统", "server",
		tools,
		item("system", "系统状态", "/admin/system/", "server", "system:read"),
		item("updates", "系统更新", "/admin/updates/", "refresh-cw", "system:update"))
	return groups
}

func (view adminView) Breadcrumb() adminNavGroup {
	for _, group := range view.Navigation() {
		if group.Active {
			return group
		}
	}
	return adminNavGroup{Title: "账户"}
}

func (view adminView) PageTitle() string {
	if view.DetailTitle != "" {
		return view.DetailTitle
	}
	for _, group := range view.Navigation() {
		for _, item := range group.Items {
			if item.Active {
				return item.Title
			}
		}
	}
	if view.Current == "sessions" {
		return "登录与设备"
	}
	return "后台"
}
