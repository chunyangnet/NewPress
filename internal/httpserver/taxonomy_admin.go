package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/site"
)

type adminTaxonomyView struct {
	Taxonomy      string
	Label         string
	Description   string
	Search        string
	Rows          []adminTermRow
	ParentTerms   []content.Term
	Form          adminTermForm
	CategoryCount int
	TagCount      int
	Total         int
	Page          int
	Pages         int
	PreviousURL   string
	NextURL       string
	HasPrevious   bool
	HasNext       bool
}

type adminTermRow struct {
	Term         content.Term
	DepthClass   string
	EditURL      string
	ArchiveURL   string
	UpdatedLabel string
}

type adminTermForm struct {
	ID          int64
	Version     int64
	Taxonomy    string
	ParentID    int64
	Name        string
	Slug        string
	Description string
	System      bool
	Count       int
}

func (s *Server) adminTaxonomy(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/admin/taxonomy/" {
		s.renderError(writer, http.StatusNotFound, "分类管理页面没有找到")
		return
	}
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityTaxonomyManage)
	if !ok {
		return
	}
	query, ok := strictQueryShape(request.URL.Query(), map[string]bool{
		"taxonomy": true, "q": true, "page": true, "edit": true, "notice": true,
	})
	if !ok {
		s.renderError(writer, http.StatusBadRequest, "分类管理筛选参数不正确")
		return
	}
	taxonomy := strings.ToLower(strings.TrimSpace(query.Get("taxonomy")))
	if taxonomy == "" {
		taxonomy = content.TaxonomyCategory
	}
	if !content.ValidTaxonomy(taxonomy) {
		s.renderError(writer, http.StatusBadRequest, "分类法参数不正确")
		return
	}
	search := strings.TrimSpace(query.Get("q"))
	if _, err := content.NormalizeTermQuery(content.TermQuery{Taxonomy: taxonomy, Search: search, Limit: 1}); err != nil {
		s.renderError(writer, http.StatusBadRequest, "分类搜索参数不正确")
		return
	}
	page, err := positiveQueryInt(query.Get("page"), 1, 100000)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "分页参数不正确")
		return
	}
	form := adminTermForm{Taxonomy: taxonomy}
	if rawEdit := strings.TrimSpace(query.Get("edit")); rawEdit != "" {
		id, parseErr := strconv.ParseInt(rawEdit, 10, 64)
		if parseErr != nil || id <= 0 {
			s.renderError(writer, http.StatusBadRequest, "术语编号不正确")
			return
		}
		term, loadErr := s.repository.GetTermByID(request.Context(), id)
		if errors.Is(loadErr, content.ErrTermNotFound) {
			s.renderError(writer, http.StatusNotFound, "分类或标签没有找到")
			return
		}
		if loadErr != nil {
			slog.Error("load term for editing", "id", id, "error", loadErr)
			s.renderError(writer, http.StatusInternalServerError, "分类信息暂时不可用")
			return
		}
		taxonomy = term.Taxonomy
		form = termFormFromTerm(term)
	}
	s.renderAdminTaxonomy(writer, request, http.StatusOK, principal, readOnly, taxonomy,
		search, page, form, nil, taxonomyNoticeFromQuery(query.Get("notice")))
}

func (s *Server) adminTaxonomySubmit(writer http.ResponseWriter, request *http.Request) {
	principal, form, ok := s.requireTaxonomyWrite(writer, request)
	if !ok {
		return
	}
	if invalidTaxonomyForm(form) != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_taxonomy_form_shape"})
		return
	}
	action := form.Get("action")
	taxonomy := strings.ToLower(strings.TrimSpace(form.Get("taxonomy")))
	if !content.ValidTaxonomy(taxonomy) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_taxonomy"})
		return
	}
	termForm, fields := parseTermForm(form)
	termForm.Taxonomy = taxonomy
	input := content.TermInput{
		Taxonomy: taxonomy, ParentID: termForm.ParentID, Slug: termForm.Slug,
		Name: termForm.Name, Description: termForm.Description,
	}
	normalized, validationFields := content.NormalizeTermInput(input)
	for key, value := range validationFields {
		fields[key] = value
	}
	termForm.Name = normalized.Name
	termForm.Slug = normalized.Slug
	termForm.Description = normalized.Description
	if action == "delete" {
		fields = nil
	}
	if len(fields) > 0 {
		s.renderAdminTaxonomy(writer, request, http.StatusUnprocessableEntity, principal, false, taxonomy, "", 1, termForm, fields,
			adminNotice{Message: "请检查标记字段后再保存", Tone: "error"})
		return
	}
	var err error
	switch action {
	case "create":
		if termForm.ID != 0 || termForm.Version != 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unexpected_term_identity"})
			return
		}
		_, err = s.repository.CreateTerm(request.Context(), normalized)
	case "update":
		if termForm.ID <= 0 || termForm.Version <= 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_term_identity"})
			return
		}
		_, err = s.repository.UpdateTerm(request.Context(), termForm.ID,
			content.TermMutation{ExpectedVersion: termForm.Version, Actor: principal.Username}, normalized)
	case "delete":
		if termForm.ID <= 0 || termForm.Version <= 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_term_identity"})
			return
		}
		err = s.repository.DeleteTerm(request.Context(), termForm.ID,
			content.TermMutation{ExpectedVersion: termForm.Version, Actor: principal.Username})
	default:
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unknown_taxonomy_action"})
		return
	}
	if err != nil {
		s.handleTaxonomyMutationError(writer, request, principal, taxonomy, action, termForm, err)
		return
	}
	notice := "updated"
	if action == "create" {
		notice = "created"
	} else if action == "delete" {
		notice = "deleted"
	}
	http.Redirect(writer, request, taxonomyAdminURL(taxonomy, "", 1, 0)+"&notice="+notice, http.StatusSeeOther)
}

func (s *Server) renderAdminTaxonomy(writer http.ResponseWriter, request *http.Request, status int, principal auth.Principal, readOnly bool, taxonomy, search string, page int, form adminTermForm, fieldErrors map[string]string, notice adminNotice) {
	pageSize := max(1, s.siteSnapshot(request.Context()).Settings.Reading.AdminPageSize)
	query := content.TermQuery{Taxonomy: taxonomy, Search: search, Limit: pageSize, Offset: (page - 1) * pageSize}
	result, err := s.repository.ListTerms(request.Context(), query)
	if err != nil {
		slog.Error("list taxonomy terms", "taxonomy", taxonomy, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "分类与标签暂时不可用")
		return
	}
	parents, err := s.repository.ListTerms(request.Context(), content.TermQuery{Taxonomy: content.TaxonomyCategory, Limit: 500})
	if err != nil {
		slog.Error("list category parents", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "分类层级暂时不可用")
		return
	}
	parentTerms := filterTermParentOptions(parents.Items, form.ID)
	location := site.TimeLocation(s.siteSnapshot(request.Context()).Settings)
	rows := make([]adminTermRow, 0, len(result.Items))
	for _, term := range result.Items {
		rows = append(rows, adminTermRow{
			Term: term, DepthClass: "depth-" + strconv.Itoa(min(term.Depth, 5)),
			EditURL:    taxonomyAdminURL(term.Taxonomy, search, page, term.ID),
			ArchiveURL: termArchiveURL(term), UpdatedLabel: term.UpdatedAt.In(location).Format("2006-01-02 15:04"),
		})
	}
	pageCount := max(1, (result.Total+pageSize-1)/pageSize)
	label, description := taxonomyPresentation(taxonomy)
	if form.Taxonomy == "" {
		form.Taxonomy = taxonomy
	}
	data := s.baseViewFor(writer, "分类与标签", "建立可复用、可归档、可迁移的内容发现体系", "/admin/taxonomy/")
	data.BodyClass = "is-admin is-admin-taxonomy"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("taxonomy", principal, readOnly)
	data.Admin.FieldErrors = fieldErrors
	data.Admin.Notice = notice.Message
	data.Admin.NoticeTone = notice.Tone
	data.Taxonomy = adminTaxonomyView{
		Taxonomy: taxonomy, Label: label, Description: description, Search: search,
		Rows: rows, ParentTerms: parentTerms, Form: form,
		CategoryCount: result.Counts[content.TaxonomyCategory], TagCount: result.Counts[content.TaxonomyTag],
		Total: result.Total, Page: page, Pages: pageCount,
		HasPrevious: page > 1, HasNext: page < pageCount,
		PreviousURL: taxonomyAdminURL(taxonomy, search, max(1, page-1), 0),
		NextURL:     taxonomyAdminURL(taxonomy, search, min(pageCount, page+1), 0),
	}
	s.render(writer, status, "admin-taxonomy", data)
}

func (s *Server) requireTaxonomyWrite(writer http.ResponseWriter, request *http.Request) (auth.Principal, url.Values, bool) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityTaxonomyManage)
	if !ok {
		return auth.Principal{}, nil, false
	}
	if readOnly || !s.auth.Configured() {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "admin_credentials_not_configured"})
		return auth.Principal{}, nil, false
	}
	if !s.sameOriginRequest(request) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "cross_site_request_rejected"})
		return auth.Principal{}, nil, false
	}
	if !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "form_content_type_required"})
		return auth.Principal{}, nil, false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
	if err := request.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]string{"error": "taxonomy_form_too_large"})
			return auth.Principal{}, nil, false
		}
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_taxonomy_form"})
		return auth.Principal{}, nil, false
	}
	form := request.PostForm
	if len(form["csrf_token"]) != 1 || !s.auth.CheckCSRF(principal, form.Get("csrf_token")) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return auth.Principal{}, nil, false
	}
	return principal, form, true
}

func parseTermForm(form url.Values) (adminTermForm, map[string]string) {
	result := adminTermForm{
		Taxonomy: strings.TrimSpace(form.Get("taxonomy")), Name: form.Get("name"),
		Slug: form.Get("slug"), Description: form.Get("description"),
	}
	fields := make(map[string]string)
	if raw := strings.TrimSpace(form.Get("id")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			fields["id"] = "术语编号不正确"
		} else {
			result.ID = value
		}
	}
	if raw := strings.TrimSpace(form.Get("version")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			fields["version"] = "术语版本不正确"
		} else {
			result.Version = value
		}
	}
	if raw := strings.TrimSpace(form.Get("parent_id")); raw != "" && raw != "0" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			fields["parent_id"] = "父级分类不正确"
		} else {
			result.ParentID = value
		}
	}
	return result, fields
}

func invalidTaxonomyForm(form url.Values) string {
	allowed := map[string]bool{
		"csrf_token": true, "action": true, "taxonomy": true, "id": true, "version": true,
		"name": true, "slug": true, "parent_id": true, "description": true,
	}
	for key, values := range form {
		if !allowed[key] || len(values) != 1 {
			return key
		}
	}
	return ""
}

func (s *Server) handleTaxonomyMutationError(writer http.ResponseWriter, request *http.Request, principal auth.Principal, taxonomy, action string, form adminTermForm, err error) {
	status := http.StatusUnprocessableEntity
	fields := content.FieldErrors(err)
	notice := adminNotice{Message: "分类或标签没有通过服务端校验", Tone: "error"}
	switch {
	case errors.Is(err, content.ErrTermSlugConflict):
		status = http.StatusConflict
		fields = map[string]string{"slug": "同一分类法已使用这个别名"}
		notice.Message = "别名冲突，请更换后重试"
	case errors.Is(err, content.ErrTermVersionConflict):
		status = http.StatusConflict
		notice.Message = "这项分类刚刚被其他请求修改，请刷新后再提交"
	case errors.Is(err, content.ErrTermHierarchyConflict):
		fields = map[string]string{"parent_id": "父级不能指向自身或自己的下级"}
		notice.Message = "分类层级会形成循环"
	case errors.Is(err, content.ErrTermProtected):
		status = http.StatusConflict
		notice.Message = "系统默认分类不能删除，可修改名称和说明"
	case errors.Is(err, content.ErrTermNotFound):
		s.renderError(writer, http.StatusNotFound, "分类或标签没有找到")
		return
	case len(fields) == 0:
		slog.Error("mutate taxonomy term", "action", action, "id", form.ID, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "分类与标签暂时无法保存")
		return
	}
	if form.ID > 0 {
		if current, loadErr := s.repository.GetTermByID(request.Context(), form.ID); loadErr == nil {
			form.Version = current.Version
			form.System = current.System
			form.Count = current.Count
		}
	}
	s.renderAdminTaxonomy(writer, request, status, principal, false, taxonomy, "", 1, form, fields, notice)
}

func termFormFromTerm(term content.Term) adminTermForm {
	return adminTermForm{
		ID: term.ID, Version: term.Version, Taxonomy: term.Taxonomy, ParentID: term.ParentID,
		Name: term.Name, Slug: term.Slug, Description: term.Description, System: term.System, Count: term.Count,
	}
}

func filterTermParentOptions(items []content.Term, editingID int64) []content.Term {
	if editingID <= 0 {
		return items
	}
	parents := make(map[int64]int64, len(items))
	for _, term := range items {
		parents[term.ID] = term.ParentID
	}
	result := make([]content.Term, 0, len(items))
	for _, term := range items {
		if term.ID == editingID || termDescendsFrom(term.ID, editingID, parents) {
			continue
		}
		result = append(result, term)
	}
	return result
}

func termDescendsFrom(termID, ancestorID int64, parents map[int64]int64) bool {
	seen := make(map[int64]bool)
	for current := parents[termID]; current > 0 && !seen[current]; current = parents[current] {
		if current == ancestorID {
			return true
		}
		seen[current] = true
	}
	return false
}

func taxonomyPresentation(taxonomy string) (string, string) {
	if taxonomy == content.TaxonomyTag {
		return "文章标签", "建立横向主题网络；标签无父子层级，可在编辑器中输入后自动复用。"
	}
	return "分类目录", "用父子层级组织长期内容栏目；文章至少保留一个分类，删除后自动回落到默认分类。"
}

func taxonomyAdminURL(taxonomy, search string, page int, editID int64) string {
	query := url.Values{"taxonomy": []string{taxonomy}}
	if search != "" {
		query.Set("q", search)
	}
	if page > 1 {
		query.Set("page", strconv.Itoa(page))
	}
	if editID > 0 {
		query.Set("edit", strconv.FormatInt(editID, 10))
	}
	return "/admin/taxonomy/?" + query.Encode()
}

func termArchiveURL(term content.Term) string {
	prefix := "/tags/"
	if term.Taxonomy == content.TaxonomyCategory {
		prefix = "/categories/"
	}
	return prefix + url.PathEscape(term.Slug) + "/"
}

func taxonomyNoticeFromQuery(value string) adminNotice {
	switch value {
	case "created":
		return adminNotice{Message: "分类或标签已创建，可立即用于文章归档", Tone: "success"}
	case "updated":
		return adminNotice{Message: "术语元数据与公开归档已同步更新", Tone: "success"}
	case "deleted":
		return adminNotice{Message: "术语已删除；文章关系已安全清理或回落到默认分类", Tone: "success"}
	default:
		return adminNotice{}
	}
}
