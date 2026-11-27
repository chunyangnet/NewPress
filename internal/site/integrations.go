package site

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"newpress.local/newpress/internal/contenthtml"
)

// IntegrationsSettings contains public client identifiers, never API secrets or
// executable snippets.
type IntegrationsSettings struct {
	ICPNumber        string               `json:"icp_number,omitempty"`
	PoliceNumber     string               `json:"police_number,omitempty"`
	PoliceCode       string               `json:"police_code,omitempty"`
	FooterCopyright  string               `json:"footer_copyright,omitempty"`
	FooterHTML       string               `json:"footer_html,omitempty"`
	FooterHideCredit bool                 `json:"footer_hide_credit,omitempty"`
	Analytics        AnalyticsSettings    `json:"analytics"`
	Verification     VerificationSettings `json:"verification"`
}

type AnalyticsSettings struct {
	Enabled         bool   `json:"enabled,omitempty"`
	GoogleID        string `json:"google_id,omitempty"`
	GoogleTagID     string `json:"google_tag_id,omitempty"`
	BaiduID         string `json:"baidu_id,omitempty"`
	ClarityID       string `json:"clarity_id,omitempty"`
	CloudflareToken string `json:"cloudflare_token,omitempty"`
	MatomoURL       string `json:"matomo_url,omitempty"`
	MatomoSiteID    string `json:"matomo_site_id,omitempty"`
	UmamiURL        string `json:"umami_url,omitempty"`
	UmamiWebsiteID  string `json:"umami_website_id,omitempty"`
	PlausibleURL    string `json:"plausible_url,omitempty"`
	AllowedOrigins  string `json:"allowed_origins,omitempty"`
}

func (a AnalyticsSettings) Active() bool {
	return a.Enabled && (a.GoogleID != "" || a.GoogleTagID != "" || a.BaiduID != "" || a.ClarityID != "" || a.CloudflareToken != "" || a.MatomoURL != "" || a.UmamiURL != "" || a.PlausibleURL != "")
}

type VerificationSettings struct {
	Google string `json:"google,omitempty"`
	Bing   string `json:"bing,omitempty"`
	Baidu  string `json:"baidu,omitempty"`
	Qihoo  string `json:"qihoo,omitempty"`
	Sogou  string `json:"sogou,omitempty"`
	Yandex string `json:"yandex,omitempty"`
	Custom string `json:"custom,omitempty"`
}

type VerificationMeta struct{ Name, Content string }

func (v VerificationSettings) Metas() []VerificationMeta {
	var result []VerificationMeta
	for _, meta := range []VerificationMeta{
		{"google-site-verification", v.Google}, {"msvalidate.01", v.Bing},
		{"baidu-site-verification", v.Baidu}, {"360-site-verification", v.Qihoo},
		{"sogou_site_verification", v.Sogou}, {"yandex-verification", v.Yandex},
	} {
		if meta.Content != "" {
			result = append(result, meta)
		}
	}
	for _, line := range strings.Split(v.Custom, "\n") {
		name, content, ok := strings.Cut(line, "=")
		if ok {
			result = append(result, VerificationMeta{strings.TrimSpace(name), strings.TrimSpace(content)})
		}
	}
	return result
}

func (s Settings) IntegrationValues() IntegrationsSettings {
	if s.Integrations == nil {
		return IntegrationsSettings{}
	}
	return *s.Integrations
}

var (
	googleAnalyticsID = regexp.MustCompile(`^G-[A-Z0-9]{4,32}$`)
	googleTagID       = regexp.MustCompile(`^GTM-[A-Z0-9]{4,32}$`)
	hexTrackingID     = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
	clarityTrackingID = regexp.MustCompile(`^[a-zA-Z0-9]{1,64}$`)
	umamiTrackingID   = regexp.MustCompile(`^[a-fA-F0-9]{8}(?:-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12}$`)
	matomoTrackingID  = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)
	policeRecordCode  = regexp.MustCompile(`^[0-9]{10,30}$`)
	verificationName  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,99}$`)
)

func integrationTextFields(i *IntegrationsSettings) map[string]*string {
	a, v := &i.Analytics, &i.Verification
	return map[string]*string{
		"icp_number": &i.ICPNumber, "police_number": &i.PoliceNumber, "police_code": &i.PoliceCode,
		"footer_copyright": &i.FooterCopyright, "footer_html": &i.FooterHTML,
		"analytics_google_id": &a.GoogleID, "analytics_google_tag_id": &a.GoogleTagID,
		"analytics_baidu_id": &a.BaiduID, "analytics_clarity_id": &a.ClarityID,
		"analytics_cloudflare_token": &a.CloudflareToken, "analytics_matomo_url": &a.MatomoURL,
		"analytics_matomo_site_id": &a.MatomoSiteID, "analytics_umami_url": &a.UmamiURL,
		"analytics_umami_website_id": &a.UmamiWebsiteID, "analytics_plausible_url": &a.PlausibleURL,
		"analytics_allowed_origins": &a.AllowedOrigins,
		"verification_google":       &v.Google, "verification_bing": &v.Bing, "verification_baidu": &v.Baidu,
		"verification_qihoo": &v.Qihoo, "verification_sogou": &v.Sogou, "verification_yandex": &v.Yandex,
		"verification_custom": &v.Custom,
	}
}

func parseIntegrations(values url.Values, enabled bool) *IntegrationsSettings {
	i := &IntegrationsSettings{}
	i.Analytics.Enabled = enabled
	i.FooterHideCredit = values.Get("footer_hide_credit") == "true"
	for key, target := range integrationTextFields(i) {
		*target = values.Get(key)
	}
	return i
}

func init() {
	formFields["analytics_enabled"] = struct{}{}
	formFields["footer_hide_credit"] = struct{}{}
	for key := range integrationTextFields(&IntegrationsSettings{}) {
		formFields[key] = struct{}{}
	}
}

func validateIntegrations(settings *Settings, fields map[string]string) {
	if settings.Integrations == nil {
		settings.Integrations = &IntegrationsSettings{}
	}
	i := *settings.Integrations
	for key, target := range integrationTextFields(&i) {
		*target = strings.TrimSpace(*target)
		if key != "verification_custom" && key != "analytics_allowed_origins" && key != "footer_html" {
			validateText(fields, key, *target, 0, 2048)
			if strings.ContainsAny(*target, "\r\n") {
				fields[key] = "请填写单行值"
			}
		}
	}
	a := &i.Analytics
	validateText(fields, "icp_number", i.ICPNumber, 0, 100)
	validateText(fields, "police_number", i.PoliceNumber, 0, 100)
	validateText(fields, "footer_copyright", i.FooterCopyright, 0, 240)
	i.FooterHTML = normalizeMultiline(i.FooterHTML)
	if !utf8.ValidString(i.FooterHTML) || len(i.FooterHTML) > 8192 || hasUnsafeControls(i.FooterHTML, true) {
		fields["footer_html"] = "页脚内容不能超过 8 KiB 或包含控制字符"
	} else {
		i.FooterHTML = contenthtml.Sanitize(i.FooterHTML)
		if len(i.FooterHTML) > 8192 {
			fields["footer_html"] = "净化后的页脚内容不能超过 8 KiB，请缩短内容"
		}
	}
	for key, rule := range map[string]*regexp.Regexp{
		"police_code": policeRecordCode, "analytics_google_id": googleAnalyticsID, "analytics_google_tag_id": googleTagID,
		"analytics_baidu_id": hexTrackingID, "analytics_clarity_id": clarityTrackingID,
		"analytics_cloudflare_token": hexTrackingID, "analytics_matomo_site_id": matomoTrackingID,
		"analytics_umami_website_id": umamiTrackingID,
	} {
		value := *integrationTextFields(&i)[key]
		if value != "" && !rule.MatchString(value) {
			fields[key] = "格式不正确，请填写平台提供的标识，不要粘贴整段代码"
		}
	}
	if (i.PoliceNumber == "") != (i.PoliceCode == "") {
		fields["police_number"], fields["police_code"] = "请同时填写公安备案显示文字和查询编号", "请填写备案查询链接中的纯数字编号"
	}
	for key, value := range map[string]string{"analytics_matomo_url": a.MatomoURL, "analytics_umami_url": a.UmamiURL, "analytics_plausible_url": a.PlausibleURL} {
		if value != "" && !validIntegrationURL(value, false) {
			fields[key] = "请填写不含账号信息、片段或空白的完整 HTTPS 地址"
		}
	}
	if u, err := url.Parse(a.MatomoURL); err == nil && u != nil && u.RawQuery != "" {
		fields["analytics_matomo_url"] = "Matomo 根地址不能包含查询参数"
	}
	a.MatomoURL = strings.TrimRight(a.MatomoURL, "/")
	if (a.MatomoURL == "") != (a.MatomoSiteID == "") {
		fields["analytics_matomo_url"], fields["analytics_matomo_site_id"] = "请同时填写 Matomo 地址和站点 ID", "请同时填写 Matomo 地址和站点 ID"
	}
	if (a.UmamiURL == "") != (a.UmamiWebsiteID == "") {
		fields["analytics_umami_url"], fields["analytics_umami_website_id"] = "请同时填写 Umami 脚本地址和网站 ID", "请同时填写 Umami 脚本地址和网站 ID"
	}
	a.AllowedOrigins = normalizeMultiline(a.AllowedOrigins)
	var origins []string
	seen := map[string]bool{}
	for _, line := range strings.Split(a.AllowedOrigins, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !validIntegrationURL(line, true) {
			fields["analytics_allowed_origins"] = "每行只填写 HTTPS 来源（如 https://stats.example.com），不支持通配符、路径或代码"
		}
		line = strings.TrimRight(line, "/")
		if !seen[line] {
			origins = append(origins, line)
			seen[line] = true
		}
	}
	if len(origins) > 16 || len(a.AllowedOrigins) > 8192 {
		fields["analytics_allowed_origins"] = "最多填写 16 个来源，总长度不能超过 8 KiB"
	}
	a.AllowedOrigins = strings.Join(origins, "\n")
	v := &i.Verification
	v.Custom = normalizeMultiline(v.Custom)
	if len(v.Custom) > 8192 || hasUnsafeControls(v.Custom, true) {
		fields["verification_custom"] = "认证项不能超过 8 KiB 或包含控制字符"
	}
	var custom []string
	names := map[string]bool{}
	fixed := *v
	fixed.Custom = ""
	for _, meta := range fixed.Metas() {
		names[strings.ToLower(meta.Name)] = true
	}
	for _, line := range strings.Split(v.Custom, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, content, ok := strings.Cut(line, "=")
		name, content = strings.TrimSpace(name), strings.TrimSpace(content)
		lower := strings.ToLower(name)
		if !ok || !verificationName.MatchString(name) || (!strings.Contains(lower, "verification") && lower != "msvalidate.01" && lower != "p:domain_verify") || content == "" || len(content) > 2048 || names[lower] {
			fields["verification_custom"] = "每行填写认证 meta 的 name=content；name 须含 verification 或为 p:domain_verify，且不能重复"
		}
		names[lower] = true
		custom = append(custom, name+"="+content)
	}
	if len(custom) > 20 {
		fields["verification_custom"] = "最多填写 20 个自定义认证项"
	}
	v.Custom = strings.Join(custom, "\n")
	settings.Integrations = &i
}

// Only serialized origins reach CSP. Disallow delimiter/quote characters even
// in paths, and let net/url reject malformed hosts and ports.
func validIntegrationURL(value string, originOnly bool) bool {
	if len(value) > 2048 || strings.ContainsAny(value, " \t\r\n'\";<>\\*") || hasUnsafeControls(value, false) {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	for _, c := range u.Host {
		if c > 127 {
			return false
		}
	}
	return !originOnly || ((u.Path == "" || u.Path == "/") && u.RawQuery == "" && !u.ForceQuery)
}

func integrationGroups(settings Settings) []SettingGroup {
	i := settings.IntegrationValues()
	a, v := i.Analytics, i.Verification
	return []SettingGroup{
		{ID: "compliance", Title: "备案信息", Icon: "badge-check", Description: "备案信息会显示在网站页脚；留空时隐藏。", Fields: []SettingField{
			textField("icp_number", "compliance", "ICP备案号", "填写完整备案号，页脚自动链接至工信部备案查询。", i.ICPNumber, false, 100, "京ICP备12345678号-1", ""),
			textField("police_number", "compliance", "公安备案显示文字", "与查询编号一起填写，显示在网站页脚。", i.PoliceNumber, false, 100, "京公网安备11010102001234号", ""),
			textField("police_code", "compliance", "公安备案查询编号", "填写官方查询链接 code 参数中的纯数字编号。", i.PoliceCode, false, 30, "11010102001234", "numeric"),
		}},
		{ID: "footer", Title: "自定义页脚", Icon: "layout-grid", Description: "设置网站底部的版权与补充内容，备案信息会始终独立展示。", Fields: []SettingField{
			textField("footer_copyright", "footer", "版权文字", "留空时显示当前年份和站点名称；填写后替换默认版权文字。", i.FooterCopyright, false, 240, "© 2026 你的站点 · 保留所有权利", ""),
			textareaField("footer_html", "footer", "页脚补充内容", "可填写文字或 HTML，支持段落、链接、列表与图片。脚本、事件和内联样式会自动清理；备案信息仍单独显示。", i.FooterHTML, 8192, "<p>关于本站 · <a href=\"/privacy/\">隐私政策</a></p>"),
			boolField("footer_hide_credit", "footer", "隐藏技术支持文字", "隐藏页脚中的“由 NewPress 提供支持”。", i.FooterHideCredit, false),
		}},
		{ID: "tracking", Title: "统计与跟踪", Icon: "activity", Description: "支持多个平台同时接入；先填写项目标识，再打开总开关。", Fields: []SettingField{
			boolField("analytics_enabled", "tracking", "启用第三方统计", "仅在公开内容页面加载。后台、账号、草稿预览和错误页面不采集；关闭后保留配置。", a.Enabled, false),
			textField("analytics_google_id", "tracking", "Google Analytics 4", "填写 G- 开头的衡量 ID。若已在 GTM 中配置同一统计，请仅选一个入口，避免重复采集。", a.GoogleID, false, 34, "G-XXXXXXXXXX", ""),
			textField("analytics_google_tag_id", "tracking", "Google Tag Manager", "填写 GTM- 开头的容器 ID，发布容器后生效。额外标签域名可在下方添加。", a.GoogleTagID, false, 36, "GTM-XXXXXXX", ""),
			textField("analytics_baidu_id", "tracking", "百度统计", "填写统计代码中 hm.js? 后面的 32 位标识。", a.BaiduID, false, 32, "32 位统计标识", ""),
			textField("analytics_clarity_id", "tracking", "Microsoft Clarity", "微软行为跟踪与热力图；填写项目 ID（clarity.ms/tag/ 后面的值）。", a.ClarityID, false, 64, "Clarity 项目 ID", ""),
			textField("analytics_cloudflare_token", "tracking", "Cloudflare Web Analytics", "填写 data-cf-beacon 中的公开 token。若 Cloudflare 已自动注入，请勿重复配置。", a.CloudflareToken, false, 32, "32 位公开 token", ""),
			textField("analytics_matomo_url", "tracking", "Matomo 服务地址", "填写 Matomo 安装根地址，支持 Cloud 与自建服务。", a.MatomoURL, false, 2048, "https://stats.example.com/matomo", "url"),
			textField("analytics_matomo_site_id", "tracking", "Matomo 站点 ID", "与 Matomo 服务地址配套填写。", a.MatomoSiteID, false, 10, "1", "numeric"),
			textField("analytics_umami_url", "tracking", "Umami 脚本地址", "填写平台提供的完整 script src，支持 Cloud 与自建服务。", a.UmamiURL, false, 2048, "https://cloud.umami.is/script.js", "url"),
			textField("analytics_umami_website_id", "tracking", "Umami 网站 ID", "填写 data-website-id 中的 UUID。", a.UmamiWebsiteID, false, 36, "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx", ""),
			textField("analytics_plausible_url", "tracking", "Plausible 脚本地址", "填写新版安装代码中站点专属的 pa-…js 地址。", a.PlausibleURL, false, 2048, "https://plausible.io/js/pa-XXXXXXXX.js", "url"),
			textareaField("analytics_allowed_origins", "tracking", "附加统计来源", "用于 GTM 标签或自建统计的独立上报域名，每行一个 HTTPS 来源；常用平台的必要域名会自动加入。", a.AllowedOrigins, 8192, "https://collect.example.com"),
		}},
		{ID: "verification", Title: "站长平台认证", Icon: "scan-search", Description: "填写平台 HTML 标签中的 content 值，自动加入公开页面 head。", Fields: []SettingField{
			textField("verification_google", "verification", "Google Search Console", "google-site-verification 标签的 content 值。", v.Google, false, 2048, "仅填写 content 值", ""),
			textField("verification_bing", "verification", "Bing 网站管理员", "msvalidate.01 标签的 content 值。", v.Bing, false, 2048, "仅填写 content 值", ""),
			textField("verification_baidu", "verification", "百度搜索资源平台", "baidu-site-verification 标签的 content 值。", v.Baidu, false, 2048, "仅填写 content 值", ""),
			textField("verification_qihoo", "verification", "360 站长平台", "360-site-verification 标签的 content 值。", v.Qihoo, false, 2048, "仅填写 content 值", ""),
			textField("verification_sogou", "verification", "搜狗站长平台", "sogou_site_verification 标签的 content 值。", v.Sogou, false, 2048, "仅填写 content 值", ""),
			textField("verification_yandex", "verification", "Yandex Webmaster", "yandex-verification 标签的 content 值。", v.Yandex, false, 2048, "仅填写 content 值", ""),
			textareaField("verification_custom", "verification", "其他平台认证", "每行填写 name=content，name 须含 verification 或为 p:domain_verify；无需粘贴 HTML 标签。", v.Custom, 8192, "p:domain_verify=认证值"),
		}},
	}
}
