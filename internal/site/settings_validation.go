package site

import (
	"errors"
	"fmt"
	"maps"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/module"
)

func Validate(settings Settings) (Settings, map[string]string) {
	fields := make(map[string]string)
	validateIntegrations(&settings, fields)
	settings.Modules = maps.Clone(settings.Modules)
	for key, message := range module.Validate(settings.Modules) {
		fields["module."+key] = message
	}
	if settings.Accounts != nil {
		accounts := *settings.Accounts
		if !oneOf(accounts.Registration, "closed", "invite", "open") {
			fields["account_registration"] = "注册方式必须是关闭、邀请或开放"
		}
		settings.Accounts = &accounts
		if accounts.Policy != nil {
			p := settings.RegistrationPolicy()
			validateRegistration(&p, fields)
			accounts.Policy = &p
			settings.Accounts = &accounts
		}
		if accounts.Registration == "closed" && accounts.Policy == nil {
			settings.Accounts = nil
		}
	}
	settings.Identity.Title = normalizedText(settings.Identity.Title)
	settings.Identity.Tagline = normalizedText(settings.Identity.Tagline)
	settings.Identity.Description = normalizedText(settings.Identity.Description)
	settings.Identity.Locale = strings.TrimSpace(settings.Identity.Locale)
	settings.Identity.Timezone = strings.TrimSpace(settings.Identity.Timezone)
	settings.Identity.TitleSeparator = strings.TrimSpace(settings.Identity.TitleSeparator)
	settings.Identity.IconURL = strings.TrimSpace(settings.Identity.IconURL)
	settings.Identity.SocialDefaultImage = strings.TrimSpace(settings.Identity.SocialDefaultImage)

	validateText(fields, "site_title", settings.Identity.Title, 1, 120)
	validateText(fields, "site_tagline", settings.Identity.Tagline, 0, 200)
	validateText(fields, "site_description", settings.Identity.Description, 0, 320)
	if !localePattern.MatchString(settings.Identity.Locale) || len(settings.Identity.Locale) > 35 {
		fields["site_locale"] = "语言标识必须是有效的 BCP 47 形式，例如 zh-CN"
	}
	if zone := settings.Identity.Timezone; zone == "" || zone == "Local" {
		fields["site_timezone"] = "请填写明确的 IANA 时区或 UTC；不支持空值或依赖服务器的 Local"
	} else if _, err := time.LoadLocation(zone); err != nil {
		fields["site_timezone"] = "时区必须是有效的 IANA 时区，例如 Asia/Shanghai"
	}
	if !oneOf(settings.Identity.TitleSeparator, "·", "-", "|", "—", "/") {
		fields["title_separator"] = "标题分隔符不受支持"
	}
	if err := validateAssetURL(settings.Identity.IconURL); err != nil {
		fields["site_icon_url"] = err.Error()
	}
	if err := validateAssetURL(settings.Identity.SocialDefaultImage); err != nil {
		fields["social_default_image"] = err.Error()
	}

	validateInt(fields, "max_upload_mib", settings.Writing.MaxUploadMiB, 0, 1024)
	if settings.Writing.PostPermalink == "slug" {
		settings.Writing.PostPermalink = ""
	}
	if settings.Writing.PagePermalink == "slug" {
		settings.Writing.PagePermalink = ""
	}
	if settings.Writing.SlugMode == "smart" {
		settings.Writing.SlugMode = ""
	}
	if !oneOf(settings.Writing.PostPermalink, "", "id", "month", "date") {
		fields["post_permalink"] = "请选择有效的文章固定链接结构"
	}
	if !oneOf(settings.Writing.PagePermalink, "", "id") {
		fields["page_permalink"] = "请选择有效的页面固定链接结构"
	}
	if !oneOf(settings.Writing.SlugMode, "", "manual") {
		fields["slug_mode"] = "请选择有效的别名生成方式"
	}
	settings.Writing.DefaultStatus = strings.ToLower(strings.TrimSpace(settings.Writing.DefaultStatus))
	settings.Writing.DefaultCategory = normalizedText(settings.Writing.DefaultCategory)
	if !oneOf(settings.Writing.DefaultStatus, "draft", "pending") {
		fields["default_content_status"] = "默认状态只能是草稿或待审"
	}
	validateText(fields, "default_category", settings.Writing.DefaultCategory, 1, 80)
	validateInt(fields, "autosave_interval_seconds", settings.Writing.AutosaveIntervalSeconds, 10, 300)
	validateInt(fields, "autosave_retention_hours", settings.Writing.AutosaveRetentionHours, 1, 720)
	validateInt(fields, "preview_token_minutes", settings.Writing.PreviewTokenMinutes, 1, 60)

	settings.Discussion.ModerationNotice = normalizedText(settings.Discussion.ModerationNotice)
	settings.Discussion.PrivacyNotice = normalizedText(settings.Discussion.PrivacyNotice)
	validateInt(fields, "comment_max_length", settings.Discussion.MaxLength, 100, 5000)
	validateInt(fields, "comments_per_page", settings.Discussion.CommentsPerPage, 10, 200)
	validateInt(fields, "comment_rate_limit", settings.Discussion.RateLimitPerTenMinutes, 1, 20)
	validateInt(fields, "comment_auto_close_days", settings.Discussion.AutoCloseDays, 0, 3650)
	validateText(fields, "comment_moderation_notice", settings.Discussion.ModerationNotice, 0, 240)
	validateText(fields, "comment_privacy_notice", settings.Discussion.PrivacyNotice, 0, 320)
	if settings.Discussion.RequireModeration && settings.Discussion.ModerationNotice == "" {
		fields["comment_moderation_notice"] = "要求审核时必须说明评论提交后的状态"
	}

	validateInt(fields, "home_posts", settings.Reading.HomePosts, 4, 48)
	validateInt(fields, "explore_posts", settings.Reading.ExplorePosts, 10, 100)
	validateInt(fields, "admin_page_size", settings.Reading.AdminPageSize, 10, 100)
	validateInt(fields, "revision_limit", settings.Reading.RevisionLimit, 5, 100)
	validateInt(fields, "public_api_default_limit", settings.API.PublicDefaultLimit, 1, 100)

	settings.SEO.RobotsText = normalizeMultiline(settings.SEO.RobotsText)
	if len(settings.SEO.RobotsText) > 8192 || hasUnsafeControls(settings.SEO.RobotsText, true) {
		fields["robots_text"] = "robots.txt 不能超过 8 KiB 或包含控制字符"
	}

	settings.Operations.MaintenanceMessage = normalizedText(settings.Operations.MaintenanceMessage)
	settings.Operations.SecurityContact = strings.TrimSpace(settings.Operations.SecurityContact)
	validateText(fields, "maintenance_message", settings.Operations.MaintenanceMessage, 0, 500)
	if settings.Operations.MaintenanceEnabled && settings.Operations.MaintenanceMessage == "" {
		fields["maintenance_message"] = "开启维护模式前必须填写访客提示"
	}
	validateInt(fields, "public_cache_seconds", settings.Operations.PublicCacheSeconds, 0, 3600)
	if err := validateSecurityContact(settings.Operations.SecurityContact); err != nil {
		fields["security_contact"] = err.Error()
	}
	return settings, fields
}

func ParseForm(values url.Values) (Settings, map[string]string, error) {
	for key, entries := range values {
		if _, ok := formFields[key]; !ok || len(entries) != 1 {
			return Settings{}, nil, fmt.Errorf("%w: field %q is unknown or repeated", ErrInvalidForm, key)
		}
	}
	settings := Settings{}
	fields := make(map[string]string)
	text := func(key string) string {
		entries, ok := values[key]
		if !ok || len(entries) != 1 {
			fields[key] = "此设置缺失"
			return ""
		}
		return entries[0]
	}
	boolean := func(key string) bool {
		entries, ok := values[key]
		if !ok {
			return false
		}
		if entries[0] != "true" {
			fields[key] = "开关值不正确"
			return false
		}
		return true
	}
	integer := func(key string) int {
		raw := text(key)
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			fields[key] = "必须填写整数"
			return 0
		}
		return parsed
	}

	settings.Identity = IdentitySettings{
		Title: text("site_title"), Tagline: text("site_tagline"), Description: text("site_description"),
		Locale: text("site_locale"), Timezone: text("site_timezone"), TitleSeparator: text("title_separator"),
		IconURL: text("site_icon_url"), SocialDefaultImage: text("social_default_image"),
	}
	if _, exists := values["account_registration"]; exists {
		settings.Accounts = &AccountSettings{Registration: text("account_registration"), Policy: parseRegistration(values, boolean, fields)}
	}
	settings.Writing = WritingSettings{
		DefaultStatus: text("default_content_status"), DefaultCategory: text("default_category"),
		PostCommentsEnabled: boolean("post_comments_enabled"), PageCommentsEnabled: boolean("page_comments_enabled"),
		AutosaveEnabled: boolean("autosave_enabled"), AutosaveIntervalSeconds: integer("autosave_interval_seconds"),
		AutosaveRetentionHours: integer("autosave_retention_hours"), PreviewTokenMinutes: integer("preview_token_minutes"),
		BrowserRecoveryEnabled: boolean("browser_recovery_enabled"),
	}
	if _, ok := values["max_upload_mib"]; ok {
		settings.Writing.MaxUploadMiB = integer("max_upload_mib")
	}
	settings.Writing.PostPermalink = values.Get("post_permalink")
	settings.Writing.PagePermalink = values.Get("page_permalink")
	settings.Writing.SlugMode = values.Get("slug_mode")
	settings.Discussion = DiscussionSettings{
		RequireLogin:             boolean("comments_require_login"),
		PublicSubmissionsEnabled: boolean("public_comment_submissions"), RequireModeration: boolean("comments_require_moderation"),
		AllowReplies: boolean("comments_allow_replies"), MaxLength: integer("comment_max_length"),
		CommentsPerPage: integer("comments_per_page"), RateLimitPerTenMinutes: integer("comment_rate_limit"),
		AutoCloseDays: integer("comment_auto_close_days"), ModerationNotice: text("comment_moderation_notice"),
		PrivacyNotice: text("comment_privacy_notice"),
	}
	settings.Reading = ReadingSettings{
		HomePosts: integer("home_posts"), ExplorePosts: integer("explore_posts"),
		AdminPageSize: integer("admin_page_size"), RevisionLimit: integer("revision_limit"),
	}
	settings.API = APISettings{
		PublicPostsEnabled: boolean("public_posts_enabled"), PublicDefaultLimit: integer("public_api_default_limit"),
		PublicIncludeBody: boolean("public_api_include_body"), AdminContentEnabled: boolean("admin_content_api_enabled"),
	}
	settings.SEO = SEOSettings{
		SearchEngineVisible: boolean("search_engine_visible"), SitemapEnabled: boolean("sitemap_enabled"),
		RobotsText: text("robots_text"),
	}
	settings.Operations = OperationsSettings{
		MaintenanceEnabled: boolean("maintenance_enabled"), MaintenanceMessage: text("maintenance_message"),
		PublicCacheSeconds: integer("public_cache_seconds"), GeneratorMeta: boolean("generator_meta"),
		SecurityContact: text("security_contact"),
	}
	settings.Integrations = parseIntegrations(values, boolean("analytics_enabled"))
	settings.Integrations.FooterHideCredit = boolean("footer_hide_credit")
	normalized, validationFields := Validate(settings)
	for key, message := range validationFields {
		if _, exists := fields[key]; !exists {
			fields[key] = message
		}
	}
	return normalized, fields, nil
}

var formFields = map[string]struct{}{
	"max_upload_mib": {}, "post_permalink": {}, "page_permalink": {}, "slug_mode": {},
	"account_registration": {},
	"site_title":           {}, "site_tagline": {}, "site_description": {}, "site_locale": {}, "site_timezone": {},
	"title_separator": {}, "site_icon_url": {}, "social_default_image": {},
	"default_content_status": {}, "default_category": {}, "post_comments_enabled": {}, "page_comments_enabled": {},
	"autosave_enabled": {}, "autosave_interval_seconds": {}, "autosave_retention_hours": {}, "preview_token_minutes": {}, "browser_recovery_enabled": {},
	"public_comment_submissions": {}, "comments_require_login": {}, "comments_require_moderation": {}, "comments_allow_replies": {},
	"comment_max_length": {}, "comments_per_page": {}, "comment_rate_limit": {}, "comment_auto_close_days": {},
	"comment_moderation_notice": {}, "comment_privacy_notice": {},
	"home_posts": {}, "explore_posts": {}, "admin_page_size": {}, "revision_limit": {},
	"public_posts_enabled": {}, "public_api_default_limit": {}, "public_api_include_body": {}, "admin_content_api_enabled": {},
	"search_engine_visible": {}, "sitemap_enabled": {}, "robots_text": {},
	"maintenance_enabled": {}, "maintenance_message": {}, "public_cache_seconds": {}, "generator_meta": {}, "security_contact": {},
}

func validateText(fields map[string]string, key, value string, minimum, maximum int) {
	count := utf8.RuneCountInString(value)
	if !utf8.ValidString(value) || count < minimum || count > maximum || hasUnsafeControls(value, false) {
		switch {
		case minimum > 0 && value == "":
			fields[key] = "此设置不能为空"
		default:
			fields[key] = fmt.Sprintf("长度必须介于 %d 和 %d 个字符且不能包含控制字符", minimum, maximum)
		}
	}
}

func validateInt(fields map[string]string, key string, value, minimum, maximum int) {
	if value < minimum || value > maximum {
		fields[key] = fmt.Sprintf("数值必须介于 %d 和 %d 之间", minimum, maximum)
	}
}

func validateAssetURL(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 2048 || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") {
		return errors.New("URL 过长或包含不安全字符")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return errors.New("URL 格式不正确")
	}
	if parsed.IsAbs() {
		if !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil {
			return errors.New("仅允许站内绝对路径或不含用户信息的 HTTPS URL")
		}
		return nil
	}
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || parsed.Host != "" {
		return errors.New("站内资源 URL 必须以单个 / 开头")
	}
	return nil
}

func validateSecurityContact(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("安全联系地址过长或包含控制字符")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return errors.New("安全联系地址格式不正确")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "mailto":
		address := strings.TrimPrefix(value, "mailto:")
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("mailto 联系地址不能包含查询参数或片段")
		}
		mailbox, err := mail.ParseAddress(address)
		if err != nil || mailbox.Address != address {
			return errors.New("mailto 联系地址必须包含有效邮箱")
		}
	case "https":
		if parsed.Host == "" || parsed.User != nil {
			return errors.New("HTTPS 联系地址必须包含主机且不能包含用户信息")
		}
	default:
		return errors.New("安全联系地址只允许 mailto: 或 HTTPS")
	}
	return nil
}

func normalizedText(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
}

func normalizeMultiline(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}

func hasUnsafeControls(value string, allowNewline bool) bool {
	for _, character := range value {
		if !unicode.IsControl(character) {
			continue
		}
		if character == '\t' || allowNewline && character == '\n' {
			continue
		}
		return true
	}
	return false
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
