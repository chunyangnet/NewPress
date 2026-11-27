package httpserver

import (
	"fmt"
	"net/http"
	"strings"

	"newpress.local/newpress/internal/presentation"
	"newpress.local/newpress/internal/theme"
)

// Deterministic visual states served only by the opt-in, isolated browser fixture.
func serveNexoraVisualFixture(w http.ResponseWriter, r *http.Request, manager *theme.Manager) {
	kind := strings.TrimPrefix(r.URL.Path, "/__nexora_visual/")
	p := presentation.Fixtures()["components"]
	p.Kind, p.Site.Title, p.PageTitle = kind, "Nexora 科技", "Nexora · 视觉核查"
	p.Site.IconURL = "/favicon.ico"
	p.ThemeCSSURL = "/theme-settings.css"
	p.Settings = manager.SettingsSnapshot().Values
	p.Navigation = map[string]presentation.Menu{}
	p.Account.Authenticated = r.URL.Query().Get("member") == "1"
	p.Account.ID = 0 // Account data loading is covered separately with actual fixture identities.
	p.Feedback.Settings.Name = "项目咨询与服务反馈"
	p.Feedback.Settings.Description = "告诉我们您的业务需求，查看回复与项目处理进展。"
	p.Feedback.Settings.SubmitLabel = "提交咨询"
	p.Feedback.URL, p.Feedback.API = "/feedback/", "/api/v1/feedback/visual"
	p.Feedback.CanSubmit, p.Feedback.SignedIn = true, true
	p.Feedback.Settings.PrivateSubmissions, p.Feedback.Settings.Moderation = true, true
	p.Feedback.Item.CanComment, p.Feedback.Item.CanEdit = true, true
	p.Feedback.Item.Title = "跨区域业务系统集成与数据协同项目咨询"
	p.Feedback.Item.Body = "希望了解实施范围、交付周期与技术支持方式。\n现有系统需要统一身份认证和数据同步。"
	p.Feedback.Item.StatusLabel = "处理中"
	p.Post.Title = "以技术连接业务，为企业创造可持续的价值"
	p.Post.ContentHTML = "<h2>业务挑战</h2><p>连接业务系统，让团队围绕共同目标协作。</p><h2>解决方案</h2><p>从需求调研到方案设计、实施交付与持续服务，每一个阶段都清晰可见。</p>"
	p.Posts = []presentation.Post{p.Post, p.Post, p.Post}
	p.Post.CoverURL = "/__component_fixture_architecture.webp"
	if kind == "home" {
		for _, key := range []string{"show_cases", "show_resources", "show_process", "show_facts", "show_partners", "show_faq"} {
			p.Settings[key] = true
		}
		for i := 1; i <= 3; i++ {
			p.Settings[fmt.Sprintf("case_%d_title", i)] = "企业数字化协同解决方案"
			p.Settings[fmt.Sprintf("case_%d_text", i)] = "面向多场景业务，连接数据与服务。"
			p.Settings[fmt.Sprintf("case_%d_image", i)] = "/__component_fixture_architecture.webp"
			p.Settings[fmt.Sprintf("case_%d_url", i)] = "/__nexora_visual/page"
			p.Settings[fmt.Sprintf("resource_%d_title", i)] = "产品与解决方案白皮书"
			p.Settings[fmt.Sprintf("resource_%d_text", i)] = "PDF · 2026 年度版 · 产品架构与服务能力"
			p.Settings[fmt.Sprintf("resource_%d_url", i)] = "/__nexora_visual/page"
			p.Settings[fmt.Sprintf("job_%d_title", i)] = "高级解决方案工程师"
			p.Settings[fmt.Sprintf("job_%d_meta", i)] = "上海 / 研发中心 / 全职"
			p.Settings[fmt.Sprintf("job_%d_text", i)] = "参与方案设计与项目交付，推动技术落地。"
			p.Settings[fmt.Sprintf("job_%d_url", i)] = "/__nexora_visual/page"
		}
		p.Settings["fact_1_value"], p.Settings["fact_1_label"] = "端到端", "从需求调研到持续服务"
		p.Settings["partner_1_name"] = "合作伙伴示例"
		p.Settings["faq_1_question"], p.Settings["faq_1_answer"] = "如何开始合作？", "通过联系入口沟通具体需求。"
	}
	p.Settings["contact_email"] = "hello@example.test"
	p.Settings["page_contact_cta"] = true
	if r.URL.Query().Get("long") == "1" {
		p.Site.Title = "Nexora 面向全球企业的数字化技术与可持续创新服务集团"
	}
	if r.URL.Query().Get("compact") == "1" {
		p.ThemeClass = "nexora-density-compact"
	}
	if kind == "feedback-list" {
		p.Kind = "feedback"
		p.Feedback.Item = presentation.FeedbackItem{}
	}
	if kind == "feedback-directory" {
		p.Kind = "feedback"
		p.Feedback.Directory = true
	}
	if kind == "questions-list" {
		p.Kind = "questions"
		p.Questions.Question = presentation.Question{}
	}
	if kind == "announcements" {
		p.Kind = "page"
		p.BodyClass = "is-page is-announcements"
	}
	if kind == "announcements-list" {
		p.Kind = "page"
		p.BodyClass = "is-page is-announcements"
		p.AnnouncementPage.Current = presentation.AnnouncementItem{}
	}
	if r.URL.Query().Get("static") == "1" {
		p.IsPreview = true
	}
	var output theme.RenderBuffer
	if err := manager.Render(&output, p.Kind, p); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(output.Bytes())
}
