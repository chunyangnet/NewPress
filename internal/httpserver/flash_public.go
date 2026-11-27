package httpserver

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

type flashItemView struct {
	ID                                                                                int64
	Version                                                                           int64
	Title, Body, HTML, URL, Slug, Status, StatusLabel, Day, Time, PublishedAt, Author string
	SourceName, SourceURL, ReviewNote                                                 string
	Important, Comments                                                               bool
}
type flashPageView struct {
	UserID                                              int64
	Items                                               []flashItemView
	Current                                             flashItemView
	Total                                               int
	Query, Filter, PreviousURL, NextURL, CSRF, Endpoint string
	Authenticated, Admin, Editing                       bool
}

func (s *Server) flashItem(r *http.Request, p content.Post, private bool) flashItemView {
	f := content.FlashMetadata(p)
	at := p.PublishedAt.In(s.siteLocation(r.Context()))
	v := flashItemView{ID: p.ID, Version: p.Version, Title: p.Title, HTML: p.ContentHTML, URL: content.PublicPath(p), Slug: p.Slug, Status: p.Status, Author: p.AuthorName, SourceName: f.SourceName, SourceURL: f.SourceURL, Important: f.Important, Comments: p.CommentStatus == content.CommentOpen}
	v.StatusLabel = map[string]string{"draft": "草稿", "pending": "待审核", "publish": "已发布", "future": "定时发布", "trash": "回收站", "private": "私密"}[p.Status]
	if !p.PublishedAt.IsZero() {
		v.Day = at.Format("2006年01月02日")
		v.Time = at.Format("15:04")
		v.PublishedAt = at.Format("2006-01-02T15:04")
	}
	if private {
		v.Body = p.ContentSource
		v.ReviewNote = f.ReviewNote
	}
	return v
}
func (s *Server) registerFlashRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /flash/{$}", s.flashList)
	mux.HandleFunc("GET /flash/submit/{$}", s.flashEditor)
	mux.HandleFunc("GET /flash/feed.xml", s.flashFeed)
	mux.HandleFunc("GET /flash/{slug}/{$}", s.flashSingle)
	mux.HandleFunc("POST /flash/{slug}/comments", s.publicCommentCreate)
	mux.HandleFunc("GET /api/v1/flash/{slug}/comments", s.publicCommentsAPI)
	mux.HandleFunc("POST /api/v1/flash/{slug}/comments", s.publicCommentCreateAPI)
	mux.HandleFunc("POST /api/v1/flash/{slug}/comments/receipt", s.publicCommentReceiptAPI)
	mux.HandleFunc("GET /admin/flash/{$}", s.flashEditor)
	mux.HandleFunc("POST /api/v1/flash/submissions", s.flashWrite)
	mux.HandleFunc("POST /api/v1/admin/flash", s.flashWrite)
}
func (s *Server) flashList(w http.ResponseWriter, r *http.Request) {
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"q": true, "filter": true, "page": true})
	if !ok {
		return
	}
	page, e := positiveQueryInt(q.Get("page"), 1, 100000)
	if e != nil || (q.Get("filter") != "" && q.Get("filter") != "important") {
		s.renderError(w, 400, "快报筛选参数不正确")
		return
	}
	query := content.PublishedQuery{Type: content.TypeFlash, Search: q.Get("q"), Important: q.Get("filter") == "important", Limit: 20, Offset: (page - 1) * 20}
	query, e = content.NormalizePublishedQuery(query)
	if e != nil {
		s.renderError(w, 400, "搜索内容不正确")
		return
	}
	result, e := s.repository.SearchPublished(r.Context(), query)
	if e != nil {
		s.flashError(w, r, e)
		return
	}
	link := func(n int) string {
		v := url.Values{"q": {query.Search}, "filter": {q.Get("filter")}}
		if n > 1 {
			v.Set("page", strconv.Itoa(n))
		}
		return "/flash/?" + v.Encode()
	}
	pages := max(1, (result.Total+19)/20)
	if page > pages {
		http.Redirect(w, r, link(pages), 302)
		return
	}
	data := s.baseViewFor(w, "快报", "及时阅读短资讯，查看来源并参与讨论。", "/flash/")
	data.BodyClass, data.ActiveNav = "is-flash", "flash"
	data.Flash = flashPageView{Total: result.Total, Query: query.Search, Filter: q.Get("filter")}
	for _, p := range result.Items {
		data.Flash.Items = append(data.Flash.Items, s.flashItem(r, p, false))
	}
	if page > 1 {
		data.Flash.PreviousURL = link(page - 1)
	}
	if page < pages {
		data.Flash.NextURL = link(page + 1)
	}
	s.render(w, 200, "flash", data)
}
func (s *Server) flashSingle(w http.ResponseWriter, r *http.Request) {
	p, e := s.findFlash(r, r.PathValue("slug"))
	if errors.Is(e, content.ErrNotFound) && s.tryContentRedirect(w, r) {
		return
	}
	if e != nil {
		s.flashError(w, r, e)
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"comments_page": true})
	if !ok {
		return
	}
	page, e := positiveQueryInt(q.Get("comments_page"), 1, 100000)
	if e != nil {
		s.renderError(w, 400, "评论分页不正确")
		return
	}
	s.renderFlashSingle(w, r, 200, p, page, publicCommentForm{}, nil, adminNotice{})
}
func (s *Server) findFlash(r *http.Request, slug string) (content.Post, error) {
	if !s.moduleEnabled("flash") || !content.ValidSlug(slug) {
		return content.Post{}, content.ErrNotFound
	}
	return s.repository.GetPublishedFlashBySlug(r.Context(), slug)
}
func (s *Server) renderFlashSingle(w http.ResponseWriter, r *http.Request, status int, p content.Post, page int, form publicCommentForm, fields map[string]string, notice adminNotice) {
	comments, e := s.publicCommentsView(r, p, page, form, fields, notice)
	if e != nil {
		s.flashError(w, r, e)
		return
	}
	data := s.baseViewFor(w, p.Title, p.Excerpt, content.PublicPath(p))
	data.BodyClass, data.ActiveNav = "is-flash", "flash"
	data.Post = p
	data.Comments = comments
	data.Flash.Current = s.flashItem(r, p, false)
	s.render(w, status, "flash-single", data)
}
func (s *Server) flashEditor(w http.ResponseWriter, r *http.Request) {
	admin := r.URL.Path == "/admin/flash/"
	p, logged := s.currentAdmin(r)
	if admin {
		var ok bool
		p, _, ok = s.requireAdminPageCapability(w, r, auth.CapabilityContentWrite)
		if !ok {
			return
		}
		logged = true
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"edit": true, "status": true, "q": true, "page": true})
	if !ok {
		return
	}
	data := s.baseViewFor(w, "我的快报", "保存草稿，提交审核并查看处理结果。", "")
	data.BodyClass = "is-flash"
	data.Site.Robots = "noindex, nofollow"
	data.Flash = flashPageView{Authenticated: logged, Admin: admin, Endpoint: "/api/v1/flash/submissions", Current: flashItemView{Comments: true}}
	if admin {
		data.IsAdmin = true
		data.Admin = s.adminView("flash", p, false)
		data.PageTitle = "快报管理"
		data.Flash.Endpoint = "/api/v1/admin/flash"
	}
	if logged {
		data.Flash.CSRF = p.CSRFToken
		data.Flash.UserID = p.UserID
		page, e := positiveQueryInt(q.Get("page"), 1, 100000)
		if e != nil {
			s.renderError(w, 400, "页码不正确")
			return
		}
		query := content.AdminQuery{Type: content.TypeFlash, Status: q.Get("status"), Search: q.Get("q"), Limit: 20, Offset: (page - 1) * 20}
		if !admin {
			query.AuthorID = p.UserID
		}
		query, e = content.NormalizeAdminQuery(query)
		if e != nil {
			s.renderError(w, 400, "筛选参数不正确")
			return
		}
		result, e := s.repository.ListAdmin(r.Context(), query)
		if e != nil {
			s.flashError(w, r, e)
			return
		}
		data.Flash.Total = result.Total
		data.Flash.Query = query.Search
		data.Flash.Filter = query.Status
		for _, post := range result.Items {
			data.Flash.Items = append(data.Flash.Items, s.flashItem(r, post, true))
		}
		link := func(n int) string {
			v := url.Values{"page": {strconv.Itoa(n)}, "q": {query.Search}, "status": {query.Status}}
			return r.URL.Path + "?" + v.Encode()
		}
		if page > 1 {
			data.Flash.PreviousURL = link(page - 1)
		}
		if page*20 < result.Total {
			data.Flash.NextURL = link(page + 1)
		}
		if edit := q.Get("edit"); edit != "" {
			data.Flash.Editing = true
			if edit != "new" {
				id, e := strconv.ParseInt(edit, 10, 64)
				if e != nil || id < 1 {
					s.renderError(w, 400, "快报编号不正确")
					return
				}
				post, e := s.repository.GetByID(r.Context(), id)
				if e != nil {
					s.flashError(w, r, e)
					return
				}
				if post.Type != content.TypeFlash || (!admin && post.AuthorID != p.UserID) {
					s.renderError(w, 404, "快报不存在")
					return
				}
				data.Flash.Current = s.flashItem(r, post, true)
			}
		}
	}
	page := "flash-submit"
	if admin {
		page = "admin-flash"
	}
	s.render(w, 200, page, data)
}
