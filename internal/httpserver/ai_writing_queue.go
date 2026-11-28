package httpserver

import (
	"crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
)

const aiQueueHome = "/admin/plugins/ai-pro-max/#writing-queue"

type writingModelOption struct {
	Value, Label string
	Selected     bool
}
type writingQueueRow struct {
	Kind, Result                                   string
	Priority                                       int
	PostID                                         int64
	ID, Topic, Due, Status, Label, Detail, Problem string
	Version                                        int64
	Attempts                                       int
}
type writingQueueView struct {
	Rules                     aijobs.Rules
	Schedules                 []aijobs.Schedule
	Ready, Paused             bool
	Message, Key, Epoch, Zone string
	Status                    string
	ControlVersion            int64
	Categories                []aieditor.Category
	Models                    []writingModelOption
	Rows                      []writingQueueRow
}

func (s *Server) writingQueueView(r *http.Request, p auth.Principal, ro, enabled bool, settings aieditor.Settings) writingQueueView {
	v := writingQueueView{Zone: s.siteLocation(r.Context()).String()}
	switch {
	case !enabled:
		v.Message = "启用插件后可管理写作队列。"
		return v
	case !s.moduleEnabled("ai"):
		v.Message = "AI 助手模块已关闭。"
		return v
	case ro || !canCreateContent(p):
		v.Message = "当前账号没有排程写作权限。"
		return v
	}
	state, err := s.aiJobs.Snapshot()
	if err != nil {
		v.Message = "队列记录暂时不可读；已有记录保留在插件数据目录中。"
		return v
	}
	v.Categories, err = s.aiEditorCategories(r.Context())
	if err != nil {
		v.Message = "暂时无法读取写作分类。"
		return v
	}
	v.Rules = s.defaultProRules(state.Rules, settings)
	v.Schedules = state.Schedules
	v.Ready, v.Paused, v.ControlVersion, v.Epoch, v.Key = true, state.Paused, state.ControlVersion, state.Epoch, rand.Text()
	v.Models = s.writingModels(settings)
	switch {
	case v.Paused:
		v.Status = "已暂停领取；恢复队列后才会处理新任务。"
	case len(v.Models) == 0:
		v.Status = "待配置：没有可用的文本模型，请先配置 AI 接口。"
	default:
		v.Status = "队列已启用，文本模型配置可用；任务执行结果以记录为准。"
	}
	names := map[int64]string{}
	for _, c := range v.Categories {
		names[c.ID] = c.Name
	}
	zone := s.siteLocation(r.Context())
	for _, j := range state.Jobs {
		target := "文章"
		if j.PostType == content.TypePage {
			target = "页面"
		} else if j.CategoryID != 0 {
			name := names[j.CategoryID]
			if name == "" {
				name = "原分类已移除"
			}
			target += " / " + name
		}
		v.Rows = append(v.Rows, writingQueueRow{Kind: j.Work.Kind, Result: proResult(j), Priority: j.Priority, PostID: j.Work.PostID, ID: j.ID, Topic: j.Topic, Due: j.Due.In(zone).Format("2006-01-02 15:04"), Status: j.Status, Label: j.Label(), Version: j.Version, Attempts: j.Attempts, Detail: target + " · " + j.Model + " · " + j.Author, Problem: j.Problem})
	}
	return v
}

func queueFormShape(f url.Values, names ...string) bool {
	if len(f) != len(names) {
		return false
	}
	for _, k := range names {
		if len(f[k]) != 1 {
			return false
		}
	}
	return true
}

func (s *Server) adminAIWritingQueueAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, form, ok := s.requireContentWriteLimit(w, r, 256<<10, "ai_queue_form_too_large")
	if !ok {
		return
	}
	if !p.Can(auth.CapabilityPluginsManage) || !canCreateContent(p) || !s.sameOriginPublicWrite(r) {
		s.renderError(w, 403, "当前请求不能更改写作队列")
		return
	}
	if r.URL.RawQuery != "" {
		s.renderError(w, 400, "队列表单不接受查询参数")
		return
	}
	lease, err := s.aiProMaxLease()
	if err != nil || !lease.active() {
		s.queueError(w, plugin.ErrInactive)
		return
	}
	now := time.Now()
	action := form.Get("action")
	epoch := form.Get("epoch")
	if action == "plan" {
		if !queueFormShape(form, "csrf_token", "epoch", "action", "key", "topics", "model", "target", "language", "start", "interval") {
			s.queueError(w, aijobs.ErrInvalid)
			return
		}
		topics := strings.FieldsFunc(form.Get("topics"), func(r rune) bool { return r == '\n' || r == '\r' })
		topics = slices.DeleteFunc(topics, func(s string) bool { return strings.TrimSpace(s) == "" })
		if len(topics) == 0 || len(topics) > aijobs.MaxTopics {
			s.renderError(w, 422, "每次计划需要 1 到 20 个选题，每行一个。")
			return
		}
		provider, model, ok := strings.Cut(form.Get("model"), ":")
		if !ok || provider == "" || model == "" {
			s.queueError(w, aijobs.ErrModel)
			return
		}
		if _, err = s.aiSettings.Resolve(aieditor.Connection{ProviderID: provider, Model: model}, true); err != nil {
			s.queueError(w, aijobs.ErrModel)
			return
		}
		interval, err := strconv.Atoi(form.Get("interval"))
		if err != nil || interval < 0 || interval > 43200 {
			s.renderError(w, 422, "生成间隔必须为 0 到 43200 分钟。")
			return
		}
		plan := aijobs.Plan{Key: form.Get("key"), Author: p.Username, UserID: p.UserID, Topics: topics, Provider: provider, Model: model, PostType: content.TypePost, Language: form.Get("language"), IntervalMinutes: interval}
		plan.PostType, plan.CategoryID, err = writingTarget(form.Get("target"))
		if err != nil {
			s.queueError(w, err)
			return
		}
		all, err := s.aiEditorCategories(r.Context())
		if err != nil {
			s.queueError(w, err)
			return
		}
		categories, err := writingCategories(all, plan.PostType, plan.CategoryID)
		if err != nil {
			s.queueError(w, err)
			return
		}
		for _, topic := range topics {
			if _, err = writingMessages(topic, plan.Language, categories); err != nil {
				s.renderError(w, 422, err.Error())
				return
			}
		}
		if raw := form.Get("start"); raw != "" {
			plan.Start, err = parsePublishedLocal(raw, s.siteLocation(r.Context()))
			if err != nil {
				s.renderError(w, 422, "开始时间无效："+err.Error())
				return
			}
		}
		if !lease.active() {
			s.queueError(w, plugin.ErrInactive)
			return
		}
		if err = s.aiJobs.Enqueue(epoch, plan, now); err != nil {
			s.queueError(w, err)
			return
		}
	} else {
		if !queueFormShape(form, "csrf_token", "epoch", "action", "id", "version") {
			s.queueError(w, aijobs.ErrInvalid)
			return
		}
		version, err := positivePathID(form.Get("version"))
		if err != nil {
			s.queueError(w, aijobs.ErrInvalid)
			return
		}
		if err = s.aiJobs.Action(epoch, form.Get("id"), version, action, now); err != nil {
			s.queueError(w, err)
			return
		}
	}
	http.Redirect(w, r, aiQueueHome, http.StatusSeeOther)
}

func (s *Server) queueError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, plugin.ErrInactive):
		s.renderError(w, 404, "AI Pro Max 或 AI 助手模块未启用")
	case errors.Is(err, aijobs.ErrNotFound):
		s.renderError(w, 404, aijobs.ErrNotFound.Error())
	case errors.Is(err, aijobs.ErrConflict), errors.Is(err, aijobs.ErrFull):
		s.renderError(w, 409, err.Error())
	case errors.Is(err, aijobs.ErrInvalid), errors.Is(err, aijobs.ErrCategory), errors.Is(err, aijobs.ErrModel):
		s.renderError(w, 422, err.Error())
	default:
		s.renderError(w, 503, "暂时无法读写队列，请稍后重试")
	}
}

func (s *Server) adminAIWritingQueueDraft(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ro, ok := s.requireAdminPageCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	if ro || !canCreateContent(p) {
		s.renderError(w, 403, "当前账号不能编辑生成结果")
		return
	}
	if r.URL.RawQuery != "" {
		s.queueError(w, aijobs.ErrInvalid)
		return
	}
	lease, err := s.aiProMaxLease()
	if err != nil {
		s.queueError(w, err)
		return
	}
	st, err := s.aiJobs.Snapshot()
	if err != nil {
		s.queueError(w, err)
		return
	}
	i := slices.IndexFunc(st.Jobs, func(j aijobs.Job) bool { return j.ID == r.PathValue("id") })
	if i < 0 {
		s.queueError(w, aijobs.ErrNotFound)
		return
	}
	j := st.Jobs[i]
	if j.Work.Kind != "" || j.Status != "succeeded" {
		s.renderError(w, 409, "任务尚无可编辑的结果")
		return
	}
	all, err := s.aiEditorCategories(r.Context())
	if err != nil {
		s.queueError(w, err)
		return
	}
	doc, err := aieditor.ParseDocument(j.Result, all)
	if err != nil {
		s.renderError(w, 422, "生成结果不能转换为草稿")
		return
	}
	input := s.newContentInput(r, p, j.PostType)
	input.Status, input.Title, input.Slug, input.Excerpt, input.ContentSource = content.StatusDraft, doc.Title, doc.Slug, doc.Excerpt, doc.ContentSource
	message := "已载入队列结果。请核对后保存草稿。"
	if j.PostType == content.TypePost {
		input.TagNames = doc.Tags
		input.CategoryIDs = doc.CategoryIDs
		if j.CategoryID != 0 {
			if _, err := writingCategories(all, j.PostType, j.CategoryID); err == nil {
				input.CategoryIDs = []int64{j.CategoryID}
			} else {
				message += " 原分类已移除，请重新选择分类。"
			}
		}
	}
	if !lease.active() {
		s.queueError(w, plugin.ErrInactive)
		return
	}
	s.renderContentEditor(w, r, 200, p, false, content.Post{}, input, nil, adminNotice{message, "info"}, false, nil)
}
