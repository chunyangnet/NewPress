package httpserver

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
)

func (s *Server) requireContentWrite(writer http.ResponseWriter, request *http.Request) (auth.Principal, url.Values, bool) {
	return s.requireContentWriteLimit(writer, request, 3<<20, "content_form_too_large")
}

func (s *Server) requireContentWriteLimit(writer http.ResponseWriter, request *http.Request, maxBytes int64, tooLargeError string) (auth.Principal, url.Values, bool) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
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
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/x-www-form-urlencoded") {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "form_content_type_required"})
		return auth.Principal{}, nil, false
	}
	if charset, exists := parameters["charset"]; exists && !strings.EqualFold(charset, "utf-8") {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "form_utf8_required"})
		return auth.Principal{}, nil, false
	}
	if maxBytes <= 0 {
		maxBytes = 3 << 20
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxBytes)
	if err := request.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			if tooLargeError == "" {
				tooLargeError = "content_form_too_large"
			}
			writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]string{"error": tooLargeError})
			return auth.Principal{}, nil, false
		}
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_content_form"})
		return auth.Principal{}, nil, false
	}
	form := request.PostForm
	if len(form["csrf_token"]) != 1 || !s.auth.CheckCSRF(principal, form.Get("csrf_token")) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return auth.Principal{}, nil, false
	}
	return principal, form, true
}

func strictBulkContentFormError(form url.Values) string {
	requiredSingles := map[string]bool{
		"csrf_token": true, "action": true, "return_type": true, "return_status": true,
		"return_q": true, "return_page": true,
	}
	for key, values := range form {
		if key == "selection" {
			if len(values) < 1 || len(values) > content.MaxBulkMutations {
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
	if len(form["selection"]) < 1 || len(form["selection"]) > content.MaxBulkMutations {
		return "selection"
	}
	return ""
}

func bulkContentStatusChange(form url.Values, principal auth.Principal) (content.BulkStatusChange, error) {
	status := ""
	switch form.Get("action") {
	case "draft":
		status = content.StatusDraft
	case "pending":
		status = content.StatusPending
	case "publish":
		status = content.StatusPublish
	case "private":
		status = content.StatusPrivate
	case "trash", "delete":
		status = content.StatusTrash
	default:
		return content.BulkStatusChange{}, errors.New("unknown bulk content action")
	}
	change := content.BulkStatusChange{Status: status, Actor: principal.Username, AuthorID: contentAuthorScope(principal)}
	change.Delete = form.Get("action") == "delete"
	for _, raw := range form["selection"] {
		if len(raw) > 48 {
			return content.BulkStatusChange{}, errors.New("bulk content selection is too long")
		}
		rawID, rawVersion, found := strings.Cut(raw, ":")
		if !found || rawID == "" || rawVersion == "" || strings.Contains(rawVersion, ":") {
			return content.BulkStatusChange{}, errors.New("invalid bulk content selection pair")
		}
		id, idErr := strconv.ParseInt(rawID, 10, 64)
		version, versionErr := strconv.ParseInt(rawVersion, 10, 64)
		if idErr != nil || versionErr != nil {
			return content.BulkStatusChange{}, errors.New("invalid bulk content selection number")
		}
		change.Items = append(change.Items, content.BulkMutation{ID: id, ExpectedVersion: version})
	}
	normalized, fields := content.NormalizeBulkStatusChange(change)
	if len(fields) > 0 {
		return content.BulkStatusChange{}, &content.ValidationError{Fields: fields}
	}
	return normalized, nil
}

func bulkContentReturnQuery(form url.Values) (content.AdminQuery, int, error) {
	page, err := positiveQueryInt(form.Get("return_page"), 1, 100000)
	if err != nil {
		return content.AdminQuery{}, 0, err
	}
	query, err := content.NormalizeAdminQuery(content.AdminQuery{
		Type: form.Get("return_type"), Status: form.Get("return_status"), Search: form.Get("return_q"), Limit: 1,
	})
	if err != nil {
		return content.AdminQuery{}, 0, err
	}
	return query, page, nil
}

func contentAuthorScope(principal auth.Principal) int64 {
	if principal.UserID > 0 && principal.Can(auth.CapabilityContentWriteOwn) && !principal.Can(auth.CapabilityContentWrite) {
		return principal.UserID
	}
	return 0
}

func canCreateContent(principal auth.Principal) bool {
	return principal.Can(auth.CapabilityContentWrite) || (principal.UserID > 0 && principal.Can(auth.CapabilityContentWriteOwn))
}

func canReadContentObject(principal auth.Principal, post content.Post) bool {
	scope := contentAuthorScope(principal)
	return scope == 0 || post.AuthorID == scope
}

func canWriteContentObject(principal auth.Principal, post content.Post) bool {
	if post.Type == content.TypeFlash {
		return false
	}
	if principal.Can(auth.CapabilityContentWrite) {
		return true
	}
	return principal.UserID > 0 && principal.Can(auth.CapabilityContentWriteOwn) && post.AuthorID == principal.UserID
}

// Publish from the AI editor commits the reviewed text and its per-post switch in one version.
func (s *Server) parseEditorMutation(r *http.Request, form url.Values, p auth.Principal, current content.Post) (content.PostInput, map[string]string) {
	now := time.Now()
	input, fields := s.parseEditorInput(r, form, p, now, current)
	if current.ID != 0 {
		input.AuthorID, input.AuthorName = current.AuthorID, current.AuthorName
	}
	if form.Get("action") == "publish" {
		input.Status = content.StatusPublish
		input.PublishedAt = now.UTC()
	}
	return input, fields
}

func parseContentInput(form url.Values, principal auth.Principal, now time.Time, location *time.Location, originals ...time.Time) (content.PostInput, map[string]string) {
	input := content.PostInput{
		AuthorID: principal.UserID,
		AIPolicy: form.Get("ai_pro_max"), Type: form.Get("type"), Status: form.Get("status"), Slug: form.Get("slug"),
		Title: form.Get("title"), Excerpt: form.Get("excerpt"), ContentSource: form.Get("content_source"),
		CoverURL: form.Get("cover_url"), Category: form.Get("category"), CommentStatus: form.Get("comment_status"),
		TagNames:   []string{form.Get("tags")},
		AuthorName: principal.Label(), Actor: principal.Username,
	}
	fields := make(map[string]string)
	if form.Has("resources") {
		cfg, err := resources.Decode([]byte(form.Get("resources")))
		if err != nil {
			fields["resources"] = "资源配置无效，请检查解锁规则和交付内容"
		} else {
			input.Resources = &cfg
		}
	}
	if form.Has("pinned") {
		pinned := form.Get("pinned") == "true"
		input.Pinned = &pinned
		if form.Get("pinned") != "true" && form.Get("pinned") != "false" {
			fields["pinned"] = "请选择有效的置顶状态"
		}
	}
	if form.Has("article_badge_kind") {
		input.ArticleBadge = &content.ArticleBadge{
			Kind: form.Get("article_badge_kind"), Text: form.Get("article_badge_text"),
			Style: form.Get("article_badge_style"), Tone: form.Get("article_badge_tone"), Icon: form.Get("article_badge_icon"),
			Placement: form.Get("article_badge_placement"), HideOnMobile: form.Get("article_badge_hide_mobile") == "true",
		}
		if form.Has("article_badge_hide_mobile") && form.Get("article_badge_hide_mobile") != "true" && form.Get("article_badge_hide_mobile") != "false" {
			fields["article_badge_hide_mobile"] = "移动端显示选项无效"
		}
	}
	if values := form["category_ids"]; len(values) > content.MaxCategoriesPerPost {
		fields["category_ids"] = "选择的分类数量超过上限"
	} else {
		seen := make(map[int64]bool, len(values))
		for _, raw := range values {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 || seen[id] {
				fields["category_ids"] = "分类选择包含无效或重复编号"
				break
			}
			seen[id] = true
			input.CategoryIDs = append(input.CategoryIDs, id)
		}
	}
	if location == nil {
		location = time.UTC
	}
	if raw := strings.TrimSpace(form.Get("published_at")); raw != "" {
		if len(originals) > 0 && !originals[0].IsZero() && raw == originals[0].In(location).Format(publishedLocalLayout) {
			input.PublishedAt = originals[0]
		} else if parsed, err := parsePublishedLocal(raw, location); err != nil {
			fields["published_at"] = err.Error()
		} else {
			input.PublishedAt = parsed
		}
	}
	if form.Get("published_timezone") != location.String() {
		fields["published_at"] = "站点时区已变化或这份编辑缺少时区来源；输入已保留，请按当前 " + location.String() + " 核对后重新保存"
	}
	normalized, validationFields := content.NormalizePostInput(input, now)
	if normalized.Type == content.TypeFlash {
		fields["type"] = "请使用独立快报管理入口"
	}
	for key, value := range validationFields {
		if fields[key] == "" {
			fields[key] = value
		}
	}
	return normalized, fields
}

func inputFromPost(post content.Post) content.PostInput {
	resourceConfig := resources.FromMeta(post.Meta)
	badge := content.ArticleBadgeConfig(post)
	pinned := post.IsPinned()
	input := content.PostInput{
		Pinned:       &pinned,
		ArticleBadge: &badge,
		Resources:    &resourceConfig,
		AuthorID:     post.AuthorID,
		Type:         post.Type, Status: post.Status, Slug: post.Slug, Title: post.Title, Excerpt: post.Excerpt,
		ContentSource: post.ContentSource, ContentHTML: post.ContentHTML, CoverURL: post.CoverURL,
		AuthorName: post.AuthorName, Category: post.Category, CommentStatus: post.CommentStatus,
		PublishedAt: post.PublishedAt,
	}
	input.AIPolicy, _ = post.Meta["ai_pro_max"].(string)
	for _, term := range post.Categories {
		input.CategoryIDs = append(input.CategoryIDs, term.ID)
	}
	for _, term := range post.Tags {
		input.TagNames = append(input.TagNames, term.Name)
	}
	return input
}

func strictFormError(form url.Values) string {
	allowed := map[string]bool{
		"resources":          true,
		"pinned":             true,
		"article_badge_kind": true, "article_badge_text": true, "article_badge_style": true, "article_badge_tone": true,
		"article_badge_icon": true, "article_badge_placement": true, "article_badge_hide_mobile": true,
		"ai_pro_max": true, "csrf_token": true, "version": true, "action": true, "revision_id": true,
		"type": true, "status": true, "slug": true, "title": true, "excerpt": true,
		"content_source": true, "cover_url": true, "category": true, "category_ids": true, "tags": true, "comment_status": true,
		"published_at": true, "published_timezone": true, "published_source": true,
	}
	for key, values := range form {
		if !allowed[key] || (key != "category_ids" && len(values) != 1) || (key == "category_ids" && (len(values) < 1 || len(values) > content.MaxCategoriesPerPost)) {
			return key
		}
	}
	return ""
}
