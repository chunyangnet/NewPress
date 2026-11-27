package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

type seriesPageView struct {
	Items                []content.Series
	Current              content.Series
	Chapters             []content.Post
	Editing              bool
	Search, Kind         string
	Total, Page, Pages   int
	PreviousURL, NextURL string
}
type seriesReadingView struct {
	Series         content.Series
	Previous, Next content.Post
	Related        []content.Post
	Position       int
}
type seriesChapterResponse struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status,omitempty"`
	URL     string `json:"url,omitempty"`
	Excerpt string `json:"excerpt,omitempty"`
}
type seriesAdminResponse struct {
	content.Series
	ETag string `json:"etag"`
}
type seriesAPIInput struct {
	Kind        apiOptionalString     `json:"kind"`
	Status      apiOptionalString     `json:"status"`
	Title       apiOptionalString     `json:"title"`
	Slug        apiOptionalString     `json:"slug"`
	Description apiOptionalString     `json:"description"`
	CoverURL    apiOptionalString     `json:"cover_url"`
	PostIDs     apiOptionalInt64Slice `json:"post_ids"`
}
type seriesPublicResponse struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
	URL         string `json:"url"`
	Count       int    `json:"count"`
}

func seriesPublic(item content.Series) seriesPublicResponse {
	return seriesPublicResponse{item.ID, item.Kind, item.Title, item.Slug, item.Description, item.CoverURL, item.URL(), item.Count()}
}
func seriesETag(item content.Series) string {
	return fmt.Sprintf(`"series-%d-v%d"`, item.ID, item.Version)
}
func seriesPageURL(base, kind, search string, page int) string {
	q := url.Values{}
	if kind != "" {
		q.Set("kind", kind)
	}
	if search != "" {
		q.Set("q", search)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) > 0 {
		return base + "?" + q.Encode()
	}
	return base
}

func (s *Server) seriesQuery(w http.ResponseWriter, r *http.Request, admin bool) (content.SeriesQuery, int, bool) {
	allowed := map[string]bool{"q": true, "kind": true, "page": true}
	if admin && r.URL.Path == "/admin/series/" {
		allowed["edit"] = true
	}
	values, ok := s.strictAPIQuery(w, r, allowed)
	if !ok {
		return content.SeriesQuery{}, 0, false
	}
	page, err := positiveQueryInt(values.Get("page"), 1, 100000)
	q, qerr := content.NormalizeSeriesQuery(content.SeriesQuery{Kind: values.Get("kind"), Search: values.Get("q"), Limit: 24, Offset: (page - 1) * 24, PublicOnly: !admin})
	if err != nil || qerr != nil {
		s.writeAPIError(w, r, 400, "series.invalid_query", "专题筛选参数不正确", nil)
		return q, 0, false
	}
	return q, page, true
}

func (s *Server) publicSeriesIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q, page, ok := s.seriesQuery(w, r, false)
	if !ok {
		return
	}
	list, err := s.repository.ListSeries(r.Context(), q)
	if err != nil {
		s.seriesError(w, r, err)
		return
	}
	data := s.baseViewFor(w, "专题与系列", "围绕一个主题，沿着一条线索，读得更深入。", seriesPageURL("/series/", q.Kind, q.Search, page))
	data.BodyClass, data.ActiveNav = "is-series", "explore"
	pages := max(1, (list.Total+23)/24)
	data.Series = seriesPageView{Items: list.Items, Search: q.Search, Kind: q.Kind, Total: list.Total, Page: page, Pages: pages, PreviousURL: seriesPageURL("/series/", q.Kind, q.Search, max(1, page-1)), NextURL: seriesPageURL("/series/", q.Kind, q.Search, page+1)}
	s.render(w, 200, "series", data)
}

func (s *Server) publicSeriesDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	item, posts, err := s.repository.PublishedSeries(r.Context(), r.PathValue("slug"))
	if errors.Is(err, content.ErrNotFound) {
		s.renderError(w, 404, "专题或系列尚未公开")
		return
	}
	if err != nil {
		s.seriesError(w, r, err)
		return
	}
	data := s.baseViewFor(w, item.Title, item.Description, item.URL())
	data.BodyClass, data.ActiveNav = "is-series", "explore"
	if item.CoverURL != "" {
		data.Site.SocialImageURL = s.absolutePublicURL(item.CoverURL)
	}
	data.Series = seriesPageView{Current: item, Chapters: posts}
	s.render(w, 200, "series", data)
}

func (s *Server) adminSeries(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilityTaxonomyManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	q, page, ok := s.seriesQuery(w, r, true)
	if !ok {
		return
	}
	data := s.baseViewFor(w, "专题与系列", "编排文章，建立连续的阅读路径。", "/admin/series/")
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-series"
	data.Admin = s.adminView("series", p, readOnly)
	if edit := r.URL.Query().Get("edit"); edit != "" {
		data.Series.Editing = true
		data.Series.Current = content.Series{SeriesInput: content.SeriesInput{Kind: content.SeriesKindSequence, Status: content.StatusDraft, PostIDs: []int64{}}}
		if edit != "new" {
			id, err := strconv.ParseInt(edit, 10, 64)
			if err != nil || id <= 0 {
				s.renderError(w, 404, "专题没有找到")
				return
			}
			item, err := s.repository.GetSeries(r.Context(), id)
			if errors.Is(err, content.ErrNotFound) {
				s.renderError(w, 404, "专题没有找到")
				return
			}
			if err != nil {
				s.seriesError(w, r, err)
				return
			}
			data.Series.Current = item
			for _, postID := range item.PostIDs {
				post, err := s.repository.GetByID(r.Context(), postID)
				if err != nil {
					s.seriesError(w, r, err)
					return
				}
				data.Series.Chapters = append(data.Series.Chapters, post)
			}
		}
	} else {
		list, err := s.repository.ListSeries(r.Context(), q)
		if err != nil {
			s.seriesError(w, r, err)
			return
		}
		data.Series = seriesPageView{Items: list.Items, Search: q.Search, Kind: q.Kind, Total: list.Total, Page: page, Pages: max(1, (list.Total+23)/24), PreviousURL: seriesPageURL("/admin/series/", q.Kind, q.Search, max(1, page-1)), NextURL: seriesPageURL("/admin/series/", q.Kind, q.Search, page+1)}
	}
	s.render(w, 200, "admin-series", data)
}

func (s *Server) seriesError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, content.ErrNotFound):
		s.writeAPIError(w, r, 404, "series.not_found", "专题或系列没有找到", nil)
	case errors.Is(err, content.ErrVersionConflict):
		s.writeAPIError(w, r, 412, "series.version_conflict", "已被其他请求修改，请重新载入后合并。当前输入仍保留。", nil)
	case errors.Is(err, content.ErrSlugConflict):
		s.writeAPIError(w, r, 409, "series.slug_conflict", "这个访问别名已经被使用，请换一个别名。", nil)
	default:
		if fields := content.FieldErrors(err); len(fields) > 0 {
			s.writeAPIError(w, r, 422, "series.validation_failed", "请检查填写的内容", map[string]any{"fields": fields})
			return
		}
		slog.Error("series request failed", "error", err)
		s.writeAPIError(w, r, 503, "series.unavailable", "暂时无法载入或保存，请稍后重试", nil)
	}
}
func (s *Server) loadSeriesAPI(w http.ResponseWriter, r *http.Request) (content.Series, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.seriesError(w, r, content.ErrNotFound)
		return content.Series{}, false
	}
	item, err := s.repository.GetSeries(r.Context(), id)
	if err != nil {
		s.seriesError(w, r, err)
		return item, false
	}
	return item, true
}
func (s *Server) seriesPrecondition(w http.ResponseWriter, r *http.Request, item content.Series) bool {
	values := r.Header.Values("If-Match")
	if len(values) != 1 || values[0] == "" {
		s.writeAPIError(w, r, 428, "series.precondition_required", "保存或删除需要当前版本的 If-Match", nil)
		return false
	}
	if !strongETagMatches(values[0], seriesETag(item)) {
		s.seriesError(w, r, content.ErrVersionConflict)
		return false
	}
	return true
}
func (s *Server) adminSeriesListAPI(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityTaxonomyManage); !ok {
		return
	}
	q, page, ok := s.seriesQuery(w, r, true)
	if !ok {
		return
	}
	list, err := s.repository.ListSeries(r.Context(), q)
	if err != nil {
		s.seriesError(w, r, err)
		return
	}
	items := []seriesAdminResponse{}
	for _, item := range list.Items {
		items = append(items, seriesAdminResponse{item, seriesETag(item)})
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": list.Total, "page": page, "pages": max(1, (list.Total+23)/24)})
}
func (s *Server) adminSeriesItemAPI(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityTaxonomyManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	item, ok := s.loadSeriesAPI(w, r)
	if !ok {
		return
	}
	w.Header().Set("ETag", seriesETag(item))
	writeJSON(w, 200, seriesAdminResponse{item, seriesETag(item)})
}
func (s *Server) adminSeriesSaveAPI(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityTaxonomyManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var current content.Series
	if r.PathValue("id") != "" {
		current, ok = s.loadSeriesAPI(w, r)
		if !ok || !s.seriesPrecondition(w, r, current) {
			return
		}
	}
	var body seriesAPIInput
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &body, 32<<10, "32 KiB"); !ok {
		return
	}
	if !body.PostIDs.Present {
		s.writeAPIError(w, r, 422, "series.chapters_required", "post_ids 必须是数组，空目录请提交 []", nil)
		return
	}
	input := content.SeriesInput{Kind: body.Kind.Value, Status: body.Status.Value, Title: body.Title.Value, Slug: body.Slug.Value, Description: body.Description.Value, CoverURL: body.CoverURL.Value, PostIDs: body.PostIDs.Value}
	item, err := s.repository.SaveSeries(r.Context(), current.ID, content.Mutation{ExpectedVersion: current.Version, Actor: p.Username}, input)
	if err != nil {
		s.seriesError(w, r, err)
		return
	}
	status := 200
	if current.ID == 0 {
		status = 201
		w.Header().Set("Location", fmt.Sprintf("/api/v1/admin/series/%d", item.ID))
	}
	w.Header().Set("ETag", seriesETag(item))
	writeJSON(w, status, seriesAdminResponse{item, seriesETag(item)})
}
func (s *Server) adminSeriesDeleteAPI(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityTaxonomyManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok || !s.requireEmptyAPIBody(w, r) {
		return
	}
	item, ok := s.loadSeriesAPI(w, r)
	if !ok || !s.seriesPrecondition(w, r, item) {
		return
	}
	if err := s.repository.DeleteSeries(r.Context(), item.ID, content.Mutation{ExpectedVersion: item.Version, Actor: p.Username}); err != nil {
		s.seriesError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) adminSeriesSourcesAPI(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityTaxonomyManage); !ok {
		return
	}
	query, ok := s.strictAPIQuery(w, r, map[string]bool{"q": true, "page": true})
	if !ok {
		return
	}
	page, err := positiveQueryInt(query.Get("page"), 1, 100000)
	if err != nil {
		s.writeAPIError(w, r, 400, "series.invalid_query", "分页参数不正确", nil)
		return
	}
	q, err := content.NormalizeAdminQuery(content.AdminQuery{Type: content.TypePost, Search: query.Get("q"), Limit: 20, Offset: (page - 1) * 20})
	if err != nil {
		s.writeAPIError(w, r, 400, "series.invalid_query", "搜索参数不正确", nil)
		return
	}
	list, err := s.repository.ListAdmin(r.Context(), q)
	if err != nil {
		s.seriesError(w, r, err)
		return
	}
	items := []seriesChapterResponse{}
	for _, post := range list.Items {
		items = append(items, seriesChapterResponse{ID: post.ID, Title: post.Title, Status: post.Status})
	}
	writeJSON(w, 200, map[string]any{"items": items, "page": page, "total": list.Total, "has_more": page*20 < list.Total})
}
func (s *Server) publicSeriesAPI(w http.ResponseWriter, r *http.Request) {
	if !s.siteSettings.Snapshot().Settings.API.PublicPostsEnabled {
		http.NotFound(w, r)
		return
	}
	if slug := r.PathValue("slug"); slug != "" {
		if _, ok := s.strictAPIQuery(w, r, nil); !ok {
			return
		}
		item, posts, err := s.repository.PublishedSeries(r.Context(), slug)
		if err != nil {
			s.seriesError(w, r, err)
			return
		}
		chapters := []seriesChapterResponse{}
		for _, p := range posts {
			chapters = append(chapters, seriesChapterResponse{ID: p.ID, Title: p.Title, URL: s.contentPath(p), Excerpt: p.Excerpt})
		}
		writeJSON(w, 200, map[string]any{"series": seriesPublic(item), "chapters": chapters})
		return
	}
	q, page, ok := s.seriesQuery(w, r, false)
	if !ok {
		return
	}
	list, err := s.repository.ListSeries(r.Context(), q)
	if err != nil {
		s.seriesError(w, r, err)
		return
	}
	items := []seriesPublicResponse{}
	for _, item := range list.Items {
		items = append(items, seriesPublic(item))
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": list.Total, "page": page, "pages": max(1, (list.Total+23)/24)})
}

func (s *Server) readingSeries(ctx context.Context, postID int64) []seriesReadingView {
	if !s.moduleEnabled("series") {
		return nil
	}
	list, err := s.repository.ListSeries(ctx, content.SeriesQuery{PublicOnly: true, PostID: postID, Limit: 4})
	if err != nil {
		slog.Error("load article series", "error", err)
		return nil
	}
	result := []seriesReadingView{}
	for _, item := range list.Items {
		current, posts, err := s.repository.PublishedSeries(ctx, item.Slug)
		if err != nil {
			continue
		}
		view := seriesReadingView{Series: current}
		for i, p := range posts {
			if p.ID == postID {
				view.Position = i + 1
				if i > 0 {
					view.Previous = posts[i-1]
				}
				if i+1 < len(posts) {
					view.Next = posts[i+1]
				}
			}
			if p.ID != postID && len(view.Related) < 3 {
				view.Related = append(view.Related, p)
			}
		}
		if view.Position > 0 {
			result = append(result, view)
		}
	}
	return result
}
