package httpserver

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/site"
)

type adminEditorRecoveryView struct {
	Available     bool
	UpdatedLabel  string
	ExpiresLabel  string
	SourceVersion int64
	RecoveryURL   string
	DiscardURL    string
}

type contentPreviewView struct {
	EditorURL     string
	SavedLabel    string
	ExpiresLabel  string
	SourceVersion int64
	StatusLabel   string
}

type autosaveAPIResponse struct {
	Available     bool   `json:"available"`
	PostID        int64  `json:"post_id"`
	SourceVersion int64  `json:"source_version,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	RecoveryURL   string `json:"recovery_url,omitempty"`
	PreviewURL    string `json:"preview_url,omitempty"`
}

type previewTokenClaims struct {
	Version         int    `json:"v"`
	PostID          int64  `json:"p"`
	OwnerDigest     string `json:"o"`
	SourceVersion   int64  `json:"s"`
	AutosaveUpdated int64  `json:"u"`
	ExpiresUnix     int64  `json:"e"`
}

func newPreviewSigningKey() [32]byte {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		panic(fmt.Sprintf("initialize preview signing key: %v", err))
	}
	return key
}

func autosaveOwnerKey(principal auth.Principal) (string, bool) {
	if principal.UserID > 0 {
		return "user:" + strconv.FormatInt(principal.UserID, 10), true
	}
	username := strings.ToLower(strings.TrimSpace(principal.Username))
	if username == "" {
		return "", false
	}
	digest := sha256.Sum256([]byte(username))
	return "legacy:" + hex.EncodeToString(digest[:]), true
}

func autosaveOwnerDigest(ownerKey string) string {
	digest := sha256.Sum256([]byte(ownerKey))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (s *Server) adminContentAutosaveStatus(writer http.ResponseWriter, request *http.Request) {
	s.previewPrivateHeaders(writer)
	principal, ok := s.requireAutosaveAPIPrincipal(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_autosave_query"})
		return
	}
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "content_not_found"})
		return
	}
	post, ok := s.autosaveAuthorizedPost(writer, request, principal, id)
	if !ok {
		return
	}
	ownerKey, ok := autosaveOwnerKey(principal)
	if !ok {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "autosave_identity_unavailable"})
		return
	}
	autosave, err := s.repository.GetAutosave(request.Context(), post.ID, ownerKey)
	request, ok = s.freshSessionRequest(writer, request)
	if !ok {
		return
	}
	if _, ok = s.requireAutosaveAPIPrincipal(writer, request); !ok {
		return
	}
	if errors.Is(err, content.ErrAutosaveNotFound) {
		writeJSON(writer, http.StatusOK, autosaveAPIResponse{Available: false, PostID: post.ID})
		return
	}
	if err != nil {
		slog.Error("load content autosave status", "post_id", post.ID, "error", err)
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "autosave_unavailable"})
		return
	}
	if autosave.SourceVersion != post.Version {
		writeJSON(writer, http.StatusOK, autosaveAPIResponse{Available: false, PostID: post.ID})
		return
	}
	state, err := s.autosaveReadState(request, post.ID, ownerKey)
	if errors.Is(err, content.ErrAutosaveNotFound) || errors.Is(err, content.ErrNotFound) || err == nil && (state.SourceVersion != autosave.SourceVersion || !state.UpdatedAt.Equal(autosave.UpdatedAt)) {
		writeJSON(writer, http.StatusOK, autosaveAPIResponse{Available: false, PostID: post.ID})
		return
	}
	if err != nil {
		s.autosaveReadError(writer, request, err, false)
		return
	}
	writeJSON(writer, http.StatusOK, s.autosaveResponse(autosave, ownerKey))
}

func (s *Server) adminContentAutosaveSubmit(writer http.ResponseWriter, request *http.Request) {
	s.previewPrivateHeaders(writer)
	principal, form, ok := s.requireContentWrite(writer, request)
	if !ok {
		return
	}
	request, principal, ok = s.refreshAutosaveWrite(writer, request)
	if !ok {
		return
	}
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "content_not_found"})
		return
	}
	post, ok := s.autosaveAuthorizedPost(writer, request, principal, id)
	if !ok {
		return
	}
	ownerKey, ok := autosaveOwnerKey(principal)
	if !ok {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "autosave_identity_unavailable"})
		return
	}
	action := form.Get("action")
	if message := strictAutosaveFormError(form, action); message != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_autosave_form", "field": message})
		return
	}
	version, err := strconv.ParseInt(form.Get("version"), 10, 64)
	if err != nil || version <= 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_content_version"})
		return
	}
	if version != post.Version {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "content_version_conflict"})
		return
	}
	if action == "discard" {
		if !s.recheckAutosaveMutation(writer, request, post) {
			return
		}
		if err := s.repository.DeleteAutosave(request.Context(), post.ID, ownerKey); err != nil && !errors.Is(err, content.ErrNotFound) {
			slog.Error("discard content autosave", "post_id", post.ID, "error", err)
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "autosave_discard_failed"})
			return
		}
		http.Redirect(writer, request, "/admin/content/"+strconv.FormatInt(post.ID, 10)+"/edit/?notice=autosave-discarded", http.StatusSeeOther)
		return
	}
	if action != "autosave" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unknown_autosave_action"})
		return
	}
	now := time.Now()
	settings := s.siteSnapshot(request.Context()).Settings
	input, fields := s.parseEditorInput(request, form, principal, now, post)
	input.AuthorID = post.AuthorID
	input.AuthorName = post.AuthorName
	if len(fields) > 0 {
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{"error": "autosave_validation_failed", "fields": fields, "published_timezone": s.siteLocation(request.Context()).String()})
		return
	}
	snapshot, fields, err := s.buildAutosaveSnapshot(request, post, input, now)
	if err != nil {
		slog.Error("build content autosave", "post_id", post.ID, "error", err)
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "autosave_unavailable"})
		return
	}
	if len(fields) > 0 {
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{"error": "autosave_validation_failed", "fields": fields})
		return
	}
	if !s.recheckAutosaveMutation(writer, request, post) {
		return
	}
	autosave, err := s.repository.SaveAutosave(request.Context(), content.Autosave{
		PostID: post.ID, OwnerKey: ownerKey, SourceVersion: version, Snapshot: snapshot,
		ExpiresAt: now.Add(time.Duration(settings.Writing.AutosaveRetentionHours) * time.Hour),
	})
	if errors.Is(err, content.ErrVersionConflict) {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "content_version_conflict"})
		return
	}
	if validation := content.FieldErrors(err); len(validation) > 0 {
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{"error": "autosave_validation_failed", "fields": validation})
		return
	}
	if err != nil {
		slog.Error("save content autosave", "post_id", post.ID, "error", err)
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "autosave_save_failed"})
		return
	}
	writeJSON(writer, http.StatusOK, s.autosaveResponse(autosave, ownerKey))
}

func strictAutosaveFormError(form url.Values, action string) string {
	allowed := map[string]bool{"csrf_token": true, "version": true, "action": true}
	required := []string{"csrf_token", "version", "action"}
	if action == "autosave" {
		for _, key := range []string{"resources", "pinned", "ai_pro_max", "type", "status", "slug", "title", "excerpt", "content_source", "cover_url", "category", "category_ids", "tags", "comment_status", "published_at", "published_timezone", "published_source", "article_badge_kind", "article_badge_text", "article_badge_style", "article_badge_tone", "article_badge_icon", "article_badge_placement", "article_badge_hide_mobile"} {
			allowed[key] = true
		}
		required = append(required, "type", "status", "slug", "title", "excerpt", "content_source", "cover_url", "tags", "comment_status", "published_at", "published_timezone", "published_source")
	}
	for key, values := range form {
		if !allowed[key] || (key != "category_ids" && len(values) != 1) || (key == "category_ids" && (len(values) < 1 || len(values) > content.MaxCategoriesPerPost)) {
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

func (s *Server) buildAutosaveSnapshot(request *http.Request, canonical content.Post, input content.PostInput, now time.Time) (content.Post, map[string]string, error) {
	snapshot := canonical
	snapshot.Type = input.Type
	snapshot.Status = input.Status
	snapshot.Slug = input.Slug
	snapshot.Title = input.Title
	snapshot.Excerpt = input.Excerpt
	snapshot.ContentSource = input.ContentSource
	snapshot.ContentHTML = input.ContentHTML
	snapshot.CoverURL = input.CoverURL
	snapshot.AuthorID = canonical.AuthorID
	snapshot.AuthorName = canonical.AuthorName
	snapshot.CommentStatus = input.CommentStatus
	snapshot.PublishedAt = input.PublishedAt
	snapshot.UpdatedAt = now
	snapshot.Version = canonical.Version
	snapshot.Categories = nil
	snapshot.Tags = nil
	fields := make(map[string]string)
	if input.Type == content.TypePost {
		for _, id := range input.CategoryIDs {
			term, err := s.repository.GetTermByID(request.Context(), id)
			if errors.Is(err, content.ErrTermNotFound) || (err == nil && term.Taxonomy != content.TaxonomyCategory) {
				fields["category_ids"] = "所选分类不存在或已被删除"
				continue
			}
			if err != nil {
				return content.Post{}, nil, err
			}
			snapshot.Categories = append(snapshot.Categories, term)
		}
		if len(snapshot.Categories) == 0 {
			name := strings.TrimSpace(input.Category)
			if name == "" {
				name = "未分类"
			}
			slug := content.NormalizeTermSlug(name)
			if slug == "" {
				slug = "preview-category"
			}
			snapshot.Categories = []content.Term{{Taxonomy: content.TaxonomyCategory, Slug: slug, Name: name}}
		}
		snapshot.Category = snapshot.Categories[0].Name
		knownTags := make(map[string]content.Term)
		if result, err := s.repository.ListTerms(request.Context(), content.TermQuery{Taxonomy: content.TaxonomyTag, Limit: 500}); err == nil {
			for _, term := range result.Items {
				knownTags[strings.ToLower(term.Name)] = term
			}
		} else {
			slog.Warn("list tags for autosave snapshot", "post_id", canonical.ID, "error", err)
		}
		for index, name := range input.TagNames {
			if term, exists := knownTags[strings.ToLower(name)]; exists {
				snapshot.Tags = append(snapshot.Tags, term)
				continue
			}
			slug := content.NormalizeTermSlug(name)
			if slug == "" {
				slug = "preview-tag-" + strconv.Itoa(index+1)
			}
			snapshot.Tags = append(snapshot.Tags, content.Term{Taxonomy: content.TaxonomyTag, Slug: slug, Name: name})
		}
	} else {
		snapshot.Category = ""
	}
	snapshot.Meta = map[string]any{"category": snapshot.Category, "author_name": snapshot.AuthorName}
	if canonical.IsPinned() {
		snapshot.Meta["pinned"] = true
	}
	if badge := content.ArticleBadgeConfig(canonical); badge.Kind != "" {
		snapshot.Meta["article_badge"] = badge
	}
	if policy, ok := canonical.Meta["ai_pro_max"].(string); ok {
		snapshot.Meta["ai_pro_max"] = policy
	}
	if input.Resources == nil {
		if value, ok := canonical.Meta["article_resources"]; ok {
			snapshot.Meta["article_resources"] = value
		}
	}
	content.ApplyAIMetadata(snapshot.Meta, input)
	return snapshot, fields, nil
}

func (s *Server) autosaveResponse(autosave content.Autosave, ownerKey string) autosaveAPIResponse {
	return autosaveAPIResponse{
		Available: true, PostID: autosave.PostID, SourceVersion: autosave.SourceVersion,
		UpdatedAt: autosave.UpdatedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: autosave.ExpiresAt.UTC().Format(time.RFC3339Nano),
		RecoveryURL: "/admin/content/" + strconv.FormatInt(autosave.PostID, 10) + "/edit/?recovery=autosave",
		PreviewURL:  s.autosavePreviewURL(autosave, ownerKey),
	}
}

func (s *Server) autosavePreviewURL(autosave content.Autosave, ownerKey string) string {
	minutes := s.siteSettings.Snapshot().Settings.Writing.PreviewTokenMinutes
	claims := previewTokenClaims{
		Version: 1, PostID: autosave.PostID, OwnerDigest: autosaveOwnerDigest(ownerKey),
		SourceVersion: autosave.SourceVersion, AutosaveUpdated: autosave.UpdatedAt.UnixNano(),
		ExpiresUnix: time.Now().Add(time.Duration(minutes) * time.Minute).Unix(),
	}
	payload, _ := json.Marshal(claims)
	mac := hmac.New(sha256.New, s.previewSigningKey[:])
	_, _ = mac.Write(payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return "/admin/content/" + strconv.FormatInt(autosave.PostID, 10) + "/preview/?token=" + token
}

func (s *Server) verifyPreviewToken(token string, now time.Time) (previewTokenClaims, bool) {
	if len(token) < 40 || len(token) > 2048 || strings.Count(token, ".") != 1 {
		return previewTokenClaims{}, false
	}
	parts := strings.SplitN(token, ".", 2)
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) == 0 || len(payload) > 1024 {
		return previewTokenClaims{}, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return previewTokenClaims{}, false
	}
	mac := hmac.New(sha256.New, s.previewSigningKey[:])
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return previewTokenClaims{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var claims previewTokenClaims
	if err := decoder.Decode(&claims); err != nil {
		return previewTokenClaims{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return previewTokenClaims{}, false
	}
	if claims.Version != 1 || claims.PostID <= 0 || claims.SourceVersion <= 0 || claims.AutosaveUpdated <= 0 ||
		claims.OwnerDigest == "" || claims.ExpiresUnix <= now.Unix() {
		return previewTokenClaims{}, false
	}
	return claims, true
}

func (s *Server) adminContentPreview(writer http.ResponseWriter, request *http.Request) {
	s.previewPrivateHeaders(writer)
	principal, ok := s.requireAutosavePagePrincipal(writer, request)
	if !ok {
		return
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["token"]) != 1 || query.Get("token") == "" {
		s.renderPreviewError(writer, http.StatusBadRequest, "预览链接格式不正确")
		return
	}
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		s.renderPreviewError(writer, http.StatusNotFound, "预览内容没有找到")
		return
	}
	post, err := s.repository.GetByID(request.Context(), id)
	if errors.Is(err, content.ErrNotFound) {
		s.renderPreviewError(writer, http.StatusNotFound, "预览内容没有找到")
		return
	}
	if err != nil {
		slog.Error("load preview content", "post_id", id, "error", err)
		s.renderPreviewError(writer, http.StatusInternalServerError, "预览暂时不可用")
		return
	}
	if !canWriteContentObject(principal, post) {
		s.renderPreviewError(writer, http.StatusForbidden, "当前账号不能预览其他作者的恢复点")
		return
	}
	ownerKey, ok := autosaveOwnerKey(principal)
	if !ok {
		s.renderPreviewError(writer, http.StatusForbidden, "当前身份不能创建安全预览")
		return
	}
	now := time.Now()
	claims, ok := s.verifyPreviewToken(query.Get("token"), now)
	if !ok || claims.PostID != post.ID || claims.OwnerDigest != autosaveOwnerDigest(ownerKey) || claims.SourceVersion != post.Version {
		s.renderPreviewError(writer, http.StatusForbidden, "预览链接无效或已经过期")
		return
	}
	autosave, err := s.repository.GetAutosave(request.Context(), post.ID, ownerKey)
	if errors.Is(err, content.ErrAutosaveNotFound) {
		s.renderPreviewError(writer, http.StatusGone, "恢复点已被替换、放弃或正式保存")
		return
	}
	if err != nil {
		slog.Error("load preview autosave", "post_id", post.ID, "error", err)
		s.renderPreviewError(writer, http.StatusInternalServerError, "预览暂时不可用")
		return
	}
	if autosave.SourceVersion != claims.SourceVersion || autosave.UpdatedAt.UnixNano() != claims.AutosaveUpdated || !autosave.ExpiresAt.After(now) {
		s.renderPreviewError(writer, http.StatusGone, "恢复点已更新，请从编辑器重新打开预览")
		return
	}
	access := func() error { return s.contentPreviewAccess(request, claims, ownerKey) }
	beforeOutput := func() bool {
		if err := access(); err != nil {
			s.autosaveReadError(writer, request, err, true)
			return false
		}
		return true
	}
	if !beforeOutput() {
		return
	}
	previewPost := autosave.Snapshot
	if strings.TrimSpace(previewPost.Title) == "" {
		previewPost.Title = "（无标题预览）"
	}
	if previewPost.PublishedAt.IsZero() {
		previewPost.PublishedAt = autosave.UpdatedAt
	}
	previewPost.UpdatedAt = autosave.UpdatedAt
	data := s.baseViewFor(writer, previewPost.Title, previewPost.Excerpt, "")
	data.IsPreview = true
	data.Post = previewPost
	data.Canonical = ""
	data.Site.SocialImageURL = ""
	data.BodyClass = "is-single is-content-preview"
	if previewPost.Type == content.TypePage {
		data.BodyClass += " is-page"
	}
	location := site.TimeLocation(s.siteSnapshot(request.Context()).Settings)
	data.Preview = contentPreviewView{
		EditorURL:     "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/edit/",
		SavedLabel:    autosave.UpdatedAt.In(location).Format("2006-01-02 15:04:05"),
		ExpiresLabel:  time.Unix(claims.ExpiresUnix, 0).In(location).Format("15:04"),
		SourceVersion: autosave.SourceVersion, StatusLabel: contentStatusLabel(previewPost.Status),
	}
	template := "single"
	if previewPost.Type == content.TypePage {
		template = "page"
	}
	s.renderChecked(writer, http.StatusOK, template, data, request, beforeOutput, access)
}

func (s *Server) previewPrivateHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store, max-age=0")
	writer.Header().Set("Pragma", "no-cache")
	writer.Header().Set("Expires", "0")
	writer.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Add("Vary", "Cookie")
}

func (s *Server) requireAutosaveAPIPrincipal(writer http.ResponseWriter, request *http.Request) (auth.Principal, bool) {
	if !s.auth.Configured() {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "not_found"})
		return auth.Principal{}, false
	}
	principal, ok := s.currentAdmin(request)
	if !ok {
		writer.Header().Set("WWW-Authenticate", `NewPressSession realm="admin"`)
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "authentication_required"})
		return auth.Principal{}, false
	}
	if !principal.Can(auth.CapabilityAdminAccess) || !principalCanAny(principal, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "capability_required"})
		return auth.Principal{}, false
	}
	return principal, true
}

func (s *Server) requireAutosavePagePrincipal(writer http.ResponseWriter, request *http.Request) (auth.Principal, bool) {
	if !s.auth.Configured() {
		s.renderPreviewError(writer, http.StatusNotFound, "预览页面没有找到")
		return auth.Principal{}, false
	}
	principal, ok := s.currentAdmin(request)
	if !ok {
		http.Redirect(writer, request, "/admin/login/?next="+url.QueryEscape(request.URL.RequestURI()), http.StatusSeeOther)
		return auth.Principal{}, false
	}
	if !principal.Can(auth.CapabilityAdminAccess) || !principalCanAny(principal, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn) {
		s.renderPreviewError(writer, http.StatusForbidden, "当前账号没有安全预览权限")
		return auth.Principal{}, false
	}
	return principal, true
}

func (s *Server) autosaveAuthorizedPost(writer http.ResponseWriter, request *http.Request, principal auth.Principal, id int64) (content.Post, bool) {
	post, err := s.repository.GetByID(request.Context(), id)
	if errors.Is(err, content.ErrNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "content_not_found"})
		return content.Post{}, false
	}
	if err != nil {
		slog.Error("load autosave content", "post_id", id, "error", err)
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "content_unavailable"})
		return content.Post{}, false
	}
	if !canWriteContentObject(principal, post) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "content_object_forbidden"})
		return content.Post{}, false
	}
	return post, true
}

func (s *Server) renderPreviewError(writer http.ResponseWriter, status int, message string) {
	data := s.baseViewFor(writer, http.StatusText(status), message, "")
	data.IsPreview = true
	data.Canonical = ""
	data.Site.SocialImageURL = ""
	data.Description = message
	data.BodyClass = "is-error is-content-preview"
	s.render(writer, status, "404", data)
}

func (s *Server) loadEditorAutosave(request *http.Request, post content.Post, principal auth.Principal) (content.Autosave, bool) {
	ownerKey, ok := autosaveOwnerKey(principal)
	if !ok || post.ID <= 0 {
		return content.Autosave{}, false
	}
	autosave, err := s.repository.GetAutosave(request.Context(), post.ID, ownerKey)
	if errors.Is(err, content.ErrAutosaveNotFound) {
		return content.Autosave{}, false
	}
	if err != nil {
		slog.Error("load editor autosave", "post_id", post.ID, "error", err)
		return content.Autosave{}, false
	}
	return autosave, autosave.SourceVersion == post.Version
}

func autosaveDiffersFromPost(autosave content.Autosave, post content.Post) bool {
	left, right := autosave.Snapshot, post
	policy := func(p content.Post) string {
		v, _ := p.Meta["ai_pro_max"].(string)
		if v == "" {
			return "inherit"
		}
		return v
	}
	if policy(left) != policy(right) {
		return true
	}
	if left.Type != right.Type || left.Status != right.Status || left.Slug != right.Slug || left.Title != right.Title ||
		left.Excerpt != right.Excerpt || left.ContentSource != right.ContentSource || left.CoverURL != right.CoverURL ||
		left.CommentStatus != right.CommentStatus || !left.PublishedAt.Equal(right.PublishedAt) {
		return true
	}
	return !sameAutosaveTerms(left.Categories, right.Categories) || !sameAutosaveTerms(left.Tags, right.Tags)
}

func sameAutosaveTerms(left, right []content.Term) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].Taxonomy != right[index].Taxonomy || left[index].Name != right[index].Name {
			return false
		}
	}
	return true
}
