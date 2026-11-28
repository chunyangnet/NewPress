package httpserver

import (
	"fmt"
	"net/http"
	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Server) defaultProRules(r aijobs.Rules, settings aieditor.Settings) aijobs.Rules {
	if r.Version == 0 {
		r.TopicLimit = 3
		r.Language = "auto"
		r.SafeUpdate = true
		r.Summary = true
		r.Tags = true
		r.Slug = true
		r.Category = true
		r.RetryLimit = 2
		r.Provider = settings.DefaultProvider
		for _, p := range settings.Providers {
			if p.ID == r.Provider {
				r.Model = p.DefaultModel
			}
		}
	}
	return r
}
func proResult(j aijobs.Job) string {
	if j.Work.Kind == "external" && j.Status == "succeeded" {
		return "外部内容的补全结果已就绪"
	}
	if j.Work.Kind != "" && j.Status == "succeeded" {
		return j.Result
	}
	return ""
}
func (s *Server) proSingleContent(w http.ResponseWriter, r *http.Request) {
	p, f, ok := s.requireContentWriteLimit(w, r, 4096, "ai_form_size")
	if !ok {
		return
	}
	if !queueFormShape(f, "csrf_token", "selection") {
		s.queueError(w, aijobs.ErrInvalid)
		return
	}
	s.proContentBatch(w, r, p, f["selection"])
}
func (s *Server) proContentBatch(w http.ResponseWriter, r *http.Request, p auth.Principal, items []string) {
	if !p.Can(auth.CapabilityPluginsManage) || !p.Can(auth.CapabilityContentWrite) || !s.sameOriginPublicWrite(r) {
		s.renderError(w, 403, "当前账号不能使用批量 AI 处理")
		return
	}
	state, ok := s.automationState()
	if !ok {
		s.renderError(w, 404, "请启用插件和 AI 助手")
		return
	}
	rules := s.defaultProRules(state.Rules, s.aiSettings.Snapshot())
	rules.Author = p.Username
	rules.UserID = p.UserID
	if _, err := s.aiSettings.Resolve(aieditor.Connection{ProviderID: rules.Provider, Model: rules.Model}, true); err != nil {
		s.queueError(w, aijobs.ErrModel)
		return
	}
	var jobs []aijobs.Job
	for _, v := range items {
		a, b, ok := strings.Cut(v, ":")
		id, e := positivePathID(a)
		ver, ve := positivePathID(b)
		if !ok || e != nil || ve != nil {
			s.queueError(w, aijobs.ErrInvalid)
			return
		}
		post, err := s.repository.GetByID(r.Context(), id)
		if err != nil || post.Version != ver || post.Status == content.StatusTrash {
			s.queueError(w, aijobs.ErrConflict)
			return
		}
		if wantsEnrichment(post, rules) {
			jobs = append(jobs, contentWork(post, rules, "enrich"))
		}
	}
	if err := s.aiJobs.AddWork(state.Epoch, jobs, time.Now()); err != nil {
		s.queueError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/plugins/ai-pro-max/?notice="+fmt.Sprint(len(jobs))+"#writing-queue", 303)
}
func (s *Server) proRulesForm(w http.ResponseWriter, r *http.Request) {
	p, f, ok := s.requireContentWriteLimit(w, r, 32<<10, "ai_form_size")
	if !ok {
		return
	}
	if !p.Can(auth.CapabilityPluginsManage) || !p.Can(auth.CapabilityContentWrite) || !s.sameOriginPublicWrite(r) {
		s.renderError(w, 403, "当前账号不能配置自动化")
		return
	}
	st, ok := s.automationState()
	if !ok {
		s.renderError(w, 404, "插件未启用")
		return
	}
	if st.Epoch != f.Get("epoch") {
		s.queueError(w, aijobs.ErrConflict)
		return
	}
	for _, v := range f {
		if len(v) != 1 {
			s.queueError(w, aijobs.ErrInvalid)
			return
		}
	}
	get := f.Get
	action := get("action")
	epoch := st.Epoch
	if action == "pause-schedule" || action == "resume-schedule" {
		if err := s.aiJobs.PauseSchedule(epoch, get("id"), action == "pause-schedule"); err != nil {
			s.queueError(w, err)
			return
		}
		http.Redirect(w, r, "/admin/plugins/ai-pro-max/#automation", 303)
		return
	}
	if action == "unschedule" {
		if err := s.aiJobs.Schedule(epoch, aijobs.Schedule{ID: get("id")}, true); err != nil {
			s.queueError(w, err)
			return
		}
		http.Redirect(w, r, aiQueueHome, 303)
		return
	}
	provider, model, _ := strings.Cut(get("model"), ":")
	if _, err := s.aiSettings.Resolve(aieditor.Connection{ProviderID: provider, Model: model}, true); err != nil {
		s.queueError(w, aijobs.ErrModel)
		return
	}
	lang := get("language")
	if aieditor.LanguageName(lang) == "" {
		s.queueError(w, aijobs.ErrInvalid)
		return
	}
	n := func(k string) int {
		v, err := strconv.Atoi(get(k))
		if err != nil {
			return -1
		}
		return v
	}
	category := int64(n("category"))
	if category < 0 {
		s.queueError(w, aijobs.ErrCategory)
		return
	}
	if category != 0 {
		all, e := s.aiEditorCategories(r.Context())
		if e != nil {
			s.queueError(w, e)
			return
		}
		if _, e = writingCategories(all, "post", category); e != nil {
			s.queueError(w, e)
			return
		}
	}
	var err error
	switch action {
	case "schedule":
		start := time.Now().UTC()
		if raw := get("start"); raw != "" {
			start, err = parsePublishedLocal(raw, s.siteLocation(r.Context()))
			if err != nil {
				s.queueError(w, aijobs.ErrInvalid)
				return
			}
		}
		if get("search") == "on" && os.Getenv("NEWPRESS_AI_SEARCH_TOKEN") == "" {
			s.renderError(w, 422, "请先配置 NEWPRESS_AI_SEARCH_TOKEN 搜索凭据")
			return
		}
		err = s.aiJobs.Schedule(epoch, aijobs.Schedule{Strategy: aijobs.Strategy{Style: get("style"), Search: get("search") == "on", Links: get("links") == "on", Images: n("images"), Ahead: n("ahead")}, ID: get("key"), Brief: get("brief"), Author: p.Username, UserID: p.UserID, Provider: provider, Model: model, Language: lang, CategoryID: category, Hours: n("hours"), Remaining: n("limit"), Next: start, Destination: get("destination")}, false)
	case "rules":
		on := func(k string) bool { return get(k) == "on" }
		version, e := strconv.ParseInt(get("version"), 10, 64)
		if e != nil {
			s.queueError(w, aijobs.ErrInvalid)
			return
		}
		rules := aijobs.Rules{TopicLimit: n("topic_limit"), TopicImages: on("topic_images"), External: on("external"), Version: version, UserID: p.UserID, Author: p.Username, Provider: provider, Model: model, Language: lang, AutoPost: on("auto_post"), AutoPage: on("auto_page"), Summary: on("summary"), Tags: on("tags"), Slug: on("slug"), Category: on("classify"), SafeUpdate: on("safe"), Topics: on("topics"), Cover: on("cover"), Inline: on("inline"), Vision: on("vision"), Fallback: on("fallback"), Moderate: on("moderate"), Reply: on("reply"), Followup: on("followup"), Persona: get("persona"), WakeWords: get("wake"), BlockWords: get("block"), HoldWords: get("hold"), DelayMinutes: n("delay"), RetryLimit: n("retry"), RandomHours: n("random"), CommentsPerPost: n("comments"), CategoryID: category, ExcludeCategory: on("exclude")}
		if (rules.CommentsPerPost > 0 || rules.RandomHours > 0 || rules.Moderate || rules.Reply || rules.Followup || rules.WakeWords != "") && !p.Can(auth.CapabilityCommentsModerate) || (rules.Cover || rules.Inline || rules.TopicImages) && !p.Can(auth.CapabilityMediaWrite) {
			s.renderError(w, 403, "当前账号缺少所选操作的权限")
			return
		}
		err = s.aiJobs.SaveRules(epoch, rules)
	default:
		err = aijobs.ErrInvalid
	}
	if err != nil {
		s.queueError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/plugins/ai-pro-max/#automation", 303)
}
func (s *Server) proParagraph(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	if !s.sameOriginPublicWrite(r) {
		s.writeAPIError(w, r, 403, "ai.origin", "请从本站编辑器操作", nil)
		return
	}
	lease, err := s.aiProMaxLease()
	if err != nil {
		s.writeAPIError(w, r, 404, "ai.inactive", "插件未启用", nil)
		return
	}
	var in struct{ Text, Language, Mode, Model string }
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &in, 48<<10, "48 KiB"); !ok {
		return
	}
	if !s.recheckAIEditorTool(w, r, lease) {
		return
	}
	mode := map[string]string{"polish": "润色", "shorten": "精简", "expand": "展开", "translate": "翻译"}[in.Mode]
	if mode == "" || strings.TrimSpace(in.Text) == "" || utf8.RuneCountInString(in.Text) > 8000 || aieditor.LanguageName(in.Language) == "" {
		s.writeAPIError(w, r, 422, "ai.selection", "请选择 1 到 8000 字并指定输出语言", nil)
		return
	}
	st, err := s.aiJobs.Snapshot()
	if !s.recheckAIEditorTool(w, r, lease) {
		return
	}
	if err != nil {
		s.queueError(w, err)
		return
	}
	rules := s.defaultProRules(st.Rules, s.aiSettings.Snapshot())
	if in.Model != "" {
		var ok bool
		rules.Provider, rules.Model, ok = strings.Cut(in.Model, ":")
		if !ok {
			s.queueError(w, aijobs.ErrModel)
			return
		}
	}
	if !s.aiState.acquire(p.UserID) {
		s.writeAPIError(w, r, 429, "ai.busy", "模型正忙", nil)
		return
	}
	defer s.aiState.release(p.UserID)
	ctx, cancel := lease.forRequest(r.Context(), 90*time.Second)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(95 * time.Second))
	access := func() error { return s.aiEditorToolAccess(r, lease) }
	text, err := s.autoTextChecked(ctx, aijobs.Job{Provider: rules.Provider, Model: rules.Model, UserID: p.UserID, Author: p.Username, Work: aijobs.WorkSpec{Kind: "paragraph", Rules: rules}}, quotedMessages("对所选段落执行"+mode+"。保留事实，只输出替换用纯文本，不要添加前言或 HTML", in.Text, in.Language), nil, 4096, access)
	if !s.recheckAIEditorTool(w, r, lease) {
		return
	}
	if err != nil {
		s.aiEditorToolError(w, r, err)
		return
	}
	writePrivateJSON(w, r, 200, map[string]string{"text": text}, nil, access, func(err error) { s.aiEditorToolError(w, r, err) })
}
