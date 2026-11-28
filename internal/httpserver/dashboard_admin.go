package httpserver

import (
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/theme"
)

const quickDraftRequestMaxBytes = content.MaxContentBytes*3 + (64 << 10)

type adminDashboardView struct {
	QuickDraft                                                          adminQuickDraftView
	RecentDrafts                                                        []adminRecentDraftView
	DraftTotal                                                          int
	DraftError, PostsError, PendingError, CommentsError, ScheduledError bool
	PendingTotal                                                        int
	Scheduled                                                           []adminRecentDraftView
	ScheduledTotal                                                      int
	CheckedAt, Timezone                                                 string
}

type adminQuickDraftView struct {
	Title         string
	Excerpt       string
	ContentSource string
	FieldErrors   map[string]string
}

type adminRecentDraftView struct {
	Post         content.Post
	EditURL      string
	UpdatedLabel string
}

func (s *Server) adminQuickDraftCreate(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	if readOnly || !s.auth.Configured() {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "admin_credentials_not_configured"})
		return
	}
	if !canCreateContent(principal) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "content_write_capability_required"})
		return
	}
	if request.URL.Path != "/admin/quick-draft/" || request.URL.RawQuery != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_quick_draft_target"})
		return
	}
	if !s.sameOriginRequest(request) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "cross_site_request_rejected"})
		return
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/x-www-form-urlencoded") || !validUTF8FormParameters(parameters) {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "form_content_type_required"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, quickDraftRequestMaxBytes)
	if err := request.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]string{"error": "quick_draft_form_too_large"})
			return
		}
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_quick_draft_form"})
		return
	}
	form := request.PostForm
	if !strictQuickDraftForm(form) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_quick_draft_form_shape"})
		return
	}
	principal, ok = s.refreshContentWrite(writer, request)
	if !ok {
		return
	}
	if form.Get("action") != "quick_draft" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unknown_quick_draft_action"})
		return
	}

	settings := s.siteSettings.Snapshot().Settings
	commentStatus := content.CommentClosed
	if settings.Writing.PostCommentsEnabled {
		commentStatus = content.CommentOpen
	}
	input := content.PostInput{
		AuthorID: principal.UserID, Type: content.TypePost, Status: content.StatusDraft,
		Title: form.Get("title"), Excerpt: form.Get("excerpt"), ContentSource: form.Get("content_source"),
		AuthorName: principal.Label(), Category: settings.Writing.DefaultCategory, CommentStatus: commentStatus,
		Actor: principal.Username,
	}
	normalized, fields := content.NormalizePostInput(input, time.Now())
	quickDraft := adminQuickDraftView{
		Title: form.Get("title"), Excerpt: form.Get("excerpt"), ContentSource: form.Get("content_source"), FieldErrors: fields,
	}
	if len(fields) > 0 {
		s.renderAdminDashboard(writer, request, http.StatusUnprocessableEntity, principal, false, quickDraft,
			adminNotice{Message: "快速草稿尚未保存，请检查标记字段", Tone: "error"})
		return
	}
	post, err, ok := s.createQuickDraft(writer, request, normalized)
	if !ok {
		return
	}
	if err != nil {
		if validation := content.FieldErrors(err); len(validation) > 0 {
			quickDraft.FieldErrors = validation
			s.renderAdminDashboard(writer, request, http.StatusUnprocessableEntity, principal, false, quickDraft,
				adminNotice{Message: "快速草稿没有通过服务端校验", Tone: "error"})
			return
		}
		if errors.Is(err, content.ErrSlugConflict) {
			quickDraft.FieldErrors = map[string]string{"title": "无法为这个标题生成唯一草稿地址，请调整标题后重试"}
			s.renderAdminDashboard(writer, request, http.StatusConflict, principal, false, quickDraft,
				adminNotice{Message: "草稿地址发生冲突", Tone: "error"})
			return
		}
		slog.Error("create dashboard quick draft", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "快速草稿暂时无法保存")
		return
	}
	slog.Info("dashboard quick draft created", "post_id", post.ID, "actor", principal.Username)
	http.Redirect(writer, request, "/admin/?notice=quick-draft-created", http.StatusSeeOther)
}

func (s *Server) createQuickDraft(writer http.ResponseWriter, request *http.Request, input content.PostInput) (content.Post, error, bool) {
	baseSlug := input.Slug
	for attempt := 1; attempt <= 20; attempt++ {
		candidate := input
		if attempt > 1 {
			candidate.Slug = suffixedQuickDraftSlug(baseSlug, attempt)
		}
		if _, ok := s.refreshContentWrite(writer, request); !ok {
			return content.Post{}, nil, false
		}
		post, err := s.repository.Create(request.Context(), candidate)
		if _, ok := s.refreshContentWrite(writer, request); !ok {
			return content.Post{}, nil, false
		}
		if !errors.Is(err, content.ErrSlugConflict) {
			return post, err, true
		}
	}
	return content.Post{}, content.ErrSlugConflict, true
}

func suffixedQuickDraftSlug(base string, suffixNumber int) string {
	suffix := "-" + strconv.Itoa(suffixNumber)
	maximum := content.MaxSlugBytes - len(suffix)
	if maximum < 1 {
		return "draft" + suffix
	}
	if len(base) > maximum {
		base = strings.TrimRight(base[:maximum], "-")
	}
	if base == "" {
		base = "draft"
	}
	return base + suffix
}

func validUTF8FormParameters(parameters map[string]string) bool {
	if len(parameters) == 0 {
		return true
	}
	return len(parameters) == 1 && strings.EqualFold(parameters["charset"], "utf-8")
}

func strictQuickDraftForm(form url.Values) bool {
	allowed := map[string]bool{
		"csrf_token": true, "action": true, "title": true, "excerpt": true, "content_source": true,
	}
	if len(form) != len(allowed) {
		return false
	}
	for key, values := range form {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	return true
}

func dashboardNoticeFromQuery(raw string) (adminNotice, error) {
	if raw == "" {
		return adminNotice{}, nil
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) != 1 || len(values["notice"]) != 1 {
		return adminNotice{}, errors.New("invalid dashboard query")
	}
	switch values.Get("notice") {
	case "quick-draft-created":
		return adminNotice{Message: "快速草稿已安全保存，并加入最近草稿队列", Tone: "success"}, nil
	default:
		return adminNotice{}, errors.New("unknown dashboard notice")
	}
}

func (s *Server) renderAdminDashboard(writer http.ResponseWriter, request *http.Request, status int, principal auth.Principal, readOnly bool, quickDraft adminQuickDraftView, notice adminNotice) {
	plugins := []plugin.Installed(nil)
	themes := []theme.Manifest(nil)
	if readOnly || principal.Can(auth.CapabilityPluginsManage) || principal.Can(auth.CapabilitySystemRead) {
		var pluginErr error
		plugins, pluginErr = s.plugins.List()
		if pluginErr != nil {
			slog.Error("list plugins for dashboard", "error", pluginErr)
		}
	}
	if readOnly || principal.Can(auth.CapabilityAppearanceManage) || principal.Can(auth.CapabilitySystemRead) {
		var themeErr error
		themes, themeErr = s.themes.List()
		if themeErr != nil {
			slog.Error("list themes for dashboard", "error", themeErr)
		}
	}
	authorID := contentAuthorScope(principal)
	now := time.Now()
	location := site.TimeLocation(s.siteSnapshot(request.Context()).Settings)
	dashboard := adminDashboardView{QuickDraft: quickDraft, CheckedAt: now.In(location).Format("15:04:05"), Timezone: location.String()}
	var posts []content.Post
	var checkedPosts []content.Post
	if principal.Can(auth.CapabilityContentRead) {
		result, err := s.repository.ListAdmin(request.Context(), content.AdminQuery{AuthorID: authorID, Status: content.StatusPublish, Limit: 5})
		dashboard.PostsError = err != nil
		if err != nil {
			slog.Error("dashboard public content", "error", err)
		}
		for _, post := range result.Items {
			if content.PublicAt(post, now) {
				posts = append(posts, post)
			}
		}
		checkedPosts = append(checkedPosts, result.Items...)
		drafts, err := s.repository.ListAdmin(request.Context(), content.AdminQuery{AuthorID: authorID, Status: content.StatusDraft, Limit: 5})
		dashboard.DraftError = err != nil
		if err != nil {
			slog.Error("dashboard drafts", "error", err)
		} else {
			dashboard.DraftTotal = drafts.Total
			checkedPosts = append(checkedPosts, drafts.Items...)
			for _, post := range drafts.Items {
				dashboard.RecentDrafts = append(dashboard.RecentDrafts, adminRecentDraftView{Post: post, EditURL: "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/edit/", UpdatedLabel: post.UpdatedAt.In(location).Format("01-02 15:04")})
			}
		}
		pending, err := s.repository.ListAdmin(request.Context(), content.AdminQuery{AuthorID: authorID, Status: content.StatusPending, Type: content.TypePost, Limit: 1})
		dashboard.PendingError = err != nil
		if err != nil {
			slog.Error("dashboard pending content", "error", err)
		} else {
			dashboard.PendingTotal = pending.Total
			checkedPosts = append(checkedPosts, pending.Items...)
		}
		scheduled, err := s.repository.ListAdmin(request.Context(), content.AdminQuery{AuthorID: authorID, Status: content.StatusFuture, PublishedAfter: now, PublishedBefore: now.In(location).AddDate(0, 0, 7), PublicationAscending: true, Limit: 5})
		dashboard.ScheduledError = err != nil
		if err != nil {
			slog.Error("dashboard scheduled content", "error", err)
		} else {
			dashboard.ScheduledTotal = scheduled.Total
			checkedPosts = append(checkedPosts, scheduled.Items...)
			for _, post := range scheduled.Items {
				dashboard.Scheduled = append(dashboard.Scheduled, adminRecentDraftView{Post: post, EditURL: "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/edit/", UpdatedLabel: post.PublishedAt.In(location).Format("01-02 15:04")})
			}
		}
	}
	data := s.baseViewFor(writer, "控制台", "NewPress 内容运营、扩展与系统健康总览", "/admin/")
	data.Plugins = plugins
	data.Themes = themes
	data.BodyClass = "is-admin"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("dashboard", principal, readOnly)
	data.Admin.PluginCount = len(plugins)
	data.Admin.ThemeCount = len(themes)
	data.Admin.Notice = notice.Message
	data.Admin.NoticeTone = notice.Tone
	data.Posts = posts
	data.Dashboard = dashboard
	commentsLoaded := s.moduleEnabled("comments") && (readOnly || principal.Can(auth.CapabilityCommentsModerate))
	if commentsLoaded {
		if result, commentErr := s.comments.Snapshot(request.Context()); commentErr == nil {
			data.CommentAdmin = adminCommentsView{Total: result.StatusCounts[comment.StatusPending], StatusCounts: result.StatusCounts, Status: comment.StatusPending}
		} else {
			data.Dashboard.CommentsError = true
			slog.Error("read pending comment count for dashboard", "error", commentErr)
		}
	}
	access := func() error { return s.dashboardAccess(request, principal, checkedPosts, commentsLoaded) }
	s.renderChecked(writer, status, "admin", data, request, s.contentBeforeOutput(writer, request, false, access), access)
}
