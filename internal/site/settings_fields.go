package site

import (
	"strconv"
)

func settingGroups(settings Settings) []SettingGroup {
	postStyle, pageStyle, slugMode := settings.Writing.PostPermalink, settings.Writing.PagePermalink, settings.Writing.SlugMode
	if postStyle == "" {
		postStyle = "slug"
	}
	if pageStyle == "" {
		pageStyle = "slug"
	}
	if slugMode == "" {
		slugMode = "smart"
	}
	groups := []SettingGroup{
		{ID: "permalinks", Title: "固定链接与别名", Icon: "link", Description: "设置文章和页面的公开地址。切换结构后，旧结构仍会转向当前地址。", Fields: []SettingField{
			optionField("slug_mode", "permalinks", "英文别名", "智能模式会先整理英文标题；中文标题使用已配置的默认 AI 模型生成英文别名。没有模型时使用稳定的字母数字标识，手动别名优先。", "select", slugMode, []SettingOption{{"smart", "智能生成英文别名", ""}, {"manual", "手动填写", ""}}),
			optionField("post_permalink", "permalinks", "文章固定链接", "日期结构使用发布时间（站点时区）。编号结构不随标题或别名变化。", "select", postStyle, []SettingOption{{"slug", "别名 · /posts/hello-world", ""}, {"id", "编号 · /posts/id/123", ""}, {"month", "年月与别名 · /posts/2026/11/hello-world", ""}, {"date", "日期与别名 · /posts/2026/11/26/hello-world", ""}}),
			optionField("page_permalink", "permalinks", "页面固定链接", "独立页面可以使用别名或固定编号。", "select", pageStyle, []SettingOption{{"slug", "别名 · /pages/about", ""}, {"id", "编号 · /pages/id/123", ""}}),
		}},
		{ID: "media", Title: "媒体上传", Icon: "image", Description: "管理媒体库与内容附件的单文件上传限制。", Fields: []SettingField{
			intField("max_upload_mib", "media", "单文件大小上限", "0 表示沿用部署设置（默认 64 MiB）；填写 1–1024 后覆盖部署默认值，保存后立即生效。反向代理的请求限制需另行保持一致。", settings.Writing.MaxUploadMiB, 0, 1024, 1, "MiB"),
		}},
		{ID: "accounts", Title: "注册与账号", Icon: "users", Description: "注册字段、验证方式与第三方登录。", Fields: registrationFields(settings)},
		{
			ID: "identity", Title: "站点身份", Icon: "badge-check",
			Description: "定义站点在浏览器、搜索结果、社交分享和主题页头中的基础身份。",
			Fields: []SettingField{
				textField("site_title", "identity", "站点标题", "用于页头品牌、浏览器标题和系统邮件的公开名称。", settings.Identity.Title, true, 120, "例如：NewPress", ""),
				textField("site_tagline", "identity", "副标题", "简短说明站点价值，会显示在品牌与页脚区域。", settings.Identity.Tagline, false, 200, "一句简洁的站点口号", ""),
				textareaField("site_description", "identity", "站点描述", "首页 SEO 描述与社交分享摘要；建议控制在 120–160 字。", settings.Identity.Description, 320, "说明站点主题、受众与价值"),
				localeField(settings.Identity.Locale),
				timezoneField(settings.Identity.Timezone),
				optionField("title_separator", "identity", "标题分隔符", "连接页面标题与站点标题。", "segmented", settings.Identity.TitleSeparator, []SettingOption{{"·", "·", ""}, {"-", "-", ""}, {"|", "|", ""}, {"—", "—", ""}, {"/", "/", ""}}),
				imageField("site_icon_url", "identity", "站点图标", "仅允许站内绝对路径或 HTTPS URL；留空时使用活动主题图标。", settings.Identity.IconURL, false, 2048, "/media/site-icon.png", "url"),
				imageField("social_default_image", "identity", "社交默认图", "文章没有专属封面时用于 Open Graph 分享卡片。", settings.Identity.SocialDefaultImage, false, 2048, "/media/social-cover.jpg", "url"),
			},
		},
		{
			ID: "writing", Title: "写作默认值", Icon: "pen-line",
			Description: "为新文章和页面提供一致、可预测的编辑起点。",
			Fields: []SettingField{
				optionField("default_content_status", "writing", "新内容默认状态", "草稿适合直接创作；待审适合编辑审核流程。", "radio-card", settings.Writing.DefaultStatus, []SettingOption{{"draft", "草稿", "作者可继续编辑，不对外公开"}, {"pending", "待审", "进入审核队列，等待发布决策"}}),
				textField("default_category", "writing", "默认文章栏目", "新文章未指定栏目时使用；页面不使用栏目。", settings.Writing.DefaultCategory, true, 80, "未分类", ""),
				boolField("post_comments_enabled", "writing", "文章默认开放评论", "仅决定新文章的默认值，不回写既有内容。", settings.Writing.PostCommentsEnabled, false),
				boolField("page_comments_enabled", "writing", "页面默认开放评论", "页面通常关闭评论，需要互动型页面时可开启。", settings.Writing.PageCommentsEnabled, false),
				boolField("autosave_enabled", "writing", "编辑器自动建立恢复点", "输入停止后在独立恢复存储中保存当前身份的快照；不会增加正式版本或生成修订。", settings.Writing.AutosaveEnabled, false),
				boolField("browser_recovery_enabled", "writing", "允许浏览器保留编辑副本", "默认关闭。开启后，编辑者仍需在自己的浏览器明确启用；本机明文副本包含正文和待确认保存，恢复时限沿用恢复点保留时间，最长 6 天。过期后下次打开编辑页清理，也可主动清除。共用设备建议关闭。", settings.Writing.BrowserRecoveryEnabled, false),
				intField("autosave_interval_seconds", "writing", "自动保存最小间隔", "同时作为输入空闲等待与最小写入间隔，限制后台请求频率。", settings.Writing.AutosaveIntervalSeconds, 10, 300, 5, "秒"),
				intField("autosave_retention_hours", "writing", "恢复点保留时间", "过期恢复点不再加载；正式保存、发布、回收站或修订恢复会立即清除旧快照。", settings.Writing.AutosaveRetentionHours, 1, 720, 1, "小时"),
				intField("preview_token_minutes", "writing", "安全预览链接时效", "预览仍要求当前后台会话，并绑定文章、身份、来源版本和最新恢复点。", settings.Writing.PreviewTokenMinutes, 1, 60, 1, "分钟"),
			},
		},
		{
			ID: "discussion", Title: "评论与审核", Icon: "message-circle",
			Description: "控制公开讨论入口、审核策略、嵌套回复、展示密度和单实例反滥用预算。",
			Fields: []SettingField{
				boolField("public_comment_submissions", "discussion", "允许公开提交评论", "关闭后保留已审核讨论，但文章页不再接受新评论。", settings.Discussion.PublicSubmissionsEnabled, true),
				boolField("comments_require_login", "discussion", "仅登录账号可评论", "开启后，访客需先注册或登录；页面显示登录遮罩，所有评论提交接口同时校验账号。", settings.Discussion.RequireLogin, true),
				boolField("comments_require_moderation", "discussion", "新评论必须审核", "默认进入待审队列，避免未审内容直接出现在公开页面。", settings.Discussion.RequireModeration, true),
				boolField("comments_allow_replies", "discussion", "允许嵌套回复", "回复必须指向同一内容下已公开评论，最多五层。", settings.Discussion.AllowReplies, false),
				intField("comment_max_length", "discussion", "评论字符上限", "HTTP 层和领域层共同限制；绝不接受任意 HTML。", settings.Discussion.MaxLength, 100, 5000, 100, "字符"),
				intField("comments_per_page", "discussion", "每页公开评论", "限制单篇文章一次读取和渲染的讨论数量。", settings.Discussion.CommentsPerPage, 10, 200, 10, "条"),
				intField("comment_rate_limit", "discussion", "十分钟提交上限", "按来源摘要和文章组合实施单实例有界限速。", settings.Discussion.RateLimitPerTenMinutes, 1, 20, 1, "次"),
				intField("comment_auto_close_days", "discussion", "自动关闭旧内容评论", "0 表示不自动关闭；其余值按公开发布时间计算。", settings.Discussion.AutoCloseDays, 0, 3650, 30, "天"),
				textareaField("comment_moderation_notice", "discussion", "待审提示", "提交成功且需要审核时向访客显示的纯文本说明。", settings.Discussion.ModerationNotice, 240, "评论已进入审核队列，通过后会公开显示。"),
				textareaField("comment_privacy_notice", "discussion", "隐私提示", "说明邮箱、来源摘要和客户端标识的用途；邮箱永不公开。", settings.Discussion.PrivacyNotice, 320, "邮箱仅用于反滥用与必要联系，不会公开显示。"),
			},
		},
		{
			ID: "reading", Title: "阅读与分页", Icon: "list",
			Description: "平衡公开页面信息密度、后台操作效率与数据库查询成本。",
			Fields: []SettingField{
				intField("home_posts", "reading", "首页文章数量", "首页最多读取的最新文章数。", settings.Reading.HomePosts, 4, 48, 1, "篇"),
				intField("explore_posts", "reading", "发现页每页数量", "在全部公开文章中完成搜索与分类筛选后，每页显示的文章数。", settings.Reading.ExplorePosts, 10, 100, 5, "篇"),
				intField("admin_page_size", "reading", "后台每页内容数", "内容列表和管理 API 未指定 per_page 时使用。", settings.Reading.AdminPageSize, 10, 100, 5, "条"),
				intField("revision_limit", "reading", "编辑器修订数量", "编辑器与修订 API 默认展示的历史版本上限。", settings.Reading.RevisionLimit, 5, 100, 5, "版"),
			},
		},
		{
			ID: "api", Title: "API 与集成", Icon: "code-2",
			Description: "控制公开读取接口和受保护内容管理接口的暴露范围。",
			Fields: []SettingField{
				boolField("public_posts_enabled", "api", "开放文章 API 与 RSS", "控制文章 API 和 /feed.xml 订阅；关闭后均返回 404，并隐藏订阅入口。", settings.API.PublicPostsEnabled, true),
				intField("public_api_default_limit", "api", "公开 API 默认数量", "请求未提供 limit 时返回的最大文章数。", settings.API.PublicDefaultLimit, 1, 100, 1, "条"),
				boolField("public_api_include_body", "api", "公开 API 返回正文", "关闭后列表只返回摘要，降低带宽并减少内容批量抓取面。", settings.API.PublicIncludeBody, false),
				boolField("admin_content_api_enabled", "api", "内容管理 JSON API", "控制受保护的 /api/v1/admin/contents 工作流；后台 HTML 编辑器不受影响。", settings.API.AdminContentEnabled, true),
			},
		},
		{
			ID: "seo", Title: "搜索与索引", Icon: "scan-search",
			Description: "统一管理 robots 元数据、robots.txt 与 XML Sitemap。",
			Fields: []SettingField{
				boolField("search_engine_visible", "seo", "允许搜索引擎索引", "关闭后公开页面输出 noindex，并让 robots.txt 禁止全站抓取。", settings.SEO.SearchEngineVisible, true),
				boolField("sitemap_enabled", "seo", "启用 XML Sitemap", "在 /sitemap.xml 发布首页、发现页、文章与公开页面链接。", settings.SEO.SitemapEnabled, false),
				textareaField("robots_text", "seo", "自定义 robots.txt", "仅在允许索引时使用；系统会按需追加 Sitemap 地址。", settings.SEO.RobotsText, 8192, "User-agent: *\nAllow: /"),
			},
		},
		{
			ID: "operations", Title: "运维与合规", Icon: "activity",
			Description: "处理维护窗口、公开缓存、技术指纹和安全联系入口。",
			Fields: []SettingField{
				boolField("maintenance_enabled", "operations", "维护模式", "暂停公开页面与公开 API，后台、健康探针和静态资源保持可用。", settings.Operations.MaintenanceEnabled, true),
				textareaField("maintenance_message", "operations", "维护提示", "维护页向访客展示的纯文本说明。", settings.Operations.MaintenanceMessage, 500, "说明预计恢复时间或替代联系方式"),
				intField("public_cache_seconds", "operations", "公开页面浏览器缓存", "0 表示每次重新验证；其余值写入 public max-age。", settings.Operations.PublicCacheSeconds, 0, 3600, 30, "秒"),
				boolField("generator_meta", "operations", "输出 Generator Meta", "便于生态工具识别 NewPress；关闭可减少不必要的版本指纹。", settings.Operations.GeneratorMeta, false),
				textField("security_contact", "operations", "安全联系地址", "填写 mailto: 或 HTTPS 地址后发布 /.well-known/security.txt。", settings.Operations.SecurityContact, false, 2048, "mailto:security@example.com", "url"),
			},
		},
	}
	return append(groups, integrationGroups(settings)...)
}

func localeField(value string) SettingField {
	return identitySelectField("site_locale", "站点语言", "选择站点语言，控制已接入语言包的界面文案，并写入 HTML lang 与安全联系文件。", value, []SettingOption{
		{Value: "zh-CN", Label: "简体中文"},
		{Value: "zh-Hant", Label: "繁體中文"},
		{Value: "en", Label: "English"},
		{Value: "ja", Label: "日本語"},
	})
}

func timezoneField(value string) SettingField {
	return identitySelectField("site_timezone", "站点时区", "搜索并选择 IANA 时区，统一控制全站时间显示、编辑器输入、自然日统计和自然周；不改变历史时间点或滚动时长。", value, []SettingOption{
		{Value: "UTC", Label: "协调世界时 · UTC"},
		{Value: "Asia/Shanghai", Label: "中国大陆 · Asia/Shanghai"},
		{Value: "Asia/Hong_Kong", Label: "香港 · Asia/Hong_Kong"},
		{Value: "Asia/Macau", Label: "澳门 · Asia/Macau"},
		{Value: "Asia/Taipei", Label: "台北 · Asia/Taipei"},
		{Value: "Asia/Singapore", Label: "新加坡 · Asia/Singapore"},
		{Value: "Asia/Kuala_Lumpur", Label: "吉隆坡 · Asia/Kuala_Lumpur"},
		{Value: "Asia/Tokyo", Label: "东京 · Asia/Tokyo"},
		{Value: "Asia/Seoul", Label: "首尔 · Asia/Seoul"},
		{Value: "Asia/Bangkok", Label: "曼谷 · Asia/Bangkok"},
		{Value: "Asia/Jakarta", Label: "雅加达 · Asia/Jakarta"},
		{Value: "Asia/Kolkata", Label: "印度 · Asia/Kolkata"},
		{Value: "Asia/Dubai", Label: "迪拜 · Asia/Dubai"},
		{Value: "Europe/London", Label: "伦敦 · Europe/London"},
		{Value: "Europe/Paris", Label: "巴黎 · Europe/Paris"},
		{Value: "Europe/Berlin", Label: "柏林 · Europe/Berlin"},
		{Value: "Europe/Moscow", Label: "莫斯科 · Europe/Moscow"},
		{Value: "Africa/Cairo", Label: "开罗 · Africa/Cairo"},
		{Value: "Africa/Johannesburg", Label: "约翰内斯堡 · Africa/Johannesburg"},
		{Value: "America/New_York", Label: "纽约 · America/New_York"},
		{Value: "America/Chicago", Label: "芝加哥 · America/Chicago"},
		{Value: "America/Denver", Label: "丹佛 · America/Denver"},
		{Value: "America/Los_Angeles", Label: "洛杉矶 · America/Los_Angeles"},
		{Value: "America/Vancouver", Label: "温哥华 · America/Vancouver"},
		{Value: "America/Toronto", Label: "多伦多 · America/Toronto"},
		{Value: "America/Sao_Paulo", Label: "圣保罗 · America/Sao_Paulo"},
		{Value: "Australia/Perth", Label: "珀斯 · Australia/Perth"},
		{Value: "Australia/Adelaide", Label: "阿德莱德 · Australia/Adelaide"},
		{Value: "Australia/Sydney", Label: "悉尼 · Australia/Sydney"},
		{Value: "Pacific/Auckland", Label: "奥克兰 · Pacific/Auckland"},
		{Value: "Pacific/Honolulu", Label: "檀香山 · Pacific/Honolulu"},
	})
}

func identitySelectField(key, title, description, value string, options []SettingOption) SettingField {
	// Preserve values supplied by imports or extensions even outside the presets.
	found := false
	for _, option := range options {
		if option.Value == value {
			found = true
			break
		}
	}
	if !found {
		options = append(options, SettingOption{Value: value, Label: value + "（当前配置）"})
	}
	return optionField(key, "identity", title, description, "select", value, options)
}

func textField(key, group, title, description, value string, required bool, maxLength int, placeholder, inputMode string) SettingField {
	return SettingField{
		Key: key, Group: group, Title: title, Description: description, Control: "text",
		StringValue: value, Required: required, MaxLength: maxLength, Placeholder: placeholder, InputMode: inputMode,
	}
}

func textareaField(key, group, title, description, value string, maxLength int, placeholder string) SettingField {
	return SettingField{
		Key: key, Group: group, Title: title, Description: description, Control: "textarea",
		StringValue: value, MaxLength: maxLength, Placeholder: placeholder,
	}
}

func boolField(key, group, title, description string, value, critical bool) SettingField {
	return SettingField{
		Key: key, Group: group, Title: title, Description: description, Control: "toggle",
		BoolValue: value, Critical: critical,
	}
}

func intField(key, group, title, description string, value, minimum, maximum, step int, unit string) SettingField {
	return SettingField{
		Key: key, Group: group, Title: title, Description: description, Control: "range",
		StringValue: strconv.Itoa(value), Min: strconv.Itoa(minimum), Max: strconv.Itoa(maximum),
		Step: strconv.Itoa(step), Unit: unit,
	}
}

func optionField(key, group, title, description, control, value string, options []SettingOption) SettingField {
	return SettingField{
		Key: key, Group: group, Title: title, Description: description, Control: control,
		StringValue: value, Required: true, Options: options,
	}
}

// imageField keeps the URL validation and storage contract of textField.
func imageField(key, group, title, description, value string, required bool, maximum int, placeholder, inputMode string) SettingField {
	field := textField(key, group, title, description, value, required, maximum, placeholder, inputMode)
	field.Control = "image"
	return field
}
