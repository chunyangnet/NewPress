package httpserver

import (
	"net/http"
	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/auth"
	"slices"
	"strings"
	"time"
)

func (s *Server) editorMetadata(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	lease, err := s.aiProMaxLease()
	if err != nil || !s.sameOriginPublicWrite(r) {
		s.writeAPIError(w, r, 403, "ai.metadata_scope", "请在已启用插件的编辑器中操作", nil)
		return
	}
	var input struct {
		Title, Body, Language, Model string
		Fields                       []string
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 150<<10, "150 KiB"); !ok {
		return
	}
	if len(input.Body) > 128<<10 || len(input.Title) > 1200 || len(input.Fields) == 0 || len(input.Fields) > 4 || strings.TrimSpace(input.Body) == "" || aieditor.LanguageName(input.Language) == "" {
		s.writeAPIError(w, r, 422, "ai.metadata_input", "请填写正文并勾选所需字段", nil)
		return
	}
	for _, f := range input.Fields {
		if !slices.Contains([]string{"excerpt", "tags", "slug", "categoryID"}, f) {
			s.writeAPIError(w, r, 400, "ai.metadata_field", "字段无效", nil)
			return
		}
	}
	st, err := s.aiJobs.Snapshot()
	if err != nil {
		s.queueError(w, err)
		return
	}
	rules := s.defaultProRules(st.Rules, s.aiSettings.Snapshot())
	if input.Model != "" {
		rules.Provider, rules.Model, _ = strings.Cut(input.Model, ":")
	}
	if !s.aiState.acquire(actor.UserID) {
		s.writeAPIError(w, r, 429, "ai.busy", "已有模型请求", nil)
		return
	}
	defer s.aiState.release(actor.UserID)
	ctx, cancel := lease.forRequest(r.Context(), 90*time.Second)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(95 * time.Second))
	categories, err := s.aiEditorCategories(ctx)
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	messages := quotedMessages("返回 JSON，按需要的字段给出 excerpt 简明摘要、slug 英文别名、tags 字符串数组、categoryID 一个已存在的分类编号", map[string]any{"标题": input.Title, "正文": aiCommentContext(input.Body, 16000), "需要的字段": input.Fields, "分类": categories}, input.Language)
	raw, err := s.autoText(ctx, aijobs.Job{Provider: rules.Provider, Model: rules.Model, UserID: actor.UserID, Work: aijobs.WorkSpec{Kind: "metadata", Rules: rules}}, messages, nil, 1200)
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	var out aiEnrichment
	if decodeAIObject(raw, &out) != nil {
		s.writeAPIError(w, r, 502, "ai.metadata_output", "模型没有返回有效元数据", nil)
		return
	}
	if !lease.active() {
		s.writeAPIError(w, r, 409, "ai.inactive", "操作已中止", nil)
		return
	}
	writeJSON(w, 200, out)
}
