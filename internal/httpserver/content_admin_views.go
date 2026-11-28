package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/site"
)

const adminContentBulkFormMaxBytes = 32 << 10

type adminContentView struct {
	Rows          []adminContentRow
	StatusFilters []adminContentFilter
	TypeFilters   []adminContentFilter
	Search        string
	Type          string
	Status        string
	Total         int
	Page          int
	Pages         int
	PreviousURL   string
	NextURL       string
	HasPrevious   bool
	HasNext       bool
}

type adminContentRow struct {
	AIAction     bool
	AIState      string
	Post         content.Post
	TypeLabel    string
	StatusLabel  string
	StatusTone   string
	UpdatedLabel string
	EditURL      string
	PublicURL    string
	IsPublic     bool
}

type adminContentFilter struct {
	Value  string
	Label  string
	URL    string
	Count  int
	Active bool
}

type adminEditorView struct {
	PostPermalink, PagePermalink, SlugMode, PermalinkDate string
	Pinned                                                bool
	BadgePreview                                          content.ArticleBadge
	BadgeGroups                                           []content.ArticleBadgeGroup
	BadgeStyles                                           []content.ArticleBadgeOption
	BadgeTones                                            []content.ArticleBadgeOption
	BadgeIcons                                            []content.ArticleBadgeOption
	IsNew                                                 bool
	Prefilled                                             bool
	Post                                                  content.Post
	Input                                                 content.PostInput
	FormAction                                            string
	PublishedAtLocal                                      string
	PublishedSource                                       string
	PublicURL                                             string
	IsPublic                                              bool
	Revisions                                             []adminRevisionView
	Media                                                 []adminEditorMedia
	Categories                                            []content.Term
	AvailableTags                                         []content.Term
	SelectedCategory                                      map[int64]bool
	FallbackCategory                                      string
	TagValue                                              string
	Conflict                                              bool
	Recovered                                             bool
	Recovery                                              adminEditorRecoveryView
	AutosaveURL                                           string
	AutosaveEnabled                                       bool
	BrowserRecoveryEnabled                                bool
	AutosaveInterval                                      int
	AutosaveRetention                                     int
	PreviewMinutes                                        int
	SEO                                                   editorSEOView
}

type adminEditorMedia struct {
	Item      media.Item
	SizeLabel string
}

type adminRevisionView struct {
	ID           int64
	Version      int64
	Title        string
	Actor        string
	CreatedLabel string
	CompareURL   string
	Current      bool
}

func (s *Server) renderContentList(writer http.ResponseWriter, request *http.Request, status int, principal auth.Principal, readOnly bool, query content.AdminQuery, result content.AdminList, page int, notice adminNotice) {
	pageSize := max(1, query.Limit)
	pageCount := max(1, (result.Total+pageSize-1)/pageSize)
	rows := make([]adminContentRow, 0, len(result.Items))
	now := time.Now()
	location := site.TimeLocation(s.siteSnapshot(responseContext(writer)).Settings)
	for _, post := range result.Items {
		publicURL := ""
		publicURL = s.contentPath(post)
		rows = append(rows, adminContentRow{
			Post: post, TypeLabel: contentTypeLabel(post.Type), StatusLabel: contentStatusLabel(post.Status),
			StatusTone: contentStatusTone(post.Status), UpdatedLabel: post.UpdatedAt.In(location).Format("2006-01-02 15:04"),
			EditURL:   "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/edit/",
			PublicURL: publicURL, IsPublic: content.PublicAt(post, now),
		})
	}
	statusFilters := make([]adminContentFilter, 0, 7)
	allCount := 0
	for _, count := range result.StatusCounts {
		allCount += count
	}
	statusFilters = append(statusFilters, adminContentFilter{
		Label: "全部", URL: contentListURL(query.Type, "", query.Search, 1), Count: allCount, Active: query.Status == "",
	})
	for _, item := range []struct{ value, label string }{
		{content.StatusPublish, "已发布"}, {content.StatusDraft, "草稿"}, {content.StatusFuture, "定时"},
		{content.StatusPending, "待审"}, {content.StatusPrivate, "私密"}, {content.StatusTrash, "回收站"},
	} {
		statusFilters = append(statusFilters, adminContentFilter{
			Value: item.value, Label: item.label, Count: result.StatusCounts[item.value],
			URL: contentListURL(query.Type, item.value, query.Search, 1), Active: query.Status == item.value,
		})
	}
	typeFilters := []adminContentFilter{
		{Label: "全部类型", Count: allCount, URL: contentListURL("", query.Status, query.Search, 1), Active: query.Type == ""},
		{Value: content.TypePost, Label: "文章", Count: result.TypeCounts[content.TypePost], URL: contentListURL(content.TypePost, query.Status, query.Search, 1), Active: query.Type == content.TypePost},
		{Value: content.TypePage, Label: "页面", Count: result.TypeCounts[content.TypePage], URL: contentListURL(content.TypePage, query.Status, query.Search, 1), Active: query.Type == content.TypePage},
	}
	data := s.baseViewFor(writer, "内容与页面", "统一管理文章、页面、草稿、定时发布与修订", "/admin/content/")
	data.AIProMax.Enabled = !readOnly && principal.Can(auth.CapabilityContentWrite) && principal.Can(auth.CapabilityPluginsManage) && s.aiActive()
	if data.AIProMax.Enabled {
		st, err := s.aiJobs.Snapshot()
		if err == nil {
			r := s.defaultProRules(st.Rules, s.aiSettings.Snapshot())
			for i := range rows {
				row := &rows[i]
				row.AIAction = row.Post.Status != content.StatusTrash && wantsEnrichment(row.Post, r)
				for n := len(st.Jobs) - 1; n >= 0; n-- {
					j := st.Jobs[n]
					if j.Work.PostID == row.Post.ID && j.Work.Kind == "enrich" {
						row.AIState = j.Label()
						if j.Problem != "" {
							row.AIState += " · " + j.Problem
						}
						if !j.Terminal() && j.Work.PostVersion == row.Post.Version {
							row.AIAction = false
						}
						break
					}
				}
			}
		}
	}
	data.BodyClass = "is-admin is-admin-content"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("content", principal, readOnly)
	data.Admin.PostCount = allCount
	data.Admin.Notice = notice.Message
	data.Admin.NoticeTone = notice.Tone
	data.Content = adminContentView{
		Rows: rows, StatusFilters: statusFilters, TypeFilters: typeFilters, Search: query.Search,
		Type: query.Type, Status: query.Status, Total: result.Total, Page: page, Pages: pageCount,
		HasPrevious: page > 1, HasNext: page < pageCount,
		PreviousURL: contentListURL(query.Type, query.Status, query.Search, max(1, page-1)),
		NextURL:     contentListURL(query.Type, query.Status, query.Search, min(pageCount, page+1)),
	}
	access := func() error { return s.contentListAccess(request, query.AuthorID, result.Items, false) }
	s.renderChecked(writer, status, "admin-content", data, request, s.contentBeforeOutput(writer, request, false, access), access)
}

func (s *Server) renderContentEditor(writer http.ResponseWriter, request *http.Request, status int, principal auth.Principal, readOnly bool, post content.Post, input content.PostInput, fieldErrors map[string]string, notice adminNotice, conflict bool, recoverySource *content.Autosave) {
	recovered := recoverySource != nil
	if input.Type == "" {
		input.Type = content.TypePost
	}
	if input.Status == "" {
		input.Status = content.StatusDraft
	}
	if input.CommentStatus == "" {
		input.CommentStatus = content.CommentOpen
	}
	revisions := make([]adminRevisionView, 0)
	mediaItems := make([]adminEditorMedia, 0, 6)
	categories := make([]content.Term, 0)
	availableTags := make([]content.Term, 0)
	settings := s.siteSnapshot(request.Context()).Settings
	location := site.TimeLocation(settings)
	recovery := adminEditorRecoveryView{}
	autosaveURL := ""
	if post.ID > 0 && !readOnly && s.auth.Configured() && canWriteContentObject(principal, post) {
		autosaveURL = "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/autosave/"
		var autosave content.Autosave
		exists := recovered
		if recovered {
			// The form and its recovery card describe the same snapshot. A later
			// read must not authorize already-loaded input with a newer identity.
			autosave = *recoverySource
		} else {
			autosave, exists = s.loadEditorAutosave(request, post, principal)
		}
		if exists && autosaveDiffersFromPost(autosave, post) {
			recovery = adminEditorRecoveryView{
				Available: true, SourceVersion: autosave.SourceVersion,
				UpdatedLabel: autosave.UpdatedAt.In(location).Format("2006-01-02 15:04:05"),
				ExpiresLabel: autosave.ExpiresAt.In(location).Format("01-02 15:04"),
				RecoveryURL:  "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/edit/?recovery=autosave",
				DiscardURL:   autosaveURL,
			}
		}
	}
	if post.ID > 0 {
		items, err := s.repository.ListRevisions(request.Context(), post.ID, settings.Reading.RevisionLimit)
		if err != nil && !errors.Is(err, content.ErrNotFound) {
			slog.Error("list post revisions", "id", post.ID, "error", err)
		} else {
			for _, revision := range items {
				revisions = append(revisions, adminRevisionView{
					ID: revision.ID, Version: revision.Version, Title: revision.Snapshot.Title, Actor: revision.Actor,
					CreatedLabel: revision.CreatedAt.In(location).Format("2006-01-02 15:04"),
					CompareURL:   "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/revisions/compare/?from=" + strconv.FormatInt(revision.ID, 10) + "&to=current",
					Current:      revision.Version == post.Version,
				})
			}
		}
	}
	if result, err := s.media.List(media.Query{Kind: media.KindImage, Limit: 6}); err != nil {
		slog.Error("list recent media for editor", "error", err)
	} else {
		for _, item := range result.Items {
			mediaItems = append(mediaItems, adminEditorMedia{Item: item, SizeLabel: formatBytes(item.SizeBytes)})
		}
	}
	if result, err := s.repository.ListTerms(request.Context(), content.TermQuery{Taxonomy: content.TaxonomyCategory, Limit: 500}); err != nil {
		slog.Error("list categories for editor", "error", err)
	} else {
		categories = result.Items
	}
	if result, err := s.repository.ListTerms(request.Context(), content.TermQuery{Taxonomy: content.TaxonomyTag, Limit: 100}); err != nil {
		slog.Error("list tags for editor", "error", err)
	} else {
		availableTags = result.Items
	}
	selectedCategories := make(map[int64]bool)
	fallbackCategory := ""
	for _, id := range input.CategoryIDs {
		selectedCategories[id] = true
	}
	if len(selectedCategories) == 0 {
		for _, term := range post.Categories {
			selectedCategories[term.ID] = true
		}
	}
	if len(selectedCategories) == 0 && input.Type == content.TypePost {
		for _, term := range categories {
			if strings.EqualFold(term.Name, input.Category) || (input.Category == "" && term.System) {
				selectedCategories[term.ID] = true
				break
			}
		}
		if len(selectedCategories) == 0 {
			if strings.TrimSpace(input.Category) != "" {
				fallbackCategory = input.Category
			} else {
				for _, term := range categories {
					if term.System {
						selectedCategories[term.ID] = true
						break
					}
				}
			}
		}
	}
	publicURL := ""
	if post.ID > 0 {
		publicURL = s.contentPath(post)
	}
	publishedLocal := siteTime(location, input.PublishedAt, publishedLocalLayout)
	publishedSource := "canonical"
	if recovered {
		publishedSource = "autosave"
	}
	if request.Method == http.MethodPost && len(request.PostForm["published_at"]) == 1 {
		// Even an invalid or stale civil time must survive a failed save unchanged.
		publishedLocal = request.PostForm.Get("published_at")
		if request.PostForm.Get("published_source") == "autosave" {
			publishedSource = "autosave"
		}
	}
	title := "新建文章"
	description := "创建草稿并安全推进到发布状态"
	action := "/admin/content/new/"
	if post.ID == 0 && input.Type == content.TypePage {
		title = "新建页面"
		description = "创建稳定页面并安全推进到发布状态"
	} else if post.ID > 0 {
		title = "编辑内容"
		description = "使用乐观锁、修订历史和明确状态流转保护每一次保存"
		action = "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/edit/"
	}
	data := s.baseViewFor(writer, title, description, request.URL.Path)
	data.AIProMax.Enabled = !readOnly && s.aiActive()
	if data.AIProMax.Enabled {
		settings := s.aiSettings.Snapshot()
		for _, provider := range settings.Providers {
			if _, err := s.aiSettings.Resolve(aieditor.Connection{ProviderID: provider.ID}, true); err != nil {
				continue
			}
			for _, model := range provider.Models {
				data.AIProMax.Queue.Models = append(data.AIProMax.Queue.Models, writingModelOption{Value: provider.ID + ":" + model, Label: provider.Name + " · " + model})
			}
		}
	}
	if data.AIProMax.Enabled && input.AIPolicy == "" {
		input.AIPolicy, _ = post.Meta["ai_pro_max"].(string)
	}
	data.BodyClass = "is-admin is-admin-editor"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("content", principal, readOnly)
	data.Admin.FieldErrors = fieldErrors
	data.Admin.Notice = notice.Message
	data.Admin.NoticeTone = notice.Tone
	data.Editor = adminEditorView{
		PostPermalink: settings.Writing.PostPermalink, PagePermalink: settings.Writing.PagePermalink, SlugMode: settings.Writing.SlugMode, PermalinkDate: time.Now().In(location).Format("2006/01/02"),
		BadgeGroups: content.ArticleBadgePresets(), BadgeStyles: content.ArticleBadgeStyles(), BadgeTones: content.ArticleBadgeTones(), BadgeIcons: content.ArticleBadgeIcons(),
		IsNew: post.ID == 0, Prefilled: post.ID == 0 && input.ContentSource != "" && request.Method == http.MethodGet, Post: post, Input: input, FormAction: action,
		PublishedAtLocal: publishedLocal, PublishedSource: publishedSource, PublicURL: publicURL,
		IsPublic: post.ID > 0 && content.PublicAt(post, time.Now()), Revisions: revisions, Media: mediaItems,
		Categories: categories, AvailableTags: availableTags, SelectedCategory: selectedCategories,
		FallbackCategory: fallbackCategory, TagValue: strings.Join(input.TagNames, ", "), Conflict: conflict,
		Recovered: recovered, Recovery: recovery, AutosaveURL: autosaveURL,
		AutosaveEnabled:        settings.Writing.AutosaveEnabled && autosaveURL != "",
		BrowserRecoveryEnabled: settings.Writing.BrowserRecoveryEnabled,
		AutosaveInterval:       settings.Writing.AutosaveIntervalSeconds,
		AutosaveRetention:      settings.Writing.AutosaveRetentionHours,
		PreviewMinutes:         settings.Writing.PreviewTokenMinutes,
		SEO:                    s.editorSEOPreview(input),
	}
	if data.Editor.Input.ArticleBadge == nil {
		badge := content.ArticleBadgeConfig(post)
		data.Editor.Input.ArticleBadge = &badge
	}
	data.Editor.Pinned = post.IsPinned()
	if input.Pinned != nil {
		data.Editor.Pinned = input.Type == content.TypePost && *input.Pinned
	}
	preview := content.Post{Type: input.Type, Status: input.Status, PublishedAt: input.PublishedAt, Meta: map[string]any{"pinned": data.Editor.Pinned, "article_badge": *data.Editor.Input.ArticleBadge}}
	data.Editor.BadgePreview = preview.ArticleBadge()
	access := func() error {
		return s.editorReadAccess(request, post.ID, !readOnly, recoverySource)
	}
	beforeOutput := func() bool {
		if err := access(); err != nil {
			s.editorAccessError(writer, request, err)
			return false
		}
		return true
	}
	s.renderChecked(writer, status, "admin-editor", data, request, beforeOutput, access)
}
