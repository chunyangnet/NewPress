package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

type aiEditorState struct {
	mu     sync.Mutex
	active map[int64]bool
}

func (s *Server) SetAISettings(settings *aieditor.SettingsStore) {
	if settings != nil {
		s.aiSettings = settings
	}
}

func (s *Server) adminAIConnections(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn); !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, false, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	connections := s.aiSettings.Snapshot()
	connections.Telemetry = nil
	for i := range connections.Providers {
		connections.Providers[i].Pricing = nil
		connections.Providers[i].Balance = aieditor.BalanceConfig{}
	}
	access.json(w, connections)
}

func (s *Server) adminAISettings(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, false, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	access.json(w, s.aiSettings.Snapshot())
}

func (s *Server) adminAISettingsSave(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, true, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input aieditor.Settings
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 512<<10, "512 KiB"); !ok {
		return
	}
	if !access.recheck(w) {
		return
	}
	saved, err := s.aiSettings.SaveChecked(input, access.access)
	if !access.recheck(w) {
		return
	}
	if err != nil {
		access.fail(w, err)
		return
	}
	access.json(w, saved)
}

func (state *aiEditorState) acquire(user int64) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.active[user] || len(state.active) >= 4 {
		return false
	}
	if state.active == nil {
		state.active = make(map[int64]bool)
	}
	state.active[user] = true
	return true
}

func (state *aiEditorState) release(user int64) {
	state.mu.Lock()
	defer state.mu.Unlock()
	delete(state.active, user)
}

func (s *Server) aiEditorCategories(ctx context.Context) ([]aieditor.Category, error) {
	terms, err := s.repository.ListTerms(ctx, content.TermQuery{Taxonomy: content.TaxonomyCategory, Limit: 500})
	if err != nil {
		return nil, err
	}
	list := make([]aieditor.Category, 0, len(terms.Items))
	for _, term := range terms.Items {
		list = append(list, aieditor.Category{ID: term.ID, Name: term.Name})
	}
	return list, nil
}

func (s *Server) aiEditorError(w http.ResponseWriter, r *http.Request, err error) {
	var provider *aieditor.Error
	if errors.As(err, &provider) {
		s.writeAPIError(w, r, provider.Status, "ai."+provider.Code, provider.Message, nil)
	} else if errors.Is(err, context.DeadlineExceeded) {
		s.writeAPIError(w, r, http.StatusGatewayTimeout, "ai.timeout", "AI 请求超时，请稍后重试", nil)
	} else {
		s.writeAPIError(w, r, http.StatusBadGateway, "ai.unavailable", "AI 请求暂时不可用，请稍后重试", nil)
	}
}

func (s *Server) adminAIPrompts(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn, auth.CapabilitySettingsManage); !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, false, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	team, err := s.writingTemplates.Catalog(r.Context())
	if !access.recheck(w) {
		return
	}
	if err != nil {
		s.writingTemplateError(w, r, err)
		return
	}
	prompts := make([]any, 0, len(aieditor.Prompts())+len(team))
	for _, p := range aieditor.Prompts() {
		prompts = append(prompts, p)
	}
	for _, p := range team {
		prompts = append(prompts, p)
	}
	access.json(w, map[string]any{"prompts": prompts})
}

func (s *Server) adminAIModels(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, true, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		aieditor.Connection
		AllowInsecure bool `json:"allow_insecure"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 16<<10, "16 KiB"); !ok {
		return
	}
	access.approveEndpoint = input.AllowInsecure
	if !access.recheck(w) {
		return
	}
	// Model discovery can take longer than the normal page write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(25 * time.Second))
	var connection aieditor.Connection
	var err error
	if input.AllowInsecure {
		connection, err = aieditor.ApproveProbeConnection(input.Connection)
	} else {
		connection, err = s.aiSettings.Resolve(input.Connection, false)
	}
	if !access.recheck(w) {
		return
	}
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	models, err := access.models(connection)
	if !access.recheck(w) {
		return
	}
	if err != nil {
		access.fail(w, err)
		return
	}
	access.json(w, map[string]any{"models": models, "base_url": connection.BaseURL})
}

func (s *Server) adminAIPrepare(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn); !ok {
		return
	}
	access, ok := s.newAIAdminAuthority(w, r, true, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		Title       string   `json:"title"`
		Slug        string   `json:"slug"`
		Excerpt     string   `json:"excerpt"`
		Tags        []string `json:"tags"`
		CategoryIDs []int64  `json:"category_ids"`
		CoverPrompt string   `json:"cover_prompt"`
		Markdown    string   `json:"markdown"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 1<<20, "1 MiB"); !ok {
		return
	}
	if !access.recheck(w) {
		return
	}
	categories, err := s.aiEditorCategories(r.Context())
	if !access.recheck(w) {
		return
	}
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	doc, err := aieditor.PrepareDocument(aieditor.Document{Title: input.Title, Slug: input.Slug, Excerpt: input.Excerpt, Tags: input.Tags, CategoryIDs: input.CategoryIDs, CoverPrompt: input.CoverPrompt, Markdown: input.Markdown}, categories)
	if !access.recheck(w) {
		return
	}
	if err != nil {
		s.writeAPIError(w, r, http.StatusBadRequest, "ai.invalid_document", err.Error(), nil)
		return
	}
	access.json(w, doc)
}

type aiStreamEvent struct {
	Name string
	Data any
}

func (s *Server) adminAIGenerate(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	module, enabled := s.siteSettings.ModuleContext("ai")
	if !enabled {
		s.writeAPIError(w, r, 409, "ai.inactive", "AI 模块已停用", nil)
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		aieditor.Connection
		aieditor.Generation
		TemplateVersion int64 `json:"template_version"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 256<<10, "256 KiB"); !ok {
		return
	}
	if !s.recheckAIGeneration(w, r, module) {
		return
	}
	if strings.HasPrefix(input.PromptID, "team:") {
		p, err := s.writingTemplates.Resolve(r.Context(), strings.TrimPrefix(input.PromptID, "team:"), input.TemplateVersion)
		if !s.recheckAIGeneration(w, r, module) {
			return
		}
		if err != nil {
			s.writingTemplateError(w, r, err)
			return
		}
		input.PromptID = p.ID
		if strings.TrimSpace(input.Instruction) == "" {
			input.Instruction = p.Instruction
		}
	} else if input.TemplateVersion != 0 {
		s.writeAPIError(w, r, http.StatusBadRequest, "ai.template_invalid", "内置模板不接受团队版本号", nil)
		return
	}
	connection, err := s.aiSettings.Resolve(input.Connection, true)
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	categories, err := s.aiEditorCategories(r.Context())
	if !s.recheckAIGeneration(w, r, module) {
		return
	}
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	messages, err := input.Generation.Messages(categories)
	if err != nil {
		s.writeAPIError(w, r, http.StatusBadRequest, "ai.invalid_brief", err.Error(), nil)
		return
	}
	if !s.recheckAIGeneration(w, r, module) {
		return
	}
	s.streamAIGeneration(w, r, principal, module, connection, input.ProviderID, messages, categories, input.MaxTokens)
}
