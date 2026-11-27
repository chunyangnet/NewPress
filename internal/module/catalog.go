// Package module declares the installation's business boundaries. It has no
// storage or HTTP dependencies; configuration, navigation and request routing
// consume the same contract.
package module

import (
	"fmt"
	"slices"
	"strings"
)

const Version = 1

type Definition struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Group       string   `json:"group"`
	Control     string   `json:"control"` // core, native, switch, reserved
	ConfigURL   string   `json:"config_url,omitempty"`
	Capability  string   `json:"capability,omitempty"`
	DependsOn   []string `json:"depends_on,omitempty"`
	Paths       []string `json:"paths,omitempty"`
	Contract    string   `json:"contract,omitempty"`
}

var definitions = []Definition{
	{ID: "questions", Title: "问答", Description: "全站提问、回答、采纳与内容治理，不依赖圈子。", Group: "互动", Control: "switch", ConfigURL: "/admin/questions/", Capability: "comments:moderate", DependsOn: []string{"accounts"}, Paths: []string{"/questions", "/admin/questions", "/api/v1/questions"}},
	{ID: "flash", Title: "快报", Description: "短资讯投稿、审核、发布与阅读。", Group: "内容", Control: "switch", ConfigURL: "/admin/flash/", Capability: "content:write", DependsOn: []string{"content", "accounts"}, Paths: []string{"/flash", "/admin/flash", "/api/v1/flash", "/api/v1/admin/flash"}},
	{ID: "platform", Title: "运行平台", Description: "健康检查、系统状态、备份与更新。", Group: "基础", Control: "core", ConfigURL: "/admin/system/", Capability: "system:read", Paths: []string{"/health", "/api/v1/system", "/admin", "/api/v1/admin/updates", "/api/v1/admin/system", "/api/v1/admin/audit-events"}},
	{ID: "site", Title: "站点配置", Description: "身份、阅读、维护模式和模块配置，使用修订校验保存。", Group: "基础", Control: "core", ConfigURL: "/admin/settings/", Capability: "settings:manage", Paths: []string{"/admin/settings", "/admin/modules", "/api/v1/admin/modules", "/api/v1/admin/verification", "/api/v1/verification"}},
	{ID: "content", Title: "内容与分类", Description: "文章、页面、分类、标签、修订、自动保存与公开内容 API。", Group: "内容", Control: "core", ConfigURL: "/admin/content/", Capability: "content:read", Paths: []string{"/posts", "/pages", "/categories", "/tags", "/authors", "/explore", "/api/v1/posts", "/api/v1/admin/contents", "/admin/content", "/admin/announcements", "/announcements", "/admin/quick-draft", "/admin/taxonomy"}},
	{ID: "media", Title: "媒体资源", Description: "上传、处理、引用检查与回收站。", Group: "内容", Control: "core", ConfigURL: "/admin/media/", Capability: "media:read", Paths: []string{"/media", "/admin/media", "/api/v1/admin/media"}},
	{ID: "accounts", Title: "账号与权限", Description: "登录、注册、邀请、恢复、用户资料、角色与会话。", Group: "基础", Control: "core", ConfigURL: "/admin/users/", Capability: "users:read", Paths: []string{"/account", "/admin/users", "/admin/registration-codes", "/api/v1/admin/registration-codes", "/admin/sessions", "/admin/login", "/admin/logout", "/api/v1/social", "/api/v1/admin/session", "/api/v1/admin/sessions", "/api/v1/admin/invitations"}},
	{ID: "themes", Title: "主题与布局", Description: "主题参数、可视化布局、隔离预览与主题更新。", Group: "设计", Control: "core", ConfigURL: "/admin/appearance/", Capability: "appearance:manage", DependsOn: []string{"content"}, Paths: []string{"/admin/appearance", "/theme-assets", "/theme-settings.css", "/api/v1/themes"}},
	{ID: "navigation", Title: "导航菜单", Description: "主导航、工具导航、手机导航与页脚菜单。", Group: "设计", Control: "core", ConfigURL: "/admin/navigation/", Capability: "navigation:manage", Paths: []string{"/admin/navigation", "/navigation.css", "/api/v1/navigation", "/api/v1/admin/navigation"}},
	{ID: "plugins", Title: "插件扩展", Description: "签名市场、独立启停、配置、WASM 执行与回滚。", Group: "扩展", Control: "native", ConfigURL: "/admin/plugins/", Capability: "plugins:manage", Paths: []string{"/admin/plugins", "/api/v1/plugins", "/api/v1/admin/plugins", "/api/v1/admin/extensions"}, Contract: "wasm/core-v1: content.render, admin.action; settings.get/set, cache.get/set/delete, content.get, http.get"},
	{ID: "seo", Title: "搜索与链接", Description: "搜索可见性、重定向、站点地图与 RSS。", Group: "内容", Control: "native", ConfigURL: "/admin/seo/", Capability: "settings:manage", DependsOn: []string{"content"}, Paths: []string{"/admin/seo", "/api/v1/admin/seo", "/api/v1/admin/redirects", "/robots.txt", "/sitemap.xml", "/feed.xml"}},
	{ID: "comments", Title: "评论", Description: "公开评论、回复、审核、编辑与修订；停用后保留历史数据。", Group: "互动", Control: "switch", ConfigURL: "/admin/comments/", Capability: "comments:moderate", DependsOn: []string{"content"}, Paths: []string{"/admin/comments", "/api/v1/admin/comments", "/posts/{slug}/comments", "/posts/id/{id}/comments", "/posts/{year}/{month}/{slug}/comments", "/posts/{year}/{month}/{day}/{slug}/comments", "/api/v1/posts/{slug}/comments"}},
	{ID: "series", Title: "专题与系列", Description: "专题索引、文章编排与阅读页串联。", Group: "内容", Control: "switch", ConfigURL: "/admin/series/", Capability: "taxonomy:manage", DependsOn: []string{"content"}, Paths: []string{"/series", "/admin/series", "/api/v1/series", "/api/v1/admin/series"}},
	{ID: "community", Title: "圈子", Description: "圈子、成员、图文讨论、通知与举报；同时需要服务器启用圈子存储。", Group: "互动", Control: "switch", ConfigURL: "/admin/circles/", DependsOn: []string{"accounts"}, Paths: []string{"/community", "/circles", "/admin/circles", "/api/v1/community", "/api/v1/circles", "/api/v1/social/circles"}},
	{ID: "feedback", Title: "反馈页面", Description: "反馈板块、建议、问题、状态与导出。", Group: "互动", Control: "switch", ConfigURL: "/admin/features/", Capability: "settings:manage", Paths: []string{"/feedback", "/api/v1/feedback"}},
	{ID: "links", Title: "链接页面", Description: "分类链接、图标与链接信息采集。", Group: "内容", Control: "switch", ConfigURL: "/admin/features/links/", Capability: "settings:manage", Paths: []string{"/links", "/admin/features/links", "/api/v1/admin/link-pages", "/api/v1/admin/link-metadata"}},
	{ID: "engagement", Title: "收藏与关注", Description: "文章收藏、点赞、作者关注及个人阅读库。", Group: "互动", Control: "core", ConfigURL: "/account/", DependsOn: []string{"accounts", "content"}, Paths: []string{"/api/v1/social/engagement", "/api/v1/social/library", "/api/v1/social/import"}},
	{ID: "messaging", Title: "私信", Description: "联系人、会话、消息与已读状态；关闭后隐藏所有私信入口，保留历史记录。", Group: "互动", Control: "switch", ConfigURL: "/account/", DependsOn: []string{"accounts"}, Paths: []string{"/api/v1/social/contacts", "/api/v1/social/inbox", "/api/v1/social/messages", "/api/v1/social/notifications"}},
	{ID: "notifications", Title: "通知与邮件", Description: "动态通知、邮件偏好、退订、投递重试与事件处理。", Group: "互动", Control: "native", ConfigURL: "/account/?view=notifications", DependsOn: []string{"accounts"}, Paths: []string{"/api/v1/social/notice-center", "/api/v1/social/notification-preferences", "/api/v1/social/notification-mail", "/account/notifications"}},
	{ID: "currency", Title: "虚拟币与奖励", Description: "各货币独立配置、转账、签到与发放规则，沿用账本校验。", Group: "互动", Control: "native", ConfigURL: "/admin/currency/", Capability: "settings:manage", DependsOn: []string{"accounts"}, Paths: []string{"/admin/currency", "/api/v1/admin/currency", "/api/v1/admin/currencies", "/api/v1/social/wallet", "/api/v1/social/wallets"}},
	{ID: "progression", Title: "成长与会员", Description: "经验、等级、会员方案和签到使用独立业务配置。", Group: "互动", Control: "native", ConfigURL: "/admin/progression/", Capability: "settings:manage", DependsOn: []string{"accounts", "currency"}, Paths: []string{"/admin/progression", "/api/v1/admin/progression", "/api/v1/social/progression"}},
	{ID: "recognition", Title: "互动与身份", Description: "文章打赏、个人头衔、勋章佩戴与人工认证审核。", Group: "互动", Control: "native", ConfigURL: "/admin/recognition/", Capability: "settings:manage", DependsOn: []string{"accounts", "currency"}, Paths: []string{"/admin/recognition", "/api/v1/admin/recognition", "/api/v1/social/recognition", "/api/v1/social/tips"}},
	{ID: "payments", Title: "支付与订单", Description: "支付渠道、钱包充值、会员开通、币种兑换与可恢复订单。", Group: "运营", Control: "native", ConfigURL: "/admin/payments/", Capability: "settings:manage", DependsOn: []string{"accounts", "currency", "progression"}, Paths: []string{"/admin/payments", "/billing", "/api/v1/admin/payments", "/api/v1/social/billing", "/api/v1/payments"}},
	{ID: "analytics", Title: "运营统计", Description: "访问与 AI 用量报表；AI 采集开关在 AI 接口设置中单独控制。", Group: "运营", Control: "switch", ConfigURL: "/admin/analytics/", Capability: "system:read", Paths: []string{"/admin/analytics", "/api/v1/admin/analytics", "/api/v1/admin/ai/usage"}},
	{ID: "wechat", Title: "微信公众号", Description: "公众号配置、采集和同步；暂停期间不领取新同步任务。", Group: "运营", Control: "switch", ConfigURL: "/admin/wechat/", Capability: "settings:manage", DependsOn: []string{"content", "media"}, Paths: []string{"/admin/wechat", "/api/v1/admin/wechat"}},
	{ID: "ai", Title: "AI 助手", Description: "写作、生图、运维诊断、运营总结与报告工作台。", Group: "扩展", Control: "switch", ConfigURL: "/admin/ai/", Capability: "system:read", DependsOn: []string{"content"}, Paths: []string{"/admin/ai", "/api/v1/admin/ai"}},
	{ID: "imports", Title: "内容导入", Description: "导入预检、资源解析与正式应用。", Group: "内容", Control: "switch", ConfigURL: "/admin/import/", Capability: "settings:manage", DependsOn: []string{"content", "media"}, Paths: []string{"/admin/import", "/api/v1/admin/imports"}},
	{ID: "plugin-content-write", Title: "插件内容写入", Description: "预留版本检查、权限、审计与事务接口，当前不能启用。", Group: "预留", Control: "reserved", DependsOn: []string{"plugins", "content"}, Contract: "reserved: content.write/v1; expected_version + actor + idempotency_key"},
	{ID: "plugin-jobs", Title: "插件后台任务", Description: "预留任务取消、配额、幂等与重试协议，当前没有执行器。", Group: "预留", Control: "reserved", DependsOn: []string{"plugins"}, Contract: "reserved: jobs.enqueue/v1; task_type + payload + idempotency_key"},
	{ID: "plugin-admin-nav", Title: "插件自定义页面", Description: "预留声明式后台页面与导航槽，当前仅开放已验证的配置页。", Group: "预留", Control: "reserved", DependsOn: []string{"plugins", "accounts"}, Contract: "reserved: admin.navigation/v1; route + capability + schema"},
	{ID: "plugin-users", Title: "插件用户 API", Description: "预留字段范围、用途授权与审计边界，当前不提供任意用户访问。", Group: "预留", Control: "reserved", DependsOn: []string{"plugins", "accounts"}, Contract: "reserved: users.scoped/v1; fields + purpose + actor"},
	{ID: "page-layouts", Title: "独立页面画布", Description: "预留文章页、账号页与业务页面的独立布局；目前画布作用于首页。", Group: "预留", Control: "reserved", DependsOn: []string{"themes"}, Contract: "reserved: layout.page/v1; page_type + regions + blocks"},
}

func Catalog() []Definition {
	result := slices.Clone(definitions)
	for i := range result {
		result[i].Paths = slices.Clone(result[i].Paths)
		result[i].DependsOn = slices.Clone(result[i].DependsOn)
	}
	return result
}
func Lookup(id string) (Definition, bool) {
	for _, d := range definitions {
		if d.ID == id {
			d.Paths = slices.Clone(d.Paths)
			d.DependsOn = slices.Clone(d.DependsOn)
			return d, true
		}
	}
	return Definition{}, false
}

// Allowed describes configured availability; runtime requirements remain owned
// by each domain. Native settings are never overwritten by a second switch.
func Allowed(overrides map[string]bool, id string) bool {
	d, ok := Lookup(id)
	if !ok || d.Control == "reserved" {
		return false
	}
	if enabled, exists := overrides[id]; d.Control == "switch" && exists && !enabled {
		return false
	}
	for _, dep := range d.DependsOn {
		if !Allowed(overrides, dep) {
			return false
		}
	}
	return true
}
func Validate(overrides map[string]bool) map[string]string {
	errors := map[string]string{}
	for id := range overrides {
		d, ok := Lookup(id)
		if !ok {
			errors[id] = "未知模块"
		} else if d.Control != "switch" {
			errors[id] = "此模块不支持总开关，请使用它的配置入口"
		}
	}
	return errors
}

// Owner uses longest path-segment matching, including ServeMux placeholders.
// It never treats /series-other as part of /series.
func Owner(pattern string) string {
	_, target, ok := strings.Cut(pattern, " ")
	if !ok {
		target = pattern
	}
	owner, longest := "platform", 0
	for _, d := range definitions {
		for _, p := range d.Paths {
			if (target == p || strings.HasPrefix(target, p+"/")) && len(p) > longest {
				owner, longest = d.ID, len(p)
			}
		}
	}
	return owner
}
func CheckCatalog() error {
	seen, paths := map[string]bool{}, map[string]bool{}
	for _, d := range definitions {
		if d.ID == "" || seen[d.ID] {
			return fmt.Errorf("duplicate module %q", d.ID)
		}
		seen[d.ID] = true
		for _, p := range d.Paths {
			if paths[p] {
				return fmt.Errorf("duplicate module path %q", p)
			}
			paths[p] = true
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("module dependency cycle at %s", id)
		}
		if visited[id] {
			return nil
		}
		d, ok := Lookup(id)
		if !ok {
			return fmt.Errorf("unknown module dependency %s", id)
		}
		visiting[id] = true
		for _, dep := range d.DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for _, d := range definitions {
		if err := visit(d.ID); err != nil {
			return err
		}
	}
	return nil
}
