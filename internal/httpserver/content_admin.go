package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func (s *Server) page(writer http.ResponseWriter, request *http.Request) {
	slug := request.PathValue("slug")
	if !content.ValidSlug(slug) {
		s.renderError(writer, http.StatusNotFound, "页面没有找到")
		return
	}
	post, err := s.repository.GetPublishedPageBySlug(request.Context(), slug)
	if errors.Is(err, content.ErrNotFound) {
		if s.tryContentRedirect(writer, request) {
			return
		}
		s.renderError(writer, http.StatusNotFound, "页面没有找到")
		return
	}
	if err != nil {
		slog.Error("load page", "slug", slug, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "页面暂时不可用")
		return
	}
	if s.canonicalContentRedirect(writer, request, post) {
		return
	}
	data := s.baseViewFor(writer, post.Title, post.Excerpt, s.contentPath(post))
	data.Post = s.withTopicLinks(post)
	if post.CoverURL != "" {
		data.Site.SocialImageURL = s.publicResourceURL(post.CoverURL)
	}
	data.BodyClass = "is-single is-page"
	s.render(writer, http.StatusOK, "page", data)
}

func (s *Server) adminContent(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/admin/content/" {
		s.renderError(writer, http.StatusNotFound, "内容管理页面没有找到")
		return
	}
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityContentRead)
	if !ok {
		return
	}
	page, err := positiveQueryInt(request.URL.Query().Get("page"), 1, 100000)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "分页参数不正确")
		return
	}
	pageSize := s.siteSettings.Snapshot().Settings.Reading.AdminPageSize
	query := content.AdminQuery{
		AuthorID: contentAuthorScope(principal),
		Type:     request.URL.Query().Get("type"), Status: request.URL.Query().Get("status"),
		Search: request.URL.Query().Get("q"), Limit: pageSize, Offset: (page - 1) * pageSize,
	}
	query, err = content.NormalizeAdminQuery(query)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "内容筛选条件不正确")
		return
	}
	result, err := s.repository.ListAdmin(request.Context(), query)
	if err != nil {
		slog.Error("list admin content", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "内容列表暂时不可用")
		return
	}
	readOnly = readOnly || !canCreateContent(principal)
	s.renderContentList(writer, request, http.StatusOK, principal, readOnly, query, result, page, contentNoticeFromQuery(request.URL.Query().Get("notice")))
}

func (s *Server) adminContentBulk(writer http.ResponseWriter, request *http.Request) {
	principal, form, ok := s.requireContentWriteLimit(writer, request, adminContentBulkFormMaxBytes, "content_bulk_form_too_large")
	if !ok {
		return
	}
	if strictBulkContentFormError(form) != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_bulk_content_form_shape"})
		return
	}
	returnQuery, returnPage, err := bulkContentReturnQuery(form)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_bulk_content_return"})
		return
	}
	if form.Get("action") == "ai-enrich" {
		s.proContentBatch(writer, request, principal, form["selection"])
		return
	}
	change, err := bulkContentStatusChange(form, principal)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_bulk_content_selection"})
		return
	}

	_, err = s.repository.BulkChangeStatus(request.Context(), change)
	returnURL := contentListNoticeURL(returnQuery.Type, returnQuery.Status, returnQuery.Search, returnPage, bulkContentNotice(change.Status))
	if change.Delete {
		returnURL = contentListNoticeURL(returnQuery.Type, returnQuery.Status, returnQuery.Search, returnPage, "bulk-deleted")
	}
	if errors.Is(err, content.ErrVersionConflict) {
		http.Redirect(writer, request, contentListNoticeURL(returnQuery.Type, returnQuery.Status, returnQuery.Search, returnPage, "bulk-conflict"), http.StatusSeeOther)
		return
	}
	if errors.Is(err, content.ErrNotFound) {
		s.renderError(writer, http.StatusNotFound, "所选内容包含已不存在的项目")
		return
	}
	if errors.Is(err, content.ErrAuthorScope) {
		s.renderError(writer, http.StatusForbidden, "当前账号不能批量修改其他作者的内容")
		return
	}
	if fields := content.FieldErrors(err); len(fields) > 0 {
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{"error": "bulk_content_validation_failed", "fields": fields})
		return
	}
	if err != nil {
		slog.Error("bulk change content status", "count", len(change.Items), "status", change.Status, "actor", principal.Username, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "批量更新内容失败")
		return
	}
	slog.Info("bulk content status changed", "count", len(change.Items), "status", change.Status, "actor", principal.Username)
	http.Redirect(writer, request, returnURL, http.StatusSeeOther)
}

func (s *Server) adminContentNew(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/admin/content/new/" {
		s.renderError(writer, http.StatusNotFound, "内容编辑页面没有找到")
		return
	}
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	postType := request.URL.Query().Get("type")
	if postType == "" {
		postType = content.TypePost
	}
	if !content.ValidType(postType) {
		s.renderError(writer, http.StatusBadRequest, "内容类型不正确")
		return
	}
	input := s.newContentInput(request, principal, postType)
	s.renderContentEditor(writer, request, http.StatusOK, principal, readOnly, content.Post{}, input, nil, adminNotice{}, false, nil)
}

func (s *Server) newContentInput(request *http.Request, principal auth.Principal, postType string) content.PostInput {
	settings := s.siteSnapshot(request.Context()).Settings
	commentStatus := content.CommentOpen
	if !settings.Writing.PostCommentsEnabled {
		commentStatus = content.CommentClosed
	}
	category := settings.Writing.DefaultCategory
	if postType == content.TypePage {
		commentStatus = content.CommentOpen
		if !settings.Writing.PageCommentsEnabled {
			commentStatus = content.CommentClosed
		}
		category = ""
	}
	return content.PostInput{
		Type: postType, Status: settings.Writing.DefaultStatus, CommentStatus: commentStatus,
		Category: category, AuthorID: principal.UserID, AuthorName: principal.Label(), Actor: principal.Username,
	}
}

func (s *Server) adminContentCreate(writer http.ResponseWriter, request *http.Request) {
	principal, form, ok := s.requireContentWrite(writer, request)
	if !ok {
		return
	}
	if strictFormError(form) != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_form_shape"})
		return
	}
	action := form.Get("action")
	if action != "save" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unknown_content_action"})
		return
	}
	if len(request.Header.Values("X-NewPress-Editor")) > 0 {
		s.adminEditorialSubmission(writer, request, principal, form, content.Post{})
		return
	}
	input, fields := s.parseEditorInput(request, form, principal, time.Now(), content.Post{})
	if len(fields) > 0 {
		s.renderContentEditor(writer, request, http.StatusUnprocessableEntity, principal, false, content.Post{}, input, fields, adminNotice{"请检查标记字段后再保存", "error"}, false, nil)
		return
	}
	if !s.recheckContentWrite(writer, request, 0) {
		return
	}
	post, err := s.repository.Create(request.Context(), input)
	if err != nil {
		s.handleContentCreateError(writer, request, principal, input, err)
		return
	}
	http.Redirect(writer, request, "/admin/content/"+strconv.FormatInt(post.ID, 10)+"/edit/?notice=created", http.StatusSeeOther)
}

func (s *Server) adminContentEdit(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityContentRead)
	if !ok {
		return
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "编辑器参数不正确")
		return
	}
	for key, values := range query {
		if (key != "notice" && key != "recovery") || len(values) != 1 {
			s.renderError(writer, http.StatusBadRequest, "编辑器参数不正确")
			return
		}
	}
	if recovery := query.Get("recovery"); recovery != "" && recovery != "autosave" {
		s.renderError(writer, http.StatusBadRequest, "恢复点参数不正确")
		return
	}
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		s.renderError(writer, http.StatusNotFound, "内容没有找到")
		return
	}
	post, err := s.repository.GetByID(request.Context(), id)
	if errors.Is(err, content.ErrNotFound) {
		s.renderError(writer, http.StatusNotFound, "内容没有找到")
		return
	}
	if err != nil {
		slog.Error("load admin content", "id", id, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "内容暂时不可用")
		return
	}
	if !canReadContentObject(principal, post) {
		s.renderError(writer, http.StatusForbidden, "当前账号不能访问其他作者的内容")
		return
	}
	readOnly = readOnly || !canWriteContentObject(principal, post)
	input := inputFromPost(post)
	notice := contentNoticeFromQuery(query.Get("notice"))
	var recovered *content.Autosave
	if query.Get("recovery") == "autosave" {
		if readOnly {
			s.renderError(writer, http.StatusForbidden, "当前账号不能加载这篇内容的恢复点")
			return
		}
		if autosave, exists := s.loadEditorAutosave(request, post, principal); exists {
			input = inputFromPost(autosave.Snapshot)
			recovered = &autosave
			notice = adminNotice{"已把自动保存恢复点载入表单；请核对并正式保存，公开内容和版本号尚未改变。", "success"}
		} else {
			notice = adminNotice{"恢复点已过期、被放弃或已由正式保存清除。", "warning"}
		}
	}
	s.renderContentEditor(writer, request, http.StatusOK, principal, readOnly, post, input, nil, notice, false, recovered)
}

func (s *Server) adminContentUpdate(writer http.ResponseWriter, request *http.Request) {
	principal, form, ok := s.requireContentWrite(writer, request)
	if !ok {
		return
	}
	if strictFormError(form) != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_form_shape"})
		return
	}
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		s.renderError(writer, http.StatusNotFound, "内容没有找到")
		return
	}
	version, err := strconv.ParseInt(form.Get("version"), 10, 64)
	if err != nil || version <= 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_content_version"})
		return
	}
	currentPost, err := s.repository.GetByID(request.Context(), id)
	principal, ok = s.refreshContentWrite(writer, request)
	if !ok {
		return
	}
	if errors.Is(err, content.ErrNotFound) {
		s.renderError(writer, http.StatusNotFound, "内容没有找到")
		return
	}
	if err != nil {
		slog.Error("load content authorization target", "id", id, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "内容暂时不可用")
		return
	}
	if !canWriteContentObject(principal, currentPost) {
		s.renderError(writer, http.StatusForbidden, "当前账号不能修改其他作者的内容")
		return
	}
	if len(request.Header.Values("X-NewPress-Editor")) > 0 {
		s.adminEditorialSubmission(writer, request, principal, form, currentPost)
		return
	}
	mutation := content.Mutation{ExpectedVersion: version, Actor: principal.Username}
	action := form.Get("action")
	var revisionID int64
	if strings.HasPrefix(action, "restore_revision-") {
		revisionID, err = strconv.ParseInt(strings.TrimPrefix(action, "restore_revision-"), 10, 64)
		if err != nil || revisionID <= 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_revision_id"})
			return
		}
		action = "restore_revision"
	}
	var updated content.Post
	mutationAction := action
	if action == "publish" && form.Has("ai_pro_max") {
		mutationAction = "save"
	}
	var input content.PostInput
	if mutationAction == "save" {
		var fields map[string]string
		input, fields = s.parseEditorMutation(request, form, principal, currentPost)
		if len(fields) > 0 {
			current, loadErr := s.repository.GetByID(request.Context(), id)
			if loadErr != nil {
				s.handleContentLoadError(writer, id, loadErr)
				return
			}
			s.renderContentEditor(writer, request, http.StatusUnprocessableEntity, principal, false, current, input, fields, adminNotice{"请检查标记字段后再保存", "error"}, false, nil)
			return
		}
	}
	if !s.recheckContentWrite(writer, request, id) {
		return
	}
	switch mutationAction {
	case "save":
		updated, err = s.repository.Update(request.Context(), id, mutation, input)
	case "trash":
		updated, err = s.repository.ChangeStatus(request.Context(), id, mutation, content.StatusTrash)
	case "restore":
		updated, err = s.repository.ChangeStatus(request.Context(), id, mutation, content.StatusDraft)
	case "publish":
		updated, err = s.repository.ChangeStatus(request.Context(), id, mutation, content.StatusPublish)
	case "restore_revision":
		updated, err = s.repository.RestoreRevision(request.Context(), id, revisionID, mutation)
	default:
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unknown_content_action"})
		return
	}
	if err != nil {
		s.handleContentUpdateError(writer, request, principal, id, action, form, err)
		return
	}
	notice := "saved"
	switch action {
	case "trash":
		notice = "trashed"
	case "restore":
		notice = "restored"
	case "publish":
		notice = "published"
	case "restore_revision":
		notice = "revision-restored"
	}
	http.Redirect(writer, request, "/admin/content/"+strconv.FormatInt(updated.ID, 10)+"/edit/?notice="+notice, http.StatusSeeOther)
}
