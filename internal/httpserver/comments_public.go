package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/commentorigin"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/idempotency"
	"newpress.local/newpress/internal/presentation"
	"newpress.local/newpress/internal/recognition"
	"newpress.local/newpress/internal/site"
)

const publicCommentRequestMaxBytes = 32 << 10

type publicCommentForm struct {
	ParentID    int64
	AuthorName  string
	AuthorEmail string
	Content     string
}

type publicCommentNode struct {
	Badges      []presentation.IdentityBadge
	Support     recognition.Support
	ID          int64
	ParentID    int64
	AuthorName  string
	Avatar      string
	ContentHTML string
	CreatedAt   time.Time
	EditedAt    time.Time
	StaffReply  bool
	Timezone    string
	Depth       int
	CanReply    bool
	Children    []*publicCommentNode
	Origin      commentorigin.Badges
}

type publicCommentsView struct {
	FormURL          string
	APIURL           string
	RequireLogin     bool
	LoginRequired    bool
	Items            []*publicCommentNode
	Total            int
	Page             int
	PerPage          int
	Pages            int
	PreviousURL      string
	NextURL          string
	Open             bool
	ClosedReason     string
	AllowReplies     bool
	MaxLength        int
	MaxDepth         int
	FormToken        string
	Form             publicCommentForm
	FieldErrors      map[string]string
	Notice           string
	NoticeTone       string
	PrivacyNotice    string
	ModerationNotice string
	AccountName      string
}

type publicCommentAPIResponse struct {
	Badges      []presentation.IdentityBadge `json:"badges,omitempty"`
	Support     recognition.Support          `json:"support"`
	Origin      commentorigin.Badges         `json:"origin"`
	ID          int64                        `json:"id"`
	PostID      int64                        `json:"post_id"`
	ParentID    int64                        `json:"parent_id,omitempty"`
	AuthorName  string                       `json:"author_name"`
	ContentHTML string                       `json:"content_html"`
	StaffReply  bool                         `json:"staff_reply"`
	EditedAt    *time.Time                   `json:"edited_at,omitempty"`
	CreatedAt   time.Time                    `json:"created_at"`
	UpdatedAt   time.Time                    `json:"updated_at"`
}

type publicCommentPaginationAPIResponse struct {
	Page     int    `json:"page"`
	PerPage  int    `json:"per_page"`
	Total    int    `json:"total"`
	Pages    int    `json:"pages"`
	Previous string `json:"previous,omitempty"`
	Next     string `json:"next,omitempty"`
}

type publicCommentSubmissionAPIResponse struct {
	RequiresLogin      bool   `json:"requires_login"`
	Open               bool   `json:"open"`
	ClosedReason       string `json:"closed_reason,omitempty"`
	AllowReplies       bool   `json:"allow_replies"`
	MaxLength          int    `json:"max_length"`
	RequiresModeration bool   `json:"requires_moderation"`
	FormToken          string `json:"form_token,omitempty"`
	PrivacyNotice      string `json:"privacy_notice,omitempty"`
	ModerationNotice   string `json:"moderation_notice,omitempty"`
}

type publicCommentsAPIResponse struct {
	Items      []publicCommentAPIResponse         `json:"items"`
	Pagination publicCommentPaginationAPIResponse `json:"pagination"`
	Submission publicCommentSubmissionAPIResponse `json:"submission"`
}

type publicCommentCreateAPIRequest struct {
	VerificationToken string `json:"verification_token,omitempty"`
	ExpectedUserID    *int64 `json:"expected_user_id,omitempty"`
	FormToken         string `json:"form_token"`
	ParentID          int64  `json:"parent_id,omitempty"`
	AuthorName        string `json:"author_name"`
	Email             string `json:"email"`
	Content           string `json:"content"`
	Website           string `json:"website,omitempty"`
}

type publicCommentCreateAPIResponse struct {
	Status        string                    `json:"status"`
	Notice        string                    `json:"notice"`
	SubmissionKey string                    `json:"submission_key,omitempty"`
	Replayed      bool                      `json:"replayed,omitempty"`
	Comment       *publicCommentAPIResponse `json:"comment,omitempty"`
}

func (s *Server) renderPublicPost(writer http.ResponseWriter, request *http.Request, status int, post content.Post, page int, form publicCommentForm, fields map[string]string, notice adminNotice) {
	if post.Type == content.TypeFlash {
		s.renderFlashSingle(writer, request, status, post, page, form, fields, notice)
		return
	}
	commentsView, err := s.publicCommentsView(request, post, page, form, fields, notice)
	if err != nil {
		slog.Error("load public comments", "post_id", post.ID, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.renderError(writer, http.StatusInternalServerError, "评论暂时不可用")
		return
	}
	data := s.baseViewFor(writer, post.Title, post.Excerpt, s.contentPath(post))
	data.Post = s.withTopicLinks(post)
	data.PluginFragments = s.pluginRender(request.Context(), post)
	data.ReadingSeries = s.readingSeries(request.Context(), post.ID)
	data.Comments = commentsView
	if post.CoverURL != "" {
		data.Site.SocialImageURL = s.publicResourceURL(post.CoverURL)
	}
	data.BodyClass = "is-single"
	data.ActiveNav = "explore"
	if event, ok := request.Context().Value(analyticsContextKey{}).(*analyticsObservation); ok {
		event.PostID = post.ID
	}
	s.render(writer, status, "single", data)
}

func (s *Server) publicCommentsView(request *http.Request, post content.Post, page int, form publicCommentForm, fields map[string]string, notice adminNotice) (publicCommentsView, error) {
	if !s.moduleEnabled("comments") {
		return publicCommentsView{}, nil
	}
	settings := s.siteSnapshot(request.Context()).Settings
	if page < 1 {
		page = 1
	}
	perPage := settings.Discussion.CommentsPerPage
	offset := (page - 1) * perPage
	items, total, err := s.comments.ListApproved(request.Context(), post.ID, perPage, offset)
	if err != nil {
		return publicCommentsView{}, err
	}
	open, closedReason := publicCommentSubmissionsOpen(post, settings, time.Now())
	pages := max(1, (total+perPage-1)/perPage)
	view := publicCommentsView{
		FormURL: strings.TrimRight(s.contentPath(post), "/") + "/comments", APIURL: commentAPIURL(post),
		Items: buildPublicCommentTree(items, open && settings.Discussion.AllowReplies, settings.Identity.Timezone),
		Total: total, Page: page, PerPage: perPage, Pages: pages,
		RequireLogin: settings.Discussion.RequireLogin, LoginRequired: settings.Discussion.RequireLogin,
		Open: open, ClosedReason: closedReason, AllowReplies: settings.Discussion.AllowReplies,
		MaxLength: settings.Discussion.MaxLength, MaxDepth: comment.MaxThreadDepth, Form: form, FieldErrors: fields,
		Notice: notice.Message, NoticeTone: notice.Tone,
		PrivacyNotice: settings.Discussion.PrivacyNotice, ModerationNotice: settings.Discussion.ModerationNotice,
	}
	if p, ok := s.currentAdmin(request); ok && p.UserID > 0 {
		view.AccountName = p.Label()
		view.LoginRequired = false
		view.Form.AuthorName, view.Form.AuthorEmail = "", ""
	}
	s.decorateCommentOrigins(request, view.Items, items)
	s.decorateCommentRecognition(request, view.Items, items, post.ID)
	if s.commentOrigin != nil {
		if notice := s.commentOrigin.Notice(); notice != "" {
			view.PrivacyNotice += " " + notice
		}
	}
	if open || settings.API.PublicPostsEnabled {
		view.FormToken = s.commentGuard.formToken(post.ID, time.Now())
	}
	if page > 1 {
		view.PreviousURL = contentCommentPageURL(post, page-1, s.contentPath)
	}
	if offset+len(items) < total {
		view.NextURL = contentCommentPageURL(post, page+1, s.contentPath)
	}
	return view, nil
}

func (s *Server) publicCommentCreate(writer http.ResponseWriter, request *http.Request) {
	slug := request.PathValue("slug")
	post, ok := s.loadPublicCommentPost(writer, request, slug)
	if !ok {
		return
	}
	settings := s.siteSnapshot(request.Context()).Settings
	if open, reason := publicCommentSubmissionsOpen(post, settings, time.Now()); !open {
		s.renderPublicPost(writer, request, http.StatusConflict, post, 1, publicCommentForm{}, nil, adminNotice{reason, "warning"})
		return
	}
	if !s.sameOriginPublicWrite(request) {
		s.renderError(writer, http.StatusForbidden, "评论提交来源未通过校验")
		return
	}
	form, ok := s.readPublicCommentForm(writer, request)
	if !ok {
		return
	}
	if !s.commentGuard.verifyFormToken(form.Get("form_token"), post.ID, time.Now()) {
		s.renderError(writer, http.StatusForbidden, "评论表单已失效，请刷新文章后重试")
		return
	}
	parentID, parentErr := optionalPositiveID(form.Get("parent_id"))
	input := publicCommentForm{
		ParentID: parentID, AuthorName: form.Get("author_name"), AuthorEmail: form.Get("email"), Content: form.Get("content"),
	}
	fields := map[string]string{}
	if parentErr != nil {
		fields["parent_id"] = "回复目标不正确"
	}
	userID := s.bindCommentAccount(request, &input)
	if settings.Discussion.RequireLogin && userID == 0 {
		s.renderPublicPost(writer, request, http.StatusUnauthorized, post, 1, input, nil, adminNotice{"请先登录账号再发表评论，输入内容已保留。", "warning"})
		return
	}
	normalized, validationFields := normalizePublicCommentInput(post, settings, input, s.commentGuard.sourceDigest(s.clientIPs.string(request)), request.UserAgent())
	normalized.UserID = userID
	for key, message := range validationFields {
		fields[key] = message
	}
	if len(fields) > 0 {
		s.renderPublicPost(writer, request, http.StatusUnprocessableEntity, post, 1, input, fields, adminNotice{"请检查标记字段后再提交", "error"})
		return
	}
	allowed, retry := s.commentGuard.allow(normalized.AuthorIPHash, post.ID, settings.Discussion.RateLimitPerTenMinutes, time.Now())
	if !allowed {
		writer.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Round(time.Second).Seconds()))))
		s.renderPublicPost(writer, request, http.StatusTooManyRequests, post, 1, input, nil, adminNotice{"提交过于频繁，请稍后再试", "warning"})
		return
	}
	if strings.TrimSpace(form.Get("website")) != "" {
		http.Redirect(writer, request, s.contentPath(post)+"?comment=pending#comments", http.StatusSeeOther)
		return
	}
	if err := s.checkVerification(request, "comment", form.Get("verification_token")); err != nil {
		s.renderPublicPost(writer, request, http.StatusUnprocessableEntity, post, 1, input, nil, adminNotice{err.Error(), "error"})
		return
	}
	created, err := s.comments.Create(request.Context(), normalized)
	if err != nil {
		if errors.Is(err, comment.ErrInvalidParent) {
			s.renderPublicPost(writer, request, http.StatusUnprocessableEntity, post, 1, input, map[string]string{"parent_id": "回复目标已失效或不属于这篇文章"}, adminNotice{"请选择仍在公开显示的回复目标", "error"})
			return
		}
		if validation := publicCommentFieldErrors(err); len(validation) > 0 {
			s.renderPublicPost(writer, request, http.StatusUnprocessableEntity, post, 1, input, validation, adminNotice{"评论没有通过服务端校验", "error"})
			return
		}
		slog.Error("create public comment", "post_id", post.ID, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.renderError(writer, http.StatusInternalServerError, "评论提交失败")
		return
	}
	s.enqueueComment(request.Context(), created.ID)
	s.captureCommentOrigin(request, created.ID)
	noticeCode := "pending"
	anchor := "comments"
	if created.Status == comment.StatusApproved {
		noticeCode = "approved"
		anchor = "comment-" + strconv.FormatInt(created.ID, 10)
	}
	http.Redirect(writer, request, s.contentPath(post)+"?comment="+noticeCode+"#"+anchor, http.StatusSeeOther)
}

func (s *Server) publicCommentsAPI(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	settings := s.siteSnapshot(request.Context()).Settings
	if !settings.API.PublicPostsEnabled {
		s.writeAPIError(writer, request, http.StatusNotFound, "request.not_found", "资源不存在", nil)
		return
	}
	post, ok := s.loadPublicCommentPostAPI(writer, request)
	if !ok {
		return
	}
	query, ok := s.strictAPIQuery(writer, request, map[string]bool{"page": true, "per_page": true})
	if !ok {
		return
	}
	page, err := positiveQueryInt(query.Get("page"), 1, 1_000_000)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "page 必须是有效的正整数", map[string]any{"field": "page"})
		return
	}
	perPage, err := positiveQueryInt(query.Get("per_page"), settings.Discussion.CommentsPerPage, comment.MaxListLimit)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "per_page 必须介于 1 和 200 之间", map[string]any{"field": "per_page"})
		return
	}
	offset := (page - 1) * perPage
	items, total, err := s.comments.ListApproved(request.Context(), post.ID, perPage, offset)
	if err != nil {
		slog.Error("list public comments API", "post_id", post.ID, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "comments.unavailable", "评论暂时不可用", nil)
		return
	}
	responses := make([]publicCommentAPIResponse, 0, len(items))
	badges, tips := s.commentRecognition(request.Context(), post.ID, items)
	for _, item := range items {
		response := publicCommentResponse(item)
		response.Origin = s.commentOriginBadges(request, item)
		response.Badges, response.Support = badges[item.ID], tips[item.ID]
		responses = append(responses, response)
	}
	open, reason := publicCommentSubmissionsOpen(post, settings, time.Now())
	pages := max(1, (total+perPage-1)/perPage)
	pagination := publicCommentPaginationAPIResponse{Page: page, PerPage: perPage, Total: total, Pages: pages}
	if page > 1 {
		pagination.Previous = commentAPIURL(post) + "?" + url.Values{"page": {strconv.Itoa(page - 1)}, "per_page": {strconv.Itoa(perPage)}}.Encode()
	}
	if offset+len(items) < total {
		pagination.Next = commentAPIURL(post) + "?" + url.Values{"page": {strconv.Itoa(page + 1)}, "per_page": {strconv.Itoa(perPage)}}.Encode()
	}
	submission := publicCommentSubmissionAPIResponse{
		RequiresLogin: settings.Discussion.RequireLogin,
		Open:          open, ClosedReason: reason, AllowReplies: settings.Discussion.AllowReplies,
		MaxLength: settings.Discussion.MaxLength, RequiresModeration: settings.Discussion.RequireModeration,
		PrivacyNotice: settings.Discussion.PrivacyNotice, ModerationNotice: settings.Discussion.ModerationNotice,
	}
	if s.commentOrigin != nil {
		if notice := s.commentOrigin.Notice(); notice != "" {
			submission.PrivacyNotice += " " + notice
		}
	}
	// A closed discussion can still confirm a previous submission. A fresh
	// token authorizes that lookup; the write path enforces the current policy.
	submission.FormToken = s.commentGuard.formToken(post.ID, time.Now())
	writeJSON(writer, http.StatusOK, publicCommentsAPIResponse{Items: responses, Pagination: pagination, Submission: submission})
}

func (s *Server) publicCommentCreateAPI(writer http.ResponseWriter, request *http.Request) {
	s.publicCommentSubmissionAPI(writer, request, false)
}

func (s *Server) publicCommentReceiptAPI(writer http.ResponseWriter, request *http.Request) {
	s.publicCommentSubmissionAPI(writer, request, true)
}

func (s *Server) publicCommentSubmissionAPI(writer http.ResponseWriter, request *http.Request, lookupOnly bool) {
	settings := s.siteSnapshot(request.Context()).Settings
	if !settings.API.PublicPostsEnabled {
		s.writeAPIError(writer, request, http.StatusNotFound, "request.not_found", "资源不存在", nil)
		return
	}
	post, ok := s.loadPublicCommentPostAPI(writer, request)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	key, err := strictIdempotencyKey(request)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_idempotency_key", "提交标识格式不正确", nil)
		return
	}
	if !s.sameOriginPublicWrite(request) {
		s.writeAPIError(writer, request, http.StatusForbidden, "comments.origin_rejected", "评论提交来源未通过校验", nil)
		return
	}
	var body publicCommentCreateAPIRequest
	if !s.decodeStrictCommentJSON(writer, request, &body) {
		return
	}
	if lookupOnly && (key == "" || body.ExpectedUserID == nil) {
		s.writeAPIError(writer, request, http.StatusBadRequest, "comments.submission_identity_required", "查询回执需要原提交标识和账号身份。", nil)
		return
	}
	if !s.commentGuard.verifyFormToken(body.FormToken, post.ID, time.Now()) {
		s.writeAPIError(writer, request, http.StatusForbidden, "comments.form_token_invalid", "评论表单令牌无效或已过期", nil)
		return
	}
	form := publicCommentForm{ParentID: body.ParentID, AuthorName: body.AuthorName, AuthorEmail: body.Email, Content: body.Content}
	userID := s.bindCommentAccount(request, &form)
	submission := publicCommentSubmission(key, post.ID, userID, body)
	if body.ExpectedUserID != nil && (*body.ExpectedUserID < 0 || *body.ExpectedUserID != userID) {
		s.writeAPIError(writer, request, http.StatusConflict, "comments.identity_changed", "评论账号已变化，请恢复原提交身份后再确认。", nil)
		return
	}
	if key != "" {
		result, err := s.comments.FindSubmission(request.Context(), submission.Key, submission.Fingerprint)
		if err == nil {
			s.writeCommentSubmissionResult(writer, request, post, key, result)
			return
		}
		if !errors.Is(err, comment.ErrNotFound) {
			s.writeCommentSubmissionError(writer, request, err)
			return
		}
	}
	// Receipt queries never fall through to creation, even if the discussion
	// reopened after this page was rendered or the original write is in flight.
	if lookupOnly {
		s.writeAPIError(writer, request, http.StatusNotFound, "comments.submission_not_found", "暂未查到这次提交的回执，原内容仍保留。可以稍后再次查询。", nil)
		return
	}
	if open, reason := publicCommentSubmissionsOpen(post, settings, time.Now()); !open {
		s.writeAPIError(writer, request, http.StatusConflict, "comments.closed", reason, nil)
		return
	}
	if settings.Discussion.RequireLogin && userID == 0 {
		s.writeAPIError(writer, request, http.StatusUnauthorized, "comments.login_required", "请先登录账号再发表评论，草稿已保留。", nil)
		return
	}
	normalized, fields := normalizePublicCommentInput(post, settings, form, s.commentGuard.sourceDigest(s.clientIPs.string(request)), request.UserAgent())
	normalized.UserID = userID
	if len(fields) > 0 {
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "comments.validation_failed", "评论没有通过服务端校验", map[string]any{"fields": fields})
		return
	}
	allowed, retry := s.commentGuard.allow(normalized.AuthorIPHash, post.ID, settings.Discussion.RateLimitPerTenMinutes, time.Now())
	if !allowed {
		seconds := max(1, int(retry.Round(time.Second).Seconds()))
		writer.Header().Set("Retry-After", strconv.Itoa(seconds))
		s.writeAPIError(writer, request, http.StatusTooManyRequests, "comments.rate_limited", "提交过于频繁，请稍后再试", map[string]any{"retry_after": seconds})
		return
	}
	if strings.TrimSpace(body.Website) != "" {
		writeJSON(writer, http.StatusAccepted, publicCommentCreateAPIResponse{Status: comment.StatusPending, Notice: settings.Discussion.ModerationNotice, SubmissionKey: key})
		return
	}
	if !s.requireVerification(writer, request, "comment", body.VerificationToken) {
		return
	}
	var created comment.Comment
	if key != "" {
		submission.Input = normalized
		var result comment.SubmissionResult
		result, err = s.comments.CreateOnce(request.Context(), submission)
		if err == nil {
			if !result.Replayed {
				s.enqueueComment(request.Context(), result.Receipt.CommentID)
				s.captureCommentOrigin(request, result.Receipt.CommentID)
			}
			s.writeCommentSubmissionResult(writer, request, post, key, result)
			return
		}
	} else {
		created, err = s.comments.Create(request.Context(), normalized)
	}
	if err != nil {
		if errors.Is(err, idempotency.ErrConflict) || errors.Is(err, idempotency.ErrCapacity) {
			s.writeCommentSubmissionError(writer, request, err)
			return
		}
		if errors.Is(err, comment.ErrInvalidParent) {
			s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "comments.invalid_parent", "回复目标已失效或不属于这篇文章", map[string]any{"fields": map[string]string{"parent_id": "请选择仍在公开显示的回复目标"}})
			return
		}
		if validation := publicCommentFieldErrors(err); len(validation) > 0 {
			s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "comments.validation_failed", "评论没有通过服务端校验", map[string]any{"fields": validation})
			return
		}
		slog.Error("create public comment API", "post_id", post.ID, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "comments.create_failed", "评论提交失败", nil)
		return
	}
	s.enqueueComment(request.Context(), created.ID)
	s.captureCommentOrigin(request, created.ID)
	responseComment := s.publicCommentWithOrigin(request, created)
	status := http.StatusAccepted
	notice := settings.Discussion.ModerationNotice
	if created.Status == comment.StatusApproved {
		status = http.StatusCreated
		notice = "评论已发布"
		writer.Header().Set("Location", s.contentPath(post)+"#comment-"+strconv.FormatInt(created.ID, 10))
	}
	writeJSON(writer, status, publicCommentCreateAPIResponse{Status: created.Status, Notice: notice, Comment: &responseComment})
}

func (s *Server) loadPublicCommentPost(writer http.ResponseWriter, request *http.Request, slug string) (content.Post, bool) {
	if !validSlug(slug) {
		s.renderError(writer, http.StatusNotFound, "文章没有找到")
		return content.Post{}, false
	}
	post, err := s.commentPostForRequest(request, slug)
	if errors.Is(err, content.ErrNotFound) {
		if request.URL.Path == "/posts/"+slug && s.tryContentRedirect(writer, request) {
			return content.Post{}, false
		}
		s.renderError(writer, http.StatusNotFound, "文章没有找到")
		return content.Post{}, false
	}
	if err != nil {
		slog.Error("load comment post", "slug", slug, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.renderError(writer, http.StatusInternalServerError, "内容暂时不可用")
		return content.Post{}, false
	}
	return post, true
}

func (s *Server) loadPublicCommentPostAPI(writer http.ResponseWriter, request *http.Request) (content.Post, bool) {
	slug := request.PathValue("slug")
	if !validSlug(slug) {
		s.writeAPIError(writer, request, http.StatusNotFound, "content.not_found", "文章没有找到", nil)
		return content.Post{}, false
	}
	post, err := s.commentPostForRequest(request, slug)
	if errors.Is(err, content.ErrNotFound) {
		s.writeAPIError(writer, request, http.StatusNotFound, "content.not_found", "文章没有找到", nil)
		return content.Post{}, false
	}
	if err != nil {
		slog.Error("load public comment API post", "slug", slug, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "content.unavailable", "内容暂时不可用", nil)
		return content.Post{}, false
	}
	return post, true
}

func (s *Server) readPublicCommentForm(writer http.ResponseWriter, request *http.Request) (url.Values, bool) {
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/x-www-form-urlencoded") {
		s.renderError(writer, http.StatusUnsupportedMediaType, "评论表单必须使用标准表单编码")
		return nil, false
	}
	if charset, exists := parameters["charset"]; exists && !strings.EqualFold(charset, "utf-8") {
		s.renderError(writer, http.StatusUnsupportedMediaType, "评论表单必须使用 UTF-8")
		return nil, false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, publicCommentRequestMaxBytes)
	if err := request.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.renderError(writer, http.StatusRequestEntityTooLarge, "评论表单超过 32 KiB 上限")
			return nil, false
		}
		s.renderError(writer, http.StatusBadRequest, "评论表单格式不正确")
		return nil, false
	}
	allowed := map[string]bool{
		"form_token": true, "parent_id": true, "author_name": true, "email": true, "content": true, "website": true,
	}
	for key, entries := range request.PostForm {
		if (!allowed[key] && key != "verification_token") || len(entries) != 1 {
			s.renderError(writer, http.StatusBadRequest, "评论表单包含未知或重复字段")
			return nil, false
		}
	}
	for key := range allowed {
		if len(request.PostForm[key]) != 1 {
			s.renderError(writer, http.StatusBadRequest, "评论表单缺少必要字段")
			return nil, false
		}
	}
	return request.PostForm, true
}

func (s *Server) decodeStrictCommentJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		s.writeAPIError(writer, request, http.StatusUnsupportedMediaType, "request.json_content_type_required", "请求必须使用 application/json", nil)
		return false
	}
	if charset, exists := parameters["charset"]; exists && !strings.EqualFold(charset, "utf-8") {
		s.writeAPIError(writer, request, http.StatusUnsupportedMediaType, "request.unsupported_charset", "JSON 请求必须使用 UTF-8", nil)
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, publicCommentRequestMaxBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeAPIError(writer, request, http.StatusRequestEntityTooLarge, "request.body_too_large", "评论 JSON 请求体不能超过 32 KiB", nil)
			return false
		}
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_body", "无法读取请求体", nil)
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.empty_body", "JSON 请求体不能为空", nil)
		return false
	}
	if err := validateUniqueJSONObject(body, target); err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_json", "JSON 对象格式不正确", map[string]any{"reason": boundedAPIErrorReason(err)})
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_json", "JSON 字段未知或类型不正确", map[string]any{"reason": boundedAPIErrorReason(err)})
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_json", "JSON 请求只能包含一个对象", nil)
		return false
	}
	return true
}

func normalizePublicCommentInput(post content.Post, settings site.Settings, form publicCommentForm, sourceDigest, userAgent string) (comment.CreateInput, map[string]string) {
	status := comment.StatusApproved
	if settings.Discussion.RequireModeration {
		status = comment.StatusPending
	}
	input := comment.CreateInput{
		PostID: post.ID, ParentID: form.ParentID, AuthorName: form.AuthorName, AuthorEmail: form.AuthorEmail,
		Status: status, ContentSource: form.Content, AuthorIPHash: sourceDigest, UserAgent: boundedCommentUserAgent(userAgent),
	}
	normalized, rawFields := comment.NormalizeCreateInput(input)
	fields := publicCommentValidationFields(rawFields)
	if utf8.RuneCountInString(normalized.ContentSource) > settings.Discussion.MaxLength {
		fields["content"] = "评论不能超过 " + strconv.Itoa(settings.Discussion.MaxLength) + " 个字符"
	}
	if !settings.Discussion.AllowReplies && normalized.ParentID != 0 {
		fields["parent_id"] = "站点当前未开放嵌套回复"
	}
	return normalized, fields
}

func publicCommentFieldErrors(err error) map[string]string {
	return publicCommentValidationFields(comment.FieldErrors(err))
}

func publicCommentValidationFields(fields map[string]string) map[string]string {
	result := make(map[string]string, len(fields))
	for key, message := range fields {
		switch key {
		case "author_email":
			key = "email"
		case "content_source":
			key = "content"
		case "post_id", "status", "author_ip_hash", "user_agent", "user_id":
			continue
		}
		result[key] = message
	}
	return result
}

func publicCommentSubmissionsOpen(post content.Post, settings site.Settings, now time.Time) (bool, string) {
	if !settings.Discussion.PublicSubmissionsEnabled {
		return false, "站点当前暂停接收新评论，已审核讨论仍可阅读。"
	}
	if post.CommentStatus != content.CommentOpen {
		return false, "这篇文章已关闭评论。"
	}
	if days := settings.Discussion.AutoCloseDays; days > 0 && !post.PublishedAt.IsZero() && now.After(post.PublishedAt.In(site.TimeLocation(settings)).AddDate(0, 0, days)) {
		return false, "这篇文章已超过评论开放期限。"
	}
	return true, ""
}

func buildPublicCommentTree(items []comment.Comment, allowReplies bool, timezone string) []*publicCommentNode {
	nodes := make(map[int64]*publicCommentNode, len(items))
	ordered := make([]*publicCommentNode, 0, len(items))
	for _, item := range items {
		node := &publicCommentNode{
			ID: item.ID, ParentID: item.ParentID, AuthorName: item.AuthorName, Avatar: commentAvatar(item.AuthorName),
			ContentHTML: item.ContentHTML, CreatedAt: item.CreatedAt, EditedAt: item.EditedAt, StaffReply: item.StaffReply,
			Timezone: timezone, Children: []*publicCommentNode{},
		}
		nodes[item.ID] = node
		ordered = append(ordered, node)
	}
	roots := make([]*publicCommentNode, 0, len(ordered))
	for _, node := range ordered {
		if parent, exists := nodes[node.ParentID]; node.ParentID > 0 && exists && parent != node {
			parent.Children = append(parent.Children, node)
			continue
		}
		roots = append(roots, node)
	}
	var setDepth func(*publicCommentNode, int)
	setDepth = func(node *publicCommentNode, depth int) {
		node.Depth = depth
		node.CanReply = allowReplies && depth < comment.MaxThreadDepth
		for _, child := range node.Children {
			setDepth(child, depth+1)
		}
	}
	for _, root := range roots {
		setDepth(root, 1)
	}
	return roots
}

func publicCommentResponse(item comment.Comment) publicCommentAPIResponse {
	var editedAt *time.Time
	if !item.EditedAt.IsZero() {
		value := item.EditedAt
		editedAt = &value
	}
	return publicCommentAPIResponse{
		ID: item.ID, PostID: item.PostID, ParentID: item.ParentID, AuthorName: item.AuthorName,
		ContentHTML: item.ContentHTML, StaffReply: item.StaffReply, EditedAt: editedAt,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func commentAvatar(name string) string {
	runes := []rune(strings.TrimSpace(name))
	if len(runes) == 0 {
		return "?"
	}
	return strings.ToUpper(string(runes[0]))
}

func optionalPositiveID(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, comment.ErrInvalidInput
	}
	return value, nil
}

func publicCommentPageURL(slug string, page int) string {
	if page <= 1 {
		return "/posts/" + slug + "#comments"
	}
	return "/posts/" + slug + "?comments_page=" + strconv.Itoa(page) + "#comments"
}

func publicCommentsAPIPageURL(slug string, page, perPage int) string {
	query := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(perPage)}}
	return "/api/v1/posts/" + slug + "/comments?" + query.Encode()
}
