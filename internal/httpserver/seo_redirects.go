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
	"newpress.local/newpress/internal/content"
)

type redirectRow struct {
	content.Redirect
	Target, Title, Status string
	Public                bool
}
type redirectPageView struct {
	Items                []redirectRow
	Current              content.Redirect
	Editing              bool
	Search               string
	Total, Page, Pages   int
	PreviousURL, NextURL string
	Visible, Sitemap     bool
}

func redirectETag(item content.Redirect) string {
	return fmt.Sprintf(`"redirect-%d-v%d"`, item.ID, item.Version)
}

func (s *Server) tryContentRedirect(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if !content.ValidRedirectPath(r.URL.Path) {
		return false
	}
	escaped := strings.ToLower(r.URL.EscapedPath())
	for _, part := range []string{"%2f", "%5c", "%2e", "%25"} {
		if strings.Contains(escaped, part) {
			return false
		}
	}
	target, err := s.repository.ResolveRedirect(r.Context(), r.URL.Path)
	if errors.Is(err, content.ErrNotFound) {
		return false
	}
	if err != nil {
		slog.Error("resolve old content path", "error", err)
		s.renderError(w, 503, "旧链接暂时不可用，请稍后重试")
		return true
	}
	if strings.HasPrefix(target, "/flash/") && !s.moduleEnabled("flash") {
		return false
	}
	if p, e := s.contentAtPublicPath(r.Context(), target); e == nil {
		target = s.contentPath(p)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusMovedPermanently)
	return true
}

func redirectFailure(err error) (int, string, string) {
	switch {
	case errors.Is(err, content.ErrNotFound):
		return 404, "seo.not_found", "旧链接没有找到"
	case errors.Is(err, content.ErrVersionConflict):
		return 412, "seo.version_conflict", "规则已在其他会话中更新，请重新载入后核对"
	case errors.Is(err, content.ErrRedirectConflict):
		return 409, "seo.path_conflict", "这个路径已被内容或其他规则占用"
	case len(content.FieldErrors(err)) > 0:
		return 422, "seo.validation_failed", "旧链接没有通过校验"
	default:
		return 500, "seo.unavailable", "旧链接暂时不可用，请稍后重试"
	}
}
func (s *Server) redirectAPIError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := redirectFailure(err)
	if status == 500 {
		slog.Error("SEO redirect operation", "error", err)
	}
	s.writeAPIError(w, r, status, code, message, map[string]any{"fields": content.FieldErrors(err)})
}
func (s *Server) redirectAPIItem(w http.ResponseWriter, r *http.Request) (content.Redirect, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		s.redirectAPIError(w, r, content.ErrNotFound)
		return content.Redirect{}, false
	}
	item, err := s.repository.GetRedirect(r.Context(), id)
	if err != nil {
		s.redirectAPIError(w, r, err)
		return item, false
	}
	return item, true
}
func (s *Server) redirectPrecondition(w http.ResponseWriter, r *http.Request, item content.Redirect) bool {
	values := r.Header.Values("If-Match")
	if len(values) != 1 || values[0] == "" {
		s.writeAPIError(w, r, 428, "seo.precondition_required", "修改需要当前规则的 If-Match", nil)
		return false
	}
	if !strongETagMatches(values[0], redirectETag(item)) {
		s.redirectAPIError(w, r, content.ErrVersionConflict)
		return false
	}
	return true
}
func redirectDTO(item content.Redirect) any {
	return struct {
		content.Redirect
		ETag string `json:"etag"`
	}{item, redirectETag(item)}
}

func (s *Server) adminRedirectListAPI(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"q": true, "page": true})
	if !ok {
		return
	}
	page, err := positiveQueryInt(values.Get("page"), 1, 334)
	if err != nil {
		s.writeAPIError(w, r, 400, "seo.invalid_query", "页码不正确", nil)
		return
	}
	list, err := s.repository.ListRedirects(r.Context(), content.RedirectQuery{Search: values.Get("q"), Limit: 30, Offset: (page - 1) * 30})
	if err != nil {
		s.redirectAPIError(w, r, err)
		return
	}
	items := []any{}
	for _, item := range list.Items {
		items = append(items, redirectDTO(item))
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": list.Total, "page": page, "pages": max(1, (list.Total+29)/30)})
}
func (s *Server) adminRedirectItemAPI(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	item, ok := s.redirectAPIItem(w, r)
	if !ok {
		return
	}
	w.Header().Set("ETag", redirectETag(item))
	writeJSON(w, 200, redirectDTO(item))
}
func (s *Server) adminRedirectSaveAPI(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var current content.Redirect
	if r.PathValue("id") != "" {
		current, ok = s.redirectAPIItem(w, r)
		if !ok || !s.redirectPrecondition(w, r, current) {
			return
		}
	}
	var input struct {
		Path   apiOptionalString `json:"path"`
		PostID *int64            `json:"post_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &input, 4096, "4 KiB"); !ok {
		return
	}
	if !input.Path.Present || input.PostID == nil {
		s.writeAPIError(w, r, 400, "seo.input_required", "需要 path 和 post_id", nil)
		return
	}
	item, err := s.repository.SaveRedirect(r.Context(), current.ID, content.Mutation{ExpectedVersion: current.Version, Actor: p.Username}, content.RedirectInput{Path: input.Path.Value, PostID: *input.PostID})
	if err != nil {
		s.redirectAPIError(w, r, err)
		return
	}
	status := 200
	if current.ID == 0 {
		status = 201
		w.Header().Set("Location", fmt.Sprintf("/api/v1/admin/redirects/%d", item.ID))
	}
	w.Header().Set("ETag", redirectETag(item))
	writeJSON(w, status, redirectDTO(item))
}
func (s *Server) adminRedirectDeleteAPI(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok || !s.requireEmptyAPIBody(w, r) {
		return
	}
	item, ok := s.redirectAPIItem(w, r)
	if !ok || !s.redirectPrecondition(w, r, item) {
		return
	}
	if err := s.repository.DeleteRedirect(r.Context(), item.ID, content.Mutation{ExpectedVersion: item.Version, Actor: p.Username}); err != nil {
		s.redirectAPIError(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func redirectListURL(search string, page int) string {
	q := url.Values{}
	if search != "" {
		q.Set("q", search)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) == 0 {
		return "/admin/seo/"
	}
	return "/admin/seo/?" + q.Encode()
}
func (s *Server) adminSEO(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"q": true, "page": true, "edit": true, "notice": true})
	if !ok {
		return
	}
	page, err := positiveQueryInt(values.Get("page"), 1, 334)
	if err != nil {
		s.renderError(w, 400, "页码不正确")
		return
	}
	var current content.Redirect
	if edit := values.Get("edit"); edit != "" && edit != "new" {
		id, e := strconv.ParseInt(edit, 10, 64)
		if e != nil || id < 1 {
			s.renderError(w, 404, "旧链接没有找到")
			return
		}
		current, err = s.repository.GetRedirect(r.Context(), id)
		if err != nil {
			status, _, message := redirectFailure(err)
			s.renderError(w, status, message)
			return
		}
	}
	notice := adminNotice{}
	if values.Get("notice") == "saved" {
		notice = adminNotice{"旧链接已保存，目标公开时将跳转到其当前地址。", "success"}
	}
	if values.Get("notice") == "deleted" {
		notice = adminNotice{"旧链接规则已删除。", "success"}
	}
	s.renderSEO(w, r, 200, p, readOnly, values.Get("q"), page, current, values.Get("edit") != "", nil, notice)
}

func (s *Server) renderSEO(w http.ResponseWriter, r *http.Request, status int, p auth.Principal, readOnly bool, search string, page int, current content.Redirect, editing bool, fields map[string]string, notice adminNotice) {
	list, err := s.repository.ListRedirects(r.Context(), content.RedirectQuery{Search: search, Limit: 30, Offset: (page - 1) * 30})
	if err != nil {
		code, _, message := redirectFailure(err)
		s.renderError(w, code, message)
		return
	}
	data := s.baseViewFor(w, "搜索与链接", "整理文章的搜索呈现，承接旧地址，检查站内引用。", "/admin/seo/")
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-seo"
	data.Admin = s.adminView("seo", p, readOnly)
	data.Admin.Notice, data.Admin.NoticeTone, data.Admin.FieldErrors = notice.Message, notice.Tone, fields
	settings := s.siteSnapshot(r.Context()).Settings
	data.Redirects = redirectPageView{Current: current, Editing: editing, Search: search, Total: list.Total, Page: page, Pages: max(1, (list.Total+29)/30), PreviousURL: redirectListURL(search, max(1, page-1)), NextURL: redirectListURL(search, page+1), Visible: settings.SEO.SearchEngineVisible, Sitemap: settings.SEO.SitemapEnabled}
	for _, item := range list.Items {
		post, err := s.repository.GetByID(r.Context(), item.PostID)
		if err != nil {
			s.renderError(w, 503, "目标内容暂时不可用")
			return
		}
		data.Redirects.Items = append(data.Redirects.Items, redirectRow{Redirect: item, Target: s.contentPath(post), Title: post.Title, Status: post.Status, Public: content.PublicAt(post, time.Now())})
	}
	s.render(w, status, "admin-seo", data)
}

func (s *Server) adminSEOSubmit(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if readOnly || !s.auth.Configured() || !s.sameOriginRequest(r) {
		s.renderError(w, 403, "当前请求不能修改旧链接")
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	kind, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/x-www-form-urlencoded" || params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8") {
		s.renderError(w, 415, "需要 UTF-8 表单")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err = r.ParseForm(); err != nil {
		s.renderError(w, 400, "表单无法读取或超过 8 KiB")
		return
	}
	f := r.PostForm
	allowed := map[string]bool{"csrf_token": true, "action": true, "id": true, "version": true, "path": true, "post_id": true}
	for key, values := range f {
		if !allowed[key] || len(values) != 1 {
			s.renderError(w, 400, "表单字段不正确")
			return
		}
	}
	if !s.auth.CheckCSRF(p, f.Get("csrf_token")) {
		s.renderError(w, 403, "CSRF 校验失败")
		return
	}
	id, e1 := strconv.ParseInt(f.Get("id"), 10, 64)
	version, e2 := strconv.ParseInt(f.Get("version"), 10, 64)
	if e1 != nil || e2 != nil || id < 0 || version < 0 || id == 0 && version != 0 || id > 0 && version < 1 {
		s.renderError(w, 400, "规则编号或版本不正确")
		return
	}
	current := content.Redirect{ID: id, Version: version}
	action := f.Get("action")
	if action == "delete" {
		if id < 1 {
			s.renderError(w, 400, "请指定规则")
			return
		}
		err = s.repository.DeleteRedirect(r.Context(), id, content.Mutation{ExpectedVersion: version, Actor: p.Username})
		if err == nil {
			http.Redirect(w, r, "/admin/seo/?notice=deleted", 303)
			return
		}
		status, _, message := redirectFailure(err)
		if status == 412 {
			status = 409
		}
		s.renderError(w, status, message)
		return
	}
	if action != "save" {
		s.renderError(w, 400, "操作不正确")
		return
	}
	current.Path = f.Get("path")
	current.PostID, _ = strconv.ParseInt(f.Get("post_id"), 10, 64)
	_, err = s.repository.SaveRedirect(r.Context(), id, content.Mutation{ExpectedVersion: version, Actor: p.Username}, current.RedirectInput)
	if err == nil {
		http.Redirect(w, r, "/admin/seo/?notice=saved", 303)
		return
	}
	status, _, message := redirectFailure(err)
	if status == 412 {
		status = 409
	}
	if status == 500 {
		slog.Error("save redirect form", "error", err)
	}
	s.renderSEO(w, r, status, p, false, "", 1, current, true, content.FieldErrors(err), adminNotice{message + "。本次输入已保留。", "error"})
}

func (s *Server) adminSEOSourcesAPI(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"q": true, "page": true})
	if !ok {
		return
	}
	page, err := positiveQueryInt(values.Get("page"), 1, 100000)
	if err != nil {
		s.writeAPIError(w, r, 400, "seo.invalid_query", "页码不正确", nil)
		return
	}
	query, err := content.NormalizeAdminQuery(content.AdminQuery{Search: values.Get("q"), Limit: 20, Offset: (page - 1) * 20})
	if err != nil {
		s.writeAPIError(w, r, 400, "seo.invalid_query", "搜索条件不正确", nil)
		return
	}
	list, err := s.repository.ListAdmin(r.Context(), query)
	if err != nil {
		s.redirectAPIError(w, r, err)
		return
	}
	items := []map[string]any{}
	for _, post := range list.Items {
		items = append(items, map[string]any{"id": post.ID, "title": post.Title, "status": post.Status, "type": post.Type})
	}
	writeJSON(w, 200, map[string]any{"items": items, "page": page, "pages": max(1, (list.Total+19)/20), "total": list.Total})
}
