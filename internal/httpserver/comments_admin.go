package httpserver

import (
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
)

const (
	adminCommentFormMaxBytes     = 64 << 10
	adminCommentBulkFormMaxBytes = 32 << 10
)

type adminCommentFilter struct {
	Status string
	Search string
	PostID int64
	Page   int
	Limit  int
}

type adminCommentItemView struct {
	Comment           comment.Comment
	PostTitle         string
	PostType          string
	PostStatus        string
	postAuthorID      int64
	PostEditURL       string
	PostPermalink     string
	InspectorURL      string
	SourceFingerprint string
	StatusLabel       string
	StatusTone        string
	CanDelete         bool
}

type adminCommentsView struct {
	Items        []adminCommentItemView
	StatusCounts map[string]int
	Total        int
	Status       string
	Search       string
	PostID       int64
	Page         int
	PerPage      int
	Pages        int
	PreviousURL  string
	NextURL      string
	Notice       string
	NoticeTone   string
	CanModerate  bool
}

type adminCommentPostAPIResponse struct {
	ID        int64  `json:"id"`
	Type      string `json:"type,omitempty"`
	Status    string `json:"status,omitempty"`
	Title     string `json:"title"`
	EditURL   string `json:"edit_url,omitempty"`
	Permalink string `json:"permalink,omitempty"`
}

type adminCommentAPIResponse struct {
	ID                int64                       `json:"id"`
	PostID            int64                       `json:"post_id"`
	ParentID          int64                       `json:"parent_id,omitempty"`
	UserID            int64                       `json:"user_id,omitempty"`
	Version           int64                       `json:"version"`
	AuthorName        string                      `json:"author_name"`
	AuthorEmail       string                      `json:"author_email"`
	Status            string                      `json:"status"`
	ContentSource     string                      `json:"content_source"`
	ContentHTML       string                      `json:"content_html"`
	SourceFingerprint string                      `json:"source_fingerprint,omitempty"`
	StaffReply        bool                        `json:"staff_reply"`
	CreatedBy         string                      `json:"created_by,omitempty"`
	EditedBy          string                      `json:"edited_by,omitempty"`
	EditedAt          *time.Time                  `json:"edited_at,omitempty"`
	ModeratedBy       string                      `json:"moderated_by,omitempty"`
	ModeratedAt       *time.Time                  `json:"moderated_at,omitempty"`
	CreatedAt         time.Time                   `json:"created_at"`
	UpdatedAt         time.Time                   `json:"updated_at"`
	ETag              string                      `json:"etag"`
	Post              adminCommentPostAPIResponse `json:"post"`
}

type adminCommentsPaginationAPIResponse struct {
	Page     int    `json:"page"`
	PerPage  int    `json:"per_page"`
	Total    int    `json:"total"`
	Pages    int    `json:"pages"`
	Previous string `json:"previous,omitempty"`
	Next     string `json:"next,omitempty"`
}

type adminCommentsAPIResponse struct {
	Items        []adminCommentAPIResponse          `json:"items"`
	Pagination   adminCommentsPaginationAPIResponse `json:"pagination"`
	StatusCounts map[string]int                     `json:"status_counts"`
}

type adminCommentUpdateAPIRequest struct {
	Status  string `json:"status"`
	Version int64  `json:"version"`
}

func (s *Server) adminComments(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/admin/comments/" {
		s.renderError(writer, http.StatusNotFound, "评论审核页面没有找到")
		return
	}
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityCommentsModerate)
	if !ok {
		return
	}
	filter, noticeCode, err := s.parseAdminCommentQuery(request.URL.RawQuery, true)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "评论筛选条件不正确")
		return
	}
	s.renderAdminComments(writer, request, http.StatusOK, principal, readOnly, filter, adminCommentNotice(noticeCode))
}

func (s *Server) adminCommentsSubmit(writer http.ResponseWriter, request *http.Request) {
	principal, form, ok := s.requireAdminCommentWriteForm(writer, request, adminCommentFormMaxBytes, "comment_form_too_large")
	if !ok {
		return
	}
	allowed := map[string]bool{
		"csrf_token": true, "id": true, "version": true, "action": true,
		"return_status": true, "return_q": true, "return_post": true, "return_page": true,
	}
	for key, entries := range form {
		if !allowed[key] || len(entries) != 1 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_form_shape"})
			return
		}
	}
	for key := range allowed {
		if len(form[key]) != 1 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_form_shape"})
			return
		}
	}
	id, idErr := positivePathID(form.Get("id"))
	version, versionErr := strconv.ParseInt(form.Get("version"), 10, 64)
	filter, filterErr := s.adminCommentReturnFilter(form)
	if idErr != nil || versionErr != nil || version <= 0 || filterErr != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_comment_mutation"})
		return
	}
	action := strings.ToLower(strings.TrimSpace(form.Get("action")))
	actor := adminCommentActor(principal)
	mutation := comment.Mutation{ExpectedVersion: version, Actor: actor}
	if action == "delete" {
		current, err := s.comments.Get(request.Context(), id)
		if _, ok := s.refreshCommentWrite(writer, request); !ok {
			return
		}
		if err != nil {
			s.handleAdminCommentFormError(writer, request, principal, filter, err)
			return
		}
		if current.Status != comment.StatusTrash {
			s.renderAdminComments(writer, request, http.StatusConflict, principal, false, filter, adminNotice{"只有回收站中的评论可以永久删除", "warning"})
			return
		}
		err = s.comments.Delete(request.Context(), id, mutation)
		if err == nil {
			s.forgetCommentOrigin(id)
		}
		if _, ok := s.refreshCommentWrite(writer, request); !ok {
			return
		}
		if err != nil {
			s.handleAdminCommentFormError(writer, request, principal, filter, err)
			return
		}
		http.Redirect(writer, request, adminCommentListURL(filter, "deleted"), http.StatusSeeOther)
		return
	}
	status, ok := adminCommentActionStatus(action)
	if !ok {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unknown_comment_action"})
		return
	}
	if _, ok := s.refreshCommentWrite(writer, request); !ok {
		return
	}
	_, err := s.comments.ChangeStatus(request.Context(), id, mutation, status)
	if _, ok := s.refreshCommentWrite(writer, request); !ok {
		return
	}
	if err != nil {
		s.handleAdminCommentFormError(writer, request, principal, filter, err)
		return
	}
	http.Redirect(writer, request, adminCommentListURL(filter, "moderated"), http.StatusSeeOther)
}

func (s *Server) adminCommentsBulkSubmit(writer http.ResponseWriter, request *http.Request) {
	principal, form, ok := s.requireAdminCommentWriteForm(writer, request, adminCommentBulkFormMaxBytes, "comment_bulk_form_too_large")
	if !ok {
		return
	}
	if strictBulkCommentFormError(form) != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_bulk_comment_form_shape"})
		return
	}
	filter, err := s.adminCommentReturnFilter(form)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_bulk_comment_return"})
		return
	}
	change, err := bulkCommentStatusChange(form, adminCommentActor(principal))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_bulk_comment_selection"})
		return
	}

	if _, ok := s.refreshCommentWrite(writer, request); !ok {
		return
	}
	_, err = s.comments.BulkChangeStatus(request.Context(), change)
	if _, ok := s.refreshCommentWrite(writer, request); !ok {
		return
	}
	switch {
	case errors.Is(err, comment.ErrVersionConflict):
		http.Redirect(writer, request, adminCommentListURL(filter, "bulk-conflict"), http.StatusSeeOther)
		return
	case errors.Is(err, comment.ErrNotFound):
		s.renderError(writer, http.StatusNotFound, "所选评论包含已不存在的项目")
		return
	case len(comment.FieldErrors(err)) > 0:
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{"error": "bulk_comment_validation_failed", "fields": comment.FieldErrors(err)})
		return
	case err != nil:
		slog.Error("bulk moderate comments", "count", len(change.Items), "status", change.Status, "actor", change.Actor, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.renderError(writer, http.StatusInternalServerError, "批量评论审核失败")
		return
	}
	slog.Info("bulk comments moderated", "count", len(change.Items), "status", change.Status, "actor", change.Actor)
	http.Redirect(writer, request, adminCommentListURL(filter, bulkCommentNotice(change.Status)), http.StatusSeeOther)
}

func (s *Server) requireAdminCommentWriteForm(writer http.ResponseWriter, request *http.Request, maxBytes int64, tooLargeError string) (auth.Principal, url.Values, bool) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityCommentsModerate)
	if !ok {
		return auth.Principal{}, nil, false
	}
	if readOnly || !s.auth.Configured() {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "admin_credentials_not_configured"})
		return auth.Principal{}, nil, false
	}
	if !s.sameOriginPublicWrite(request) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "cross_site_request_rejected"})
		return auth.Principal{}, nil, false
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/x-www-form-urlencoded") || (parameters["charset"] != "" && !strings.EqualFold(parameters["charset"], "utf-8")) {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "form_content_type_required"})
		return auth.Principal{}, nil, false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxBytes)
	if err := request.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]string{"error": tooLargeError})
			return auth.Principal{}, nil, false
		}
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_comment_form"})
		return auth.Principal{}, nil, false
	}
	form := request.PostForm
	principal, ok = s.refreshCommentWrite(writer, request)
	if !ok {
		return auth.Principal{}, nil, false
	}
	return principal, form, true
}

func strictBulkCommentFormError(form url.Values) string {
	requiredSingles := map[string]bool{
		"csrf_token": true, "action": true, "return_status": true,
		"return_q": true, "return_post": true, "return_page": true,
	}
	for key, values := range form {
		if key == "selection" {
			if len(values) < 1 || len(values) > comment.MaxBulkMutations {
				return key
			}
			continue
		}
		if !requiredSingles[key] || len(values) != 1 {
			return key
		}
	}
	for key := range requiredSingles {
		if len(form[key]) != 1 {
			return key
		}
	}
	if len(form["selection"]) < 1 || len(form["selection"]) > comment.MaxBulkMutations {
		return "selection"
	}
	return ""
}

func bulkCommentStatusChange(form url.Values, actor string) (comment.BulkStatusChange, error) {
	action := strings.ToLower(strings.TrimSpace(form.Get("action")))
	status, ok := adminCommentActionStatus(action)
	if !ok || action == "restore" {
		return comment.BulkStatusChange{}, errors.New("unknown bulk comment action")
	}
	change := comment.BulkStatusChange{Status: status, Actor: actor}
	for _, raw := range form["selection"] {
		if len(raw) > 48 {
			return comment.BulkStatusChange{}, errors.New("bulk comment selection is too long")
		}
		rawID, rawVersion, found := strings.Cut(raw, ":")
		if !found || rawID == "" || rawVersion == "" || strings.Contains(rawVersion, ":") {
			return comment.BulkStatusChange{}, errors.New("invalid bulk comment selection pair")
		}
		id, idErr := strconv.ParseInt(rawID, 10, 64)
		version, versionErr := strconv.ParseInt(rawVersion, 10, 64)
		if idErr != nil || versionErr != nil {
			return comment.BulkStatusChange{}, errors.New("invalid bulk comment selection number")
		}
		change.Items = append(change.Items, comment.BulkMutation{ID: id, ExpectedVersion: version})
	}
	normalized, fields := comment.NormalizeBulkStatusChange(change)
	if len(fields) > 0 {
		return comment.BulkStatusChange{}, &comment.ValidationError{Fields: fields}
	}
	return normalized, nil
}

func adminCommentActor(principal auth.Principal) string {
	if actor := strings.TrimSpace(principal.Username); actor != "" {
		return actor
	}
	return strings.TrimSpace(principal.Label())
}

func (s *Server) renderAdminComments(writer http.ResponseWriter, request *http.Request, status int, principal auth.Principal, readOnly bool, filter adminCommentFilter, notice adminNotice) {
	queryStatus := filter.Status
	if queryStatus == "all" {
		queryStatus = ""
	}
	result, err := s.comments.ListAdmin(request.Context(), comment.Query{
		PostID: filter.PostID, Status: queryStatus, Search: filter.Search,
		Limit: filter.Limit, Offset: (filter.Page - 1) * filter.Limit,
	})
	if !s.commentOperationAccess(writer, request, err) {
		return
	}
	if err != nil {
		slog.Error("list admin comments", "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.renderError(writer, http.StatusInternalServerError, "评论审核队列暂时不可用")
		return
	}
	items := s.adminCommentItems(request, result.Items)
	for i := range items {
		items[i].InspectorURL = adminCommentEditorURL(items[i].Comment.ID, filter, "")
	}
	pages := max(1, (result.Total+filter.Limit-1)/filter.Limit)
	view := adminCommentsView{
		Items: items, StatusCounts: result.StatusCounts, Total: result.Total,
		Status: filter.Status, Search: filter.Search, PostID: filter.PostID,
		Page: filter.Page, PerPage: filter.Limit, Pages: pages,
		Notice: notice.Message, NoticeTone: notice.Tone,
		CanModerate: !readOnly && principal.Can(auth.CapabilityCommentsModerate),
	}
	if filter.Page > 1 {
		previous := filter
		previous.Page--
		view.PreviousURL = adminCommentListURL(previous, "")
	}
	if (filter.Page-1)*filter.Limit+len(result.Items) < result.Total {
		next := filter
		next.Page++
		view.NextURL = adminCommentListURL(next, "")
	}
	data := s.baseViewFor(writer, "评论审核", "审核公开讨论、识别垃圾内容并维护回复规则", "/admin/comments/")
	data.BodyClass = "is-admin is-admin-comments"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("comments", principal, readOnly)
	data.CommentAdmin = view
	access := func() error { return s.commentAccess(request) }
	s.renderChecked(writer, status, "admin-comments", data, request, func() bool { return s.recheckCommentAccess(writer, request) }, access)
}

func (s *Server) parseAdminCommentQuery(raw string, allowNotice bool) (adminCommentFilter, string, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return adminCommentFilter{}, "", err
	}
	allowed := map[string]bool{"status": true, "q": true, "post": true, "page": true}
	if allowNotice {
		allowed["notice"] = true
	}
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 {
			return adminCommentFilter{}, "", comment.ErrInvalidInput
		}
	}
	status := strings.ToLower(strings.TrimSpace(values.Get("status")))
	if status == "" {
		status = comment.StatusPending
	}
	if status != "all" && !comment.ValidStatus(status) {
		return adminCommentFilter{}, "", comment.ErrInvalidInput
	}
	page, err := positiveQueryInt(values.Get("page"), 1, 1_000_000)
	if err != nil {
		return adminCommentFilter{}, "", err
	}
	postID, err := optionalPositiveID(values.Get("post"))
	if err != nil {
		return adminCommentFilter{}, "", err
	}
	limit := s.siteSettings.Snapshot().Settings.Reading.AdminPageSize
	queryStatus := status
	if queryStatus == "all" {
		queryStatus = ""
	}
	normalized, err := comment.NormalizeQuery(comment.Query{PostID: postID, Status: queryStatus, Search: values.Get("q"), Limit: limit})
	if err != nil {
		return adminCommentFilter{}, "", err
	}
	return adminCommentFilter{Status: status, Search: normalized.Search, PostID: postID, Page: page, Limit: limit}, values.Get("notice"), nil
}

func (s *Server) adminCommentReturnFilter(form url.Values) (adminCommentFilter, error) {
	query := url.Values{
		"status": {form.Get("return_status")}, "q": {form.Get("return_q")},
		"post": {form.Get("return_post")}, "page": {form.Get("return_page")},
	}
	filter, _, err := s.parseAdminCommentQuery(query.Encode(), false)
	return filter, err
}

func (s *Server) handleAdminCommentFormError(writer http.ResponseWriter, request *http.Request, principal auth.Principal, filter adminCommentFilter, err error) {
	if !s.commentOperationAccess(writer, request, err) {
		return
	}
	switch {
	case errors.Is(err, comment.ErrVersionConflict):
		s.renderAdminComments(writer, request, http.StatusConflict, principal, false, filter, adminNotice{"评论已被其他审核者更新，队列已刷新", "warning"})
	case errors.Is(err, comment.ErrNotFound):
		s.renderError(writer, http.StatusNotFound, "评论没有找到")
	default:
		slog.Error("mutate admin comment", "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.renderError(writer, http.StatusInternalServerError, "评论审核操作失败")
	}
}

func (s *Server) adminCommentsAPI(writer http.ResponseWriter, request *http.Request) {
	_, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityCommentsModerate)
	if !ok {
		return
	}
	values, ok := s.strictAPIQuery(writer, request, map[string]bool{
		"status": true, "q": true, "post_id": true, "page": true, "per_page": true,
	})
	if !ok {
		return
	}
	status := strings.ToLower(strings.TrimSpace(values.Get("status")))
	if status == "" {
		status = comment.StatusPending
	}
	queryStatus := status
	if status == "all" {
		queryStatus = ""
	} else if !comment.ValidStatus(status) {
		s.writeAPIError(writer, request, http.StatusBadRequest, "comments.invalid_filter", "评论状态筛选不正确", map[string]any{"field": "status"})
		return
	}
	postID, err := optionalPositiveID(values.Get("post_id"))
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "post_id 必须是正整数", map[string]any{"field": "post_id"})
		return
	}
	page, err := positiveQueryInt(values.Get("page"), 1, 1_000_000)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "page 必须是有效的正整数", map[string]any{"field": "page"})
		return
	}
	defaultLimit := s.siteSettings.Snapshot().Settings.Reading.AdminPageSize
	perPage, err := positiveQueryInt(values.Get("per_page"), defaultLimit, comment.MaxListLimit)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "per_page 必须介于 1 和 200 之间", map[string]any{"field": "per_page"})
		return
	}
	normalized, err := comment.NormalizeQuery(comment.Query{
		PostID: postID, Status: queryStatus, Search: values.Get("q"), Limit: perPage, Offset: (page - 1) * perPage,
	})
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "comments.invalid_filter", "评论筛选条件不正确", nil)
		return
	}
	result, err := s.comments.ListAdmin(request.Context(), normalized)
	if !s.commentOperationAccess(writer, request, err) {
		return
	}
	if err != nil {
		slog.Error("list admin comments API", "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "comments.unavailable", "评论审核队列暂时不可用", nil)
		return
	}
	items := make([]adminCommentAPIResponse, 0, len(result.Items))
	for _, view := range s.adminCommentItems(request, result.Items) {
		items = append(items, adminCommentResponse(view))
	}
	pages := max(1, (result.Total+perPage-1)/perPage)
	pagination := adminCommentsPaginationAPIResponse{Page: page, PerPage: perPage, Total: result.Total, Pages: pages}
	if page > 1 {
		pagination.Previous = adminCommentsAPIPageURL(status, normalized.Search, postID, page-1, perPage)
	}
	if normalized.Offset+len(result.Items) < result.Total {
		pagination.Next = adminCommentsAPIPageURL(status, normalized.Search, postID, page+1, perPage)
	}
	s.writeCommentJSON(writer, request, http.StatusOK, adminCommentsAPIResponse{Items: items, Pagination: pagination, StatusCounts: result.StatusCounts}, nil)
}

func (s *Server) adminCommentUpdateAPI(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityCommentsModerate)
	if !ok {
		return
	}
	if !s.sameOriginPublicWrite(request) {
		s.writeAPIError(writer, request, http.StatusForbidden, "auth.cross_site_request_rejected", "写请求必须携带可验证的同源 Origin 或 Referer", nil)
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		s.writeAPIError(writer, request, http.StatusNotFound, "comments.not_found", "评论没有找到", nil)
		return
	}
	var mutation adminCommentUpdateAPIRequest
	if !s.decodeStrictCommentJSON(writer, request, &mutation) {
		return
	}
	principal, ok = s.refreshCommentWrite(writer, request)
	if !ok {
		return
	}
	mutation.Status = strings.ToLower(strings.TrimSpace(mutation.Status))
	if mutation.Version <= 0 || !comment.ValidStatus(mutation.Status) {
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "comments.validation_failed", "审核状态或版本不正确", map[string]any{"fields": map[string]string{"status": "必须使用 pending、approved、spam 或 trash", "version": "必须提供当前正整数版本"}})
		return
	}
	updated, err := s.comments.ChangeStatus(request.Context(), id, comment.Mutation{ExpectedVersion: mutation.Version, Actor: principal.Username}, mutation.Status)
	if _, ok := s.refreshCommentWrite(writer, request); !ok {
		return
	}
	if err != nil {
		s.handleAdminCommentAPIError(writer, request, id, err)
		return
	}
	s.writeCommentJSON(writer, request, http.StatusOK, adminCommentResponse(s.adminCommentItem(request, updated)), func() { writer.Header().Set("ETag", commentETag(updated)) })
}

func (s *Server) adminCommentDeleteAPI(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityCommentsModerate)
	if !ok {
		return
	}
	if !s.sameOriginPublicWrite(request) {
		s.writeAPIError(writer, request, http.StatusForbidden, "auth.cross_site_request_rejected", "写请求必须携带可验证的同源 Origin 或 Referer", nil)
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok || !s.requireEmptyAPIBody(writer, request) {
		return
	}
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		s.writeAPIError(writer, request, http.StatusNotFound, "comments.not_found", "评论没有找到", nil)
		return
	}
	current, err := s.comments.Get(request.Context(), id)
	principal, ok = s.refreshCommentWrite(writer, request)
	if !ok {
		return
	}
	if err != nil {
		s.handleAdminCommentAPIError(writer, request, id, err)
		return
	}
	if current.Status != comment.StatusTrash {
		s.writeCommentVersionError(writer, request, http.StatusConflict, "comments.trash_required", "只有回收站中的评论可以永久删除", map[string]any{"current_status": current.Status}, nil)
		return
	}
	if !s.requireAdminCommentPrecondition(writer, request, current, "永久删除必须携带当前 If-Match ETag") {
		return
	}
	err = s.comments.Delete(request.Context(), id, comment.Mutation{ExpectedVersion: current.Version, Actor: principal.Username})
	if err == nil {
		s.forgetCommentOrigin(id)
	}
	if _, ok := s.refreshCommentWrite(writer, request); !ok {
		return
	}
	if err != nil {
		s.handleAdminCommentAPIError(writer, request, id, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) requireAdminCommentPrecondition(writer http.ResponseWriter, request *http.Request, current comment.Comment, message string) bool {
	if !s.recheckCommentAccess(writer, request) {
		return false
	}
	ifMatchValues := request.Header.Values("If-Match")
	if len(ifMatchValues) != 1 || strings.TrimSpace(ifMatchValues[0]) == "" {
		s.writeCommentVersionError(writer, request, http.StatusPreconditionRequired, "comments.precondition_required", message, map[string]any{
			"current_etag": commentETag(current), "current_version": current.Version,
		}, nil)
		return false
	}
	if strings.TrimSpace(ifMatchValues[0]) != commentETag(current) {
		s.writeAdminCommentConflict(writer, request, current)
		return false
	}
	return true
}

func (s *Server) adminCommentsAPIMethodNotAllowed(allow string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if _, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityCommentsModerate); !ok {
			return
		}
		writer.Header().Set("Allow", allow)
		s.writeAPIError(writer, request, http.StatusMethodNotAllowed, "request.method_not_allowed", "此资源不支持当前 HTTP 方法", map[string]any{"allow": allow})
	}
}

func (s *Server) handleAdminCommentAPIError(writer http.ResponseWriter, request *http.Request, id int64, err error) {
	if !s.commentOperationAccess(writer, request, err) {
		return
	}
	switch {
	case errors.Is(err, comment.ErrNotFound):
		s.writeAPIError(writer, request, http.StatusNotFound, "comments.not_found", "评论没有找到", nil)
	case errors.Is(err, comment.ErrRevisionNotFound):
		s.writeAPIError(writer, request, http.StatusNotFound, "comments.revision_not_found", "评论修订记录没有找到", nil)
	case errors.Is(err, comment.ErrVersionConflict):
		current, loadErr := s.comments.Get(request.Context(), id)
		if !s.commentOperationAccess(writer, request, loadErr) {
			return
		}
		if loadErr == nil {
			s.writeAdminCommentConflict(writer, request, current)
			return
		}
		if errors.Is(loadErr, comment.ErrNotFound) {
			s.writeAPIError(writer, request, http.StatusNotFound, "comments.not_found", "评论没有找到", nil)
			return
		}
		slog.Error("load current comment after conflict", "id", id, "error", loadErr, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "comments.unavailable", "无法读取当前评论版本", nil)
	default:
		slog.Error("mutate admin comment API", "id", id, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "comments.update_failed", "评论审核操作失败", nil)
	}
}

func (s *Server) writeAdminCommentConflict(writer http.ResponseWriter, request *http.Request, current comment.Comment) {
	s.writeCommentVersionError(writer, request, http.StatusConflict, "comments.version_conflict", "评论已被其他审核者更新，请重新读取后再操作", map[string]any{
		"current_version": current.Version, "current_status": current.Status, "current_etag": commentETag(current),
	}, func() { writer.Header().Set("ETag", commentETag(current)) })
}

func adminCommentResponse(view adminCommentItemView) adminCommentAPIResponse {
	item := view.Comment
	var editedAt, moderatedAt *time.Time
	if !item.EditedAt.IsZero() {
		value := item.EditedAt
		editedAt = &value
	}
	if !item.ModeratedAt.IsZero() {
		value := item.ModeratedAt
		moderatedAt = &value
	}
	return adminCommentAPIResponse{
		ID: item.ID, PostID: item.PostID, ParentID: item.ParentID, UserID: item.UserID, Version: item.Version,
		AuthorName: item.AuthorName, AuthorEmail: item.AuthorEmail, Status: item.Status,
		ContentSource: item.ContentSource, ContentHTML: item.ContentHTML, SourceFingerprint: view.SourceFingerprint,
		StaffReply: item.StaffReply, CreatedBy: item.CreatedBy, EditedBy: item.EditedBy, EditedAt: editedAt,
		ModeratedBy: item.ModeratedBy, ModeratedAt: moderatedAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		ETag: commentETag(item),
		Post: adminCommentPostAPIResponse{
			ID: item.PostID, Type: view.PostType, Status: view.PostStatus, Title: view.PostTitle,
			EditURL: view.PostEditURL, Permalink: view.PostPermalink,
		},
	}
}

func adminCommentActionStatus(action string) (string, bool) {
	switch action {
	case comment.StatusApproved:
		return comment.StatusApproved, true
	case comment.StatusPending, "restore":
		return comment.StatusPending, true
	case comment.StatusSpam:
		return comment.StatusSpam, true
	case comment.StatusTrash:
		return comment.StatusTrash, true
	default:
		return "", false
	}
}

func adminCommentStatusPresentation(status string) (string, string) {
	switch status {
	case comment.StatusApproved:
		return "已批准", "live"
	case comment.StatusPending:
		return "待审核", "pending"
	case comment.StatusSpam:
		return "垃圾评论", "warning"
	case comment.StatusTrash:
		return "回收站", "muted"
	default:
		return status, "muted"
	}
}

func adminCommentNotice(code string) adminNotice {
	switch code {
	case "moderated":
		return adminNotice{"评论状态已更新并记录审核者", "success"}
	case "deleted":
		return adminNotice{"评论及其回复已永久删除", "success"}
	case "bulk-approved":
		return adminNotice{"所选评论已在同一次原子操作中批准并公开，且分别记录审核者", "success"}
	case "bulk-pending":
		return adminNotice{"所选评论已原子移回待审核队列，任一版本冲突都会阻止整批写入", "success"}
	case "bulk-spam":
		return adminNotice{"所选评论已原子隔离为垃圾内容，公开线程已同步收窄", "success"}
	case "bulk-trash":
		return adminNotice{"所选评论已原子移入回收站；永久删除仍需逐条明确确认", "success"}
	case "bulk-conflict":
		return adminNotice{"批量审核未写入：至少一条评论已在其他会话更新，请刷新队列后重新选择", "warning"}
	default:
		return adminNotice{}
	}
}

func bulkCommentNotice(status string) string {
	switch status {
	case comment.StatusApproved:
		return "bulk-approved"
	case comment.StatusPending:
		return "bulk-pending"
	case comment.StatusSpam:
		return "bulk-spam"
	case comment.StatusTrash:
		return "bulk-trash"
	default:
		return ""
	}
}

func adminCommentListURL(filter adminCommentFilter, notice string) string {
	query := url.Values{}
	if filter.Status != "" && filter.Status != comment.StatusPending {
		query.Set("status", filter.Status)
	}
	if filter.Search != "" {
		query.Set("q", filter.Search)
	}
	if filter.PostID > 0 {
		query.Set("post", strconv.FormatInt(filter.PostID, 10))
	}
	if filter.Page > 1 {
		query.Set("page", strconv.Itoa(filter.Page))
	}
	if notice != "" {
		query.Set("notice", notice)
	}
	if encoded := query.Encode(); encoded != "" {
		return "/admin/comments/?" + encoded
	}
	return "/admin/comments/"
}

func adminCommentsAPIPageURL(status, search string, postID int64, page, perPage int) string {
	query := url.Values{"status": {status}, "page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(perPage)}}
	if search != "" {
		query.Set("q", search)
	}
	if postID > 0 {
		query.Set("post_id", strconv.FormatInt(postID, 10))
	}
	return "/api/v1/admin/comments?" + query.Encode()
}

func commentETag(item comment.Comment) string {
	return fmt.Sprintf(`"comment-%d-v%d"`, item.ID, item.Version)
}

func shortCommentFingerprint(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
