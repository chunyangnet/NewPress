package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

const adminCommentEditorFormMaxBytes = 32 << 10

type adminCommentEditForm struct {
	AuthorName    string
	AuthorEmail   string
	ContentSource string
}

type adminCommentRevisionView struct {
	Revision       comment.Revision
	ChangeLabels   []string
	ChangeCount    int
	StatusLabel    string
	RestoreAction  string
	MatchesCurrent bool
}

type adminCommentEditorView struct {
	Item              adminCommentItemView
	Parent            *adminCommentItemView
	Revisions         []adminCommentRevisionView
	EditForm          adminCommentEditForm
	EditErrors        map[string]string
	ReplyContent      string
	ReplyErrors       map[string]string
	BackURL           string
	EditorURL         string
	EditAction        string
	ReplyAction       string
	APIURL            string
	Depth             int
	CanEdit           bool
	CanReply          bool
	CanDraftReply     bool
	ReplyStatus       string
	ReplyStatusLabel  string
	ReplyIdentity     string
	ReplyEmail        string
	MaxReplyLength    int
	Notice            string
	NoticeTone        string
	NoticeTitle       string
	RevisionTruncated bool
}

type adminCommentEditAPIRequest struct {
	Version       int64  `json:"version"`
	AuthorName    string `json:"author_name"`
	AuthorEmail   string `json:"author_email"`
	ContentSource string `json:"content_source"`
}

type adminCommentReplyAPIRequest struct {
	ParentVersion int64  `json:"parent_version"`
	ContentSource string `json:"content_source"`
}

type adminCommentRevisionAPIResponse struct {
	CommentID     int64     `json:"comment_id"`
	Version       int64     `json:"version"`
	AuthorName    string    `json:"author_name"`
	AuthorEmail   string    `json:"author_email"`
	Status        string    `json:"status"`
	ContentSource string    `json:"content_source"`
	ContentHTML   string    `json:"content_html"`
	Actor         string    `json:"actor"`
	CreatedAt     time.Time `json:"created_at"`
}

type adminCommentRevisionsAPIResponse struct {
	Items []adminCommentRevisionAPIResponse `json:"items"`
}

func (s *Server) adminCommentEditor(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityCommentsModerate)
	if !ok {
		return
	}
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		s.renderError(writer, http.StatusNotFound, "评论没有找到")
		return
	}
	filter, noticeCode, err := s.parseAdminCommentQuery(request.URL.RawQuery, true)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "评论返回筛选条件不正确")
		return
	}
	s.renderAdminCommentEditor(writer, request, http.StatusOK, principal, readOnly, id, filter, nil, nil, "", nil, adminCommentEditorNotice(noticeCode))
}

func (s *Server) adminCommentEditSubmit(writer http.ResponseWriter, request *http.Request) {
	principal, form, ok := s.requireAdminCommentWriteForm(writer, request, adminCommentEditorFormMaxBytes, "comment_edit_form_too_large")
	if !ok {
		return
	}
	if strictSingleValueForm(form, []string{
		"csrf_token", "version", "author_name", "author_email", "content_source",
		"return_status", "return_q", "return_post", "return_page",
	}) != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_comment_edit_form_shape"})
		return
	}
	id, idErr := positivePathID(request.PathValue("id"))
	version, versionErr := strconv.ParseInt(form.Get("version"), 10, 64)
	filter, filterErr := s.adminCommentReturnFilter(form)
	if idErr != nil || versionErr != nil || version <= 0 || filterErr != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_comment_edit_mutation"})
		return
	}
	input, fields := comment.NormalizeEditInput(comment.EditInput{
		AuthorName: form.Get("author_name"), AuthorEmail: form.Get("author_email"), ContentSource: form.Get("content_source"),
	})
	if len(fields) > 0 {
		s.renderAdminCommentEditor(writer, request, http.StatusUnprocessableEntity, principal, false, id, filter,
			&input, fields, "", nil, adminNotice{"评论编辑字段没有通过服务端校验", "error"})
		return
	}
	principal, ok = s.refreshCommentWrite(writer, request)
	if !ok {
		return
	}
	updated, err := s.comments.Edit(request.Context(), id, comment.Mutation{ExpectedVersion: version, Actor: adminCommentActor(principal)}, input)
	if _, ok := s.refreshCommentWrite(writer, request); !ok {
		return
	}
	switch {
	case errors.Is(err, comment.ErrVersionConflict):
		s.renderAdminCommentEditor(writer, request, http.StatusConflict, principal, false, id, filter, nil, nil, "", nil,
			adminNotice{"评论已被其他审核者更新；当前版本已经重新载入，未覆盖任何内容", "warning"})
		return
	case errors.Is(err, comment.ErrNotFound):
		s.renderError(writer, http.StatusNotFound, "评论没有找到")
		return
	case len(comment.FieldErrors(err)) > 0:
		s.renderAdminCommentEditor(writer, request, http.StatusUnprocessableEntity, principal, false, id, filter,
			&input, comment.FieldErrors(err), "", nil, adminNotice{"评论编辑字段没有通过仓库校验", "error"})
		return
	case err != nil:
		slog.Error("edit admin comment", "id", id, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.renderError(writer, http.StatusInternalServerError, "评论编辑失败")
		return
	}
	notice := "edited"
	if updated.Version == version {
		notice = "unchanged"
	}
	http.Redirect(writer, request, adminCommentEditorURL(id, filter, notice), http.StatusSeeOther)
}

func (s *Server) adminCommentReplySubmit(writer http.ResponseWriter, request *http.Request) {
	principal, form, ok := s.requireAdminCommentWriteForm(writer, request, adminCommentEditorFormMaxBytes, "comment_reply_form_too_large")
	if !ok {
		return
	}
	if strictSingleValueForm(form, []string{
		"csrf_token", "parent_version", "content_source", "return_status", "return_q", "return_post", "return_page",
	}) != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_comment_reply_form_shape"})
		return
	}
	id, idErr := positivePathID(request.PathValue("id"))
	version, versionErr := strconv.ParseInt(form.Get("parent_version"), 10, 64)
	filter, filterErr := s.adminCommentReturnFilter(form)
	if idErr != nil || versionErr != nil || version <= 0 || filterErr != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_comment_reply_mutation"})
		return
	}
	contentSource := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(form.Get("content_source"), "\r\n", "\n"), "\r", "\n"))
	maxLength := min(comment.MaxContentRunes, s.siteSettings.Snapshot().Settings.Discussion.MaxLength)
	if utf8.RuneCountInString(contentSource) > maxLength {
		s.renderAdminCommentEditor(writer, request, http.StatusUnprocessableEntity, principal, false, id, filter, nil, nil,
			contentSource, map[string]string{"content_source": "站方回复不能超过 " + strconv.Itoa(maxLength) + " 个字符"},
			adminNotice{"站方回复超过当前讨论策略上限", "error"})
		return
	}
	principal, ok = s.refreshCommentWrite(writer, request)
	if !ok {
		return
	}
	input := comment.ReplyInput{
		ParentID: id, ExpectedParentVersion: version, UserID: principal.UserID,
		AuthorName: principal.Label(), AuthorEmail: adminCommentReplyEmail(principal), ContentSource: contentSource,
		Actor: adminCommentActor(principal),
	}
	created, err := s.comments.Reply(request.Context(), input)
	if err == nil {
		s.captureCommentOrigin(request, created.ID)
	}
	if _, ok := s.refreshCommentWrite(writer, request); !ok {
		return
	}
	switch {
	case errors.Is(err, comment.ErrVersionConflict):
		s.renderAdminCommentEditor(writer, request, http.StatusConflict, principal, false, id, filter, nil, nil,
			contentSource, nil, adminNotice{"回复目标已更新或改变审核状态；回复未写入，请核对当前版本后重试", "warning"})
		return
	case errors.Is(err, comment.ErrInvalidParent):
		s.renderAdminCommentEditor(writer, request, http.StatusConflict, principal, false, id, filter, nil, nil,
			contentSource, map[string]string{"content_source": "当前状态、文章归属或线程深度不允许继续回复"},
			adminNotice{"站方回复未写入：目标已隔离、进入回收站或达到五层上限", "warning"})
		return
	case errors.Is(err, comment.ErrNotFound):
		s.renderError(writer, http.StatusNotFound, "回复目标没有找到")
		return
	case len(comment.FieldErrors(err)) > 0:
		s.renderAdminCommentEditor(writer, request, http.StatusUnprocessableEntity, principal, false, id, filter, nil, nil,
			contentSource, commentReplyFieldErrors(comment.FieldErrors(err)), adminNotice{"站方回复没有通过服务端校验", "error"})
		return
	case err != nil:
		slog.Error("create staff comment reply", "parent_id", id, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.renderError(writer, http.StatusInternalServerError, "站方回复提交失败")
		return
	}
	http.Redirect(writer, request, adminCommentEditorURL(created.ID, filter, "replied"), http.StatusSeeOther)
}

func (s *Server) adminCommentRevisionRestoreSubmit(writer http.ResponseWriter, request *http.Request) {
	principal, form, ok := s.requireAdminCommentWriteForm(writer, request, adminCommentEditorFormMaxBytes, "comment_revision_restore_form_too_large")
	if !ok {
		return
	}
	if strictSingleValueForm(form, []string{
		"csrf_token", "version", "return_status", "return_q", "return_post", "return_page",
	}) != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_comment_revision_restore_form_shape"})
		return
	}
	id, idErr := positivePathID(request.PathValue("id"))
	sourceVersion, sourceErr := positivePathID(request.PathValue("revision_version"))
	version, versionErr := strconv.ParseInt(form.Get("version"), 10, 64)
	filter, filterErr := s.adminCommentReturnFilter(form)
	if idErr != nil || sourceErr != nil || versionErr != nil || version <= 0 || filterErr != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_comment_revision_restore_mutation"})
		return
	}
	principal, ok = s.refreshCommentWrite(writer, request)
	if !ok {
		return
	}
	updated, err := s.comments.RestoreRevision(request.Context(), id, sourceVersion, comment.Mutation{
		ExpectedVersion: version, Actor: adminCommentActor(principal),
	})
	if _, ok := s.refreshCommentWrite(writer, request); !ok {
		return
	}
	switch {
	case errors.Is(err, comment.ErrVersionConflict):
		s.renderAdminCommentEditor(writer, request, http.StatusConflict, principal, false, id, filter, nil, nil, "", nil,
			adminNotice{"评论已被其他审核者更新；修订恢复未写入，当前版本已经重新载入", "warning"})
		return
	case errors.Is(err, comment.ErrRevisionNotFound):
		s.renderError(writer, http.StatusNotFound, "评论修订记录没有找到")
		return
	case errors.Is(err, comment.ErrNotFound):
		s.renderError(writer, http.StatusNotFound, "评论没有找到")
		return
	case len(comment.FieldErrors(err)) > 0:
		s.renderAdminCommentEditor(writer, request, http.StatusUnprocessableEntity, principal, false, id, filter, nil, nil, "", nil,
			adminNotice{"修订快照没有通过仓库校验，未改变当前评论", "error"})
		return
	case err != nil:
		slog.Error("restore admin comment revision", "id", id, "source_version", sourceVersion, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.renderError(writer, http.StatusInternalServerError, "评论修订恢复失败")
		return
	}
	notice := "restored"
	if updated.Version == version {
		notice = "unchanged"
	}
	http.Redirect(writer, request, adminCommentEditorURL(id, filter, notice)+"#comment-revision-ledger", http.StatusSeeOther)
}

func (s *Server) renderAdminCommentEditor(writer http.ResponseWriter, request *http.Request, status int, principal auth.Principal, readOnly bool,
	id int64, filter adminCommentFilter, editInput *comment.EditInput, editErrors map[string]string, replyContent string,
	replyErrors map[string]string, notice adminNotice,
) {
	item, err := s.comments.Get(request.Context(), id)
	if !s.commentOperationAccess(writer, request, err) {
		return
	}
	if errors.Is(err, comment.ErrNotFound) {
		s.renderError(writer, http.StatusNotFound, "评论没有找到")
		return
	}
	if err != nil {
		slog.Error("load admin comment editor", "id", id, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.renderError(writer, http.StatusInternalServerError, "评论检查台暂时不可用")
		return
	}
	revisions, err := s.comments.ListRevisions(request.Context(), id, comment.MaxRevisionLimit)
	if !s.commentOperationAccess(writer, request, err) {
		return
	}
	if err != nil {
		slog.Error("load admin comment revisions", "id", id, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.renderError(writer, http.StatusInternalServerError, "评论修订记录暂时不可用")
		return
	}
	items := []comment.Comment{item}
	if item.ParentID > 0 {
		if parent, loadErr := s.comments.Get(request.Context(), item.ParentID); loadErr == nil {
			items = append(items, parent)
		}
	}
	views := s.adminCommentItems(request, items)
	itemView := views[0]
	var parentView *adminCommentItemView
	if len(views) > 1 {
		parentView = &views[1]
	}
	depth := s.adminCommentDepth(request, item)
	canReply := !readOnly && principal.Can(auth.CapabilityCommentsModerate) && depth < comment.MaxThreadDepth &&
		(item.Status == comment.StatusApproved || item.Status == comment.StatusPending)
	if editInput == nil {
		editInput = &comment.EditInput{AuthorName: item.AuthorName, AuthorEmail: item.AuthorEmail, ContentSource: item.ContentSource}
	}
	revisionViews := adminCommentRevisionViews(item, revisions)
	replyStatus := item.Status
	replyStatusLabel, _ := adminCommentStatusPresentation(replyStatus)
	title := "检查台状态"
	if notice.Tone == "success" {
		title = "评论记录已同步"
	} else if notice.Tone == "warning" || notice.Tone == "error" {
		title = "保存操作未完成"
	}
	view := adminCommentEditorView{
		Item: itemView, Parent: parentView, Revisions: revisionViews,
		EditForm:   adminCommentEditForm{AuthorName: editInput.AuthorName, AuthorEmail: editInput.AuthorEmail, ContentSource: editInput.ContentSource},
		EditErrors: editErrors, ReplyContent: replyContent, ReplyErrors: replyErrors,
		BackURL:     adminCommentListURL(filter, "") + "#comment-" + strconv.FormatInt(id, 10),
		EditorURL:   adminCommentEditorURL(id, filter, ""),
		EditAction:  "/admin/comments/" + strconv.FormatInt(id, 10) + "/edit/",
		ReplyAction: "/admin/comments/" + strconv.FormatInt(id, 10) + "/reply/",
		APIURL:      "/api/v1/admin/comments/" + strconv.FormatInt(id, 10),
		Depth:       depth, CanEdit: !readOnly && principal.Can(auth.CapabilityCommentsModerate), CanReply: canReply,
		ReplyStatus: replyStatus, ReplyStatusLabel: replyStatusLabel, ReplyIdentity: principal.Label(), ReplyEmail: adminCommentReplyEmail(principal),
		MaxReplyLength: min(comment.MaxContentRunes, s.siteSnapshot(request.Context()).Settings.Discussion.MaxLength),
		Notice:         notice.Message, NoticeTone: notice.Tone, NoticeTitle: title,
		RevisionTruncated: len(revisions) == comment.MaxRevisionLimit,
	}
	if canReply && itemView.PostEditURL != "" && (itemView.PostPermalink != "" || canWriteContentObject(principal, content.Post{AuthorID: itemView.postAuthorID})) {
		_, err := s.aiProMaxLease()
		view.CanDraftReply = err == nil
	}
	data := s.baseViewFor(writer, "评论检查台", "安全修订评论、核对版本链并以当前身份回复", "/admin/comments/"+strconv.FormatInt(id, 10)+"/edit/")
	data.BodyClass = "is-admin is-admin-comment-editor"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("comments", principal, readOnly)
	data.CommentAdmin = adminCommentsView{Status: filter.Status, Search: filter.Search, PostID: filter.PostID, Page: filter.Page}
	data.CommentEditor = view
	access := func() error { return s.commentAccess(request) }
	s.renderChecked(writer, status, "admin-comment-editor", data, request, func() bool { return s.recheckCommentAccess(writer, request) }, access)
}

func (s *Server) adminCommentDepth(request *http.Request, item comment.Comment) int {
	depth := 1
	seen := map[int64]struct{}{item.ID: {}}
	currentID := item.ParentID
	for currentID > 0 && depth < comment.MaxThreadDepth {
		if _, exists := seen[currentID]; exists {
			return comment.MaxThreadDepth
		}
		seen[currentID] = struct{}{}
		parent, err := s.comments.Get(request.Context(), currentID)
		if err != nil || parent.PostID != item.PostID {
			return comment.MaxThreadDepth
		}
		depth++
		currentID = parent.ParentID
	}
	return depth
}

func adminCommentRevisionViews(current comment.Comment, revisions []comment.Revision) []adminCommentRevisionView {
	views := make([]adminCommentRevisionView, 0, len(revisions))
	next := comment.EditInput{AuthorName: current.AuthorName, AuthorEmail: current.AuthorEmail, ContentSource: current.ContentSource}
	for _, revision := range revisions {
		labels := make([]string, 0, 3)
		if revision.AuthorName != next.AuthorName {
			labels = append(labels, "称呼")
		}
		if revision.AuthorEmail != next.AuthorEmail {
			labels = append(labels, "邮箱")
		}
		if revision.ContentSource != next.ContentSource {
			labels = append(labels, "正文")
		}
		statusLabel, _ := adminCommentStatusPresentation(revision.Status)
		views = append(views, adminCommentRevisionView{
			Revision: revision, ChangeLabels: labels, ChangeCount: len(labels), StatusLabel: statusLabel,
			RestoreAction:  "/admin/comments/" + strconv.FormatInt(current.ID, 10) + "/revisions/" + strconv.FormatInt(revision.Version, 10) + "/restore/",
			MatchesCurrent: revision.AuthorName == current.AuthorName && revision.AuthorEmail == current.AuthorEmail && revision.ContentSource == current.ContentSource,
		})
		next = comment.EditInput{AuthorName: revision.AuthorName, AuthorEmail: revision.AuthorEmail, ContentSource: revision.ContentSource}
	}
	return views
}

func adminCommentReplyEmail(principal auth.Principal) string {
	if email := strings.TrimSpace(principal.Email); email != "" {
		return email
	}
	return "admin@localhost.invalid"
}

func commentReplyFieldErrors(fields map[string]string) map[string]string {
	result := make(map[string]string)
	if message := fields["content_source"]; message != "" {
		result["content_source"] = message
	}
	if message := fields["parent_id"]; message != "" {
		result["content_source"] = message
	}
	if message := fields["parent_version"]; message != "" {
		result["content_source"] = message
	}
	return result
}

func strictSingleValueForm(form url.Values, required []string) string {
	allowed := make(map[string]struct{}, len(required))
	for _, key := range required {
		allowed[key] = struct{}{}
	}
	for key, values := range form {
		if _, ok := allowed[key]; !ok || len(values) != 1 {
			return key
		}
	}
	for _, key := range required {
		if len(form[key]) != 1 {
			return key
		}
	}
	return ""
}

func adminCommentEditorNotice(code string) adminNotice {
	switch code {
	case "edited":
		return adminNotice{"评论称呼、邮箱与纯文本正文已在乐观锁保护下更新，旧版本已保存为修订记录", "success"}
	case "unchanged":
		return adminNotice{"提交内容与当前版本一致，没有生成空修订或增加版本号", "info"}
	case "replied":
		return adminNotice{"站方回复已使用当前登录身份写入；回复状态继承锁定父评论的审核状态", "success"}
	case "restored":
		return adminNotice{"历史称呼、邮箱与纯文本正文已恢复为新的 canonical 版本；当前审核状态保持不变", "success"}
	default:
		return adminNotice{}
	}
}

func adminCommentEditorURL(id int64, filter adminCommentFilter, notice string) string {
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
	path := "/admin/comments/" + strconv.FormatInt(id, 10) + "/edit/"
	if encoded := query.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func (s *Server) adminCommentAPI(writer http.ResponseWriter, request *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityCommentsModerate); !ok {
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
	item, err := s.comments.Get(request.Context(), id)
	if !s.commentOperationAccess(writer, request, err) {
		return
	}
	if err != nil {
		s.handleAdminCommentAPIError(writer, request, id, err)
		return
	}
	s.writeCommentJSON(writer, request, http.StatusOK, adminCommentResponse(s.adminCommentItem(request, item)), func() { writer.Header().Set("ETag", commentETag(item)) })
}

func (s *Server) adminCommentEditAPI(writer http.ResponseWriter, request *http.Request) {
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
	var body adminCommentEditAPIRequest
	if !s.decodeStrictCommentJSON(writer, request, &body) {
		return
	}
	principal, ok = s.refreshCommentWrite(writer, request)
	if !ok {
		return
	}
	if body.Version <= 0 {
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "comments.validation_failed", "评论版本不正确", map[string]any{"fields": map[string]string{"version": "必须提供当前正整数版本"}})
		return
	}
	input, fields := comment.NormalizeEditInput(comment.EditInput{AuthorName: body.AuthorName, AuthorEmail: body.AuthorEmail, ContentSource: body.ContentSource})
	if len(fields) > 0 {
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "comments.validation_failed", "评论编辑字段没有通过服务端校验", map[string]any{"fields": fields})
		return
	}
	updated, err := s.comments.Edit(request.Context(), id, comment.Mutation{ExpectedVersion: body.Version, Actor: adminCommentActor(principal)}, input)
	if _, ok := s.refreshCommentWrite(writer, request); !ok {
		return
	}
	if err != nil {
		if len(comment.FieldErrors(err)) > 0 {
			s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "comments.validation_failed", "评论编辑字段没有通过仓库校验", map[string]any{"fields": comment.FieldErrors(err)})
			return
		}
		s.handleAdminCommentAPIError(writer, request, id, err)
		return
	}
	s.writeCommentJSON(writer, request, http.StatusOK, adminCommentResponse(s.adminCommentItem(request, updated)), func() { writer.Header().Set("ETag", commentETag(updated)) })
}

func (s *Server) adminCommentReplyAPI(writer http.ResponseWriter, request *http.Request) {
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
		s.writeAPIError(writer, request, http.StatusNotFound, "comments.not_found", "回复目标没有找到", nil)
		return
	}
	var body adminCommentReplyAPIRequest
	if !s.decodeStrictCommentJSON(writer, request, &body) {
		return
	}
	principal, ok = s.refreshCommentWrite(writer, request)
	if !ok {
		return
	}
	maxLength := min(comment.MaxContentRunes, s.siteSettings.Snapshot().Settings.Discussion.MaxLength)
	if utf8.RuneCountInString(strings.TrimSpace(body.ContentSource)) > maxLength {
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "comments.validation_failed", "站方回复超过当前讨论策略上限", map[string]any{"fields": map[string]string{"content_source": "不能超过 " + strconv.Itoa(maxLength) + " 个字符"}})
		return
	}
	created, err := s.comments.Reply(request.Context(), comment.ReplyInput{
		ParentID: id, ExpectedParentVersion: body.ParentVersion, UserID: principal.UserID,
		AuthorName: principal.Label(), AuthorEmail: adminCommentReplyEmail(principal), ContentSource: body.ContentSource,
		Actor: adminCommentActor(principal),
	})
	if err == nil {
		s.captureCommentOrigin(request, created.ID)
	}
	if _, ok := s.refreshCommentWrite(writer, request); !ok {
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, comment.ErrInvalidParent):
			s.writeAPIError(writer, request, http.StatusConflict, "comments.reply_parent_invalid", "当前状态或线程深度不允许继续回复", nil)
		case len(comment.FieldErrors(err)) > 0:
			s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "comments.validation_failed", "站方回复没有通过服务端校验", map[string]any{"fields": commentReplyFieldErrors(comment.FieldErrors(err))})
		default:
			s.handleAdminCommentAPIError(writer, request, id, err)
		}
		return
	}
	s.writeCommentJSON(writer, request, http.StatusCreated, adminCommentResponse(s.adminCommentItem(request, created)), func() {
		writer.Header().Set("Location", "/api/v1/admin/comments/"+strconv.FormatInt(created.ID, 10))
		writer.Header().Set("ETag", commentETag(created))
	})
}

func (s *Server) adminCommentRevisionsAPI(writer http.ResponseWriter, request *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityCommentsModerate); !ok {
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
	revisions, err := s.comments.ListRevisions(request.Context(), id, comment.MaxRevisionLimit)
	if !s.commentOperationAccess(writer, request, err) {
		return
	}
	if err != nil {
		if errors.Is(err, comment.ErrNotFound) {
			s.writeAPIError(writer, request, http.StatusNotFound, "comments.not_found", "评论没有找到", nil)
			return
		}
		slog.Error("list comment revisions API", "id", id, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "comments.revisions_unavailable", "评论修订记录暂时不可用", nil)
		return
	}
	items := make([]adminCommentRevisionAPIResponse, 0, len(revisions))
	for _, revision := range revisions {
		items = append(items, adminCommentRevisionAPIResponse{
			CommentID: revision.CommentID, Version: revision.Version, AuthorName: revision.AuthorName,
			AuthorEmail: revision.AuthorEmail, Status: revision.Status, ContentSource: revision.ContentSource,
			ContentHTML: revision.ContentHTML, Actor: revision.Actor, CreatedAt: revision.CreatedAt,
		})
	}
	s.writeCommentJSON(writer, request, http.StatusOK, adminCommentRevisionsAPIResponse{Items: items}, nil)
}

func (s *Server) adminCommentRestoreRevisionAPI(writer http.ResponseWriter, request *http.Request) {
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
	sourceVersion, err := positivePathID(request.PathValue("revision_version"))
	if err != nil {
		s.writeAPIError(writer, request, http.StatusNotFound, "comments.revision_not_found", "评论修订记录没有找到", nil)
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
	if !s.requireAdminCommentPrecondition(writer, request, current, "恢复评论修订必须携带当前 If-Match ETag") {
		return
	}
	updated, err := s.comments.RestoreRevision(request.Context(), id, sourceVersion, comment.Mutation{
		ExpectedVersion: current.Version, Actor: adminCommentActor(principal),
	})
	if _, ok := s.refreshCommentWrite(writer, request); !ok {
		return
	}
	if err != nil {
		s.handleAdminCommentAPIError(writer, request, id, err)
		return
	}
	s.writeCommentJSON(writer, request, http.StatusOK, adminCommentResponse(s.adminCommentItem(request, updated)), func() { writer.Header().Set("ETag", commentETag(updated)) })
}
