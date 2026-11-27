package httpserver

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/linkpage"
)

type linkItemView struct {
	linkpage.Item
	Host, Icon, Initial string
}
type linkPagesView struct {
	Board                        linkpage.Board
	Boards                       []linkpage.Board
	Items                        []linkItemView
	Current                      linkpage.Item
	Categories                   []string
	Editing, Creating, Available bool
	Style, Search, Category      string
}

func (s *Server) SetLinkPageStore(store linkpage.Store) {
	if store != nil {
		s.linkPages = store
	}
}
func (s *Server) registerLinkPageRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/features/links/{$}", s.adminLinkPages)
	mux.HandleFunc("POST /api/v1/admin/link-pages/{slug}", s.linkPageWrite)
	mux.HandleFunc("POST /api/v1/admin/link-metadata", s.linkMetadata)
	mux.HandleFunc("GET /links/{$}", s.publicLinkPages)
	mux.HandleFunc("GET /links/{slug}/{$}", s.publicLinkPages)
	mux.HandleFunc("GET /links/{slug}/icons/{id}", s.linkIcon)
}
func linkView(b linkpage.Board, i linkpage.Item) linkItemView {
	v := linkItemView{Item: i, Host: linkpage.Host(i.URL)}
	r := []rune(i.Name)
	if len(r) > 0 {
		v.Initial = strings.ToUpper(string(r[0]))
	}
	if i.Logo != "" {
		v.Icon = "/links/" + b.Slug + "/icons/" + strconv.FormatInt(i.ID, 10) + "?v=" + strconv.FormatInt(b.Version, 10)
	}
	if i.LogoURL != "" {
		v.Icon = i.LogoURL
	}
	v.Logo = ""
	return v
}
func (s *Server) adminLinkPages(w http.ResponseWriter, r *http.Request) {
	p, ro, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"edit": true, "item": true})
	if !ok {
		return
	}
	v := linkPagesView{Available: s.pageAvailable("links"), Current: linkpage.Item{Enabled: true}}
	var e error
	v.Boards, e = s.linkPages.List(r.Context())
	if e != nil {
		s.linkError(w, r, e)
		return
	}
	slug := q.Get("edit")
	if slug != "" {
		v.Editing = true
		v.Creating = slug == "new"
		if v.Creating {
			v.Board = linkpage.Board{Settings: linkpage.Defaults()}
		} else {
			v.Board, e = s.linkPages.Get(r.Context(), slug)
			if e != nil {
				s.linkError(w, r, e)
				return
			}
		}
		for _, i := range v.Board.Items {
			v.Items = append(v.Items, linkView(v.Board, i))
			if strconv.FormatInt(i.ID, 10) == q.Get("item") {
				v.Current = i
			}
		}
		if q.Get("item") != "" && v.Current.ID == 0 {
			s.renderError(w, 404, "友链不存在")
			return
		}
	}
	data := s.baseViewFor(w, "友链页面", "管理主题附带的友链页面。", "/admin/features/links/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-links"
	data.Admin = s.adminView("features", p, ro)
	data.LinkPages = v
	s.render(w, 200, "admin-links", data)
}
func (s *Server) linkError(w http.ResponseWriter, r *http.Request, e error) {
	code := 422
	if errors.Is(e, linkpage.ErrConflict) || errors.Is(e, linkpage.ErrChanged) {
		code = 409
	} else if errors.Is(e, linkpage.ErrNotFound) {
		code = 404
	} else if errors.Is(e, linkpage.ErrForbidden) {
		code = 403
	}
	s.writeAPIError(w, r, code, "links.operation_failed", e.Error(), nil)
}
func (s *Server) linkPageWrite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var c linkpage.Command
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &c, 160<<10, "160 KiB"); !ok || !s.pluginIdentity(w, r, p, c.ExpectedUserID) {
		return
	}
	if c.Action == "save_item" && c.Item != nil {
		release, ok := s.holdSettingMedia(w, r, map[string]any{"logo_url": c.Item.LogoURL})
		if !ok {
			return
		}
		defer release()
	}
	result, e := s.linkPages.Apply(r.Context(), r.PathValue("slug"), linkpage.Actor{ID: p.UserID, Admin: true}, c, time.Now().UTC())
	if e != nil {
		s.linkError(w, r, e)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) linkMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	var b struct {
		URL            string `json:"url"`
		ExpectedUserID int64  `json:"expected_user_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &b, 4096, "4 KiB"); !ok || !s.pluginIdentity(w, r, p, b.ExpectedUserID) {
		return
	}
	if allowed, _ := s.accessRate.allow("link-metadata:"+strconv.FormatInt(p.UserID, 10), 12, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "links.rate_limited", "提取过于频繁，请稍后再试。", nil)
		return
	}
	m, e := linkpage.Extract(r.Context(), b.URL)
	if e != nil {
		s.linkError(w, r, e)
		return
	}
	writeJSON(w, 200, m)
}
func (s *Server) publicLinkPages(w http.ResponseWriter, r *http.Request) {
	if !s.pageAvailable("links") {
		s.renderError(w, 404, "当前主题不支持友链页面")
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"q": true, "category": true, "style": true})
	if !ok {
		return
	}
	v := linkPagesView{Search: strings.TrimSpace(q.Get("q")), Category: q.Get("category")}
	if len(v.Search) > 300 || len(v.Category) > 120 {
		s.linkError(w, r, linkpage.ErrInvalid)
		return
	}
	slug := r.PathValue("slug")
	if slug == "" {
		pages, e := s.linkPages.List(r.Context())
		if e != nil {
			s.linkError(w, r, e)
			return
		}
		for _, b := range pages {
			if b.Settings.Enabled {
				v.Boards = append(v.Boards, b)
			}
		}
	} else {
		b, e := s.linkPages.Get(r.Context(), slug)
		if e != nil {
			s.renderError(w, 404, "友链页面不存在")
			return
		}
		p, _ := s.currentAdmin(r)
		if !b.Settings.Enabled && !p.Can(auth.CapabilitySettingsManage) {
			s.renderError(w, 404, "友链页面暂未开放")
			return
		}
		v.Board = linkpage.Header(b)
		v.Style = b.Settings.Style
		if b.Settings.Switcher && q.Get("style") != "" {
			v.Style = q.Get("style")
		}
		if v.Style != "cards" && v.Style != "list" && v.Style != "wall" {
			s.linkError(w, r, linkpage.ErrInvalid)
			return
		}
		categories := map[string]bool{}
		for _, i := range b.Items {
			if !i.Enabled {
				continue
			}
			if i.Category != "" {
				categories[i.Category] = true
			}
			if v.Category != "" && v.Category != i.Category {
				continue
			}
			if b.Settings.Search && v.Search != "" && !strings.Contains(strings.ToLower(i.Name+" "+i.Description+" "+linkpage.Host(i.URL)), strings.ToLower(v.Search)) {
				continue
			}
			v.Items = append(v.Items, linkView(b, i))
		}
		for c := range categories {
			v.Categories = append(v.Categories, c)
		}
		sort.Strings(v.Categories)
		sort.SliceStable(v.Items, func(i, j int) bool {
			a, b := v.Items[i], v.Items[j]
			if a.Featured != b.Featured {
				return a.Featured
			}
			if a.Order != b.Order {
				return a.Order < b.Order
			}
			return a.ID < b.ID
		})
	}
	name, description, canonical := "友链页面", "探索站点推荐的朋友与作品。", "/links/"
	if slug != "" {
		name = v.Board.Settings.Name
		description = v.Board.Settings.Description
		canonical += slug + "/"
	}
	data := s.baseViewFor(w, name, description, canonical)
	data.BodyClass = "is-links"
	data.LinkPages = v
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, 200, "links", data)
}
func (s *Server) linkIcon(w http.ResponseWriter, r *http.Request) {
	b, e := s.linkPages.Get(r.Context(), r.PathValue("slug"))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	p, _ := s.currentAdmin(r)
	admin := p.Can(auth.CapabilitySettingsManage)
	if !b.Settings.Enabled && !admin {
		http.NotFound(w, r)
		return
	}
	for _, i := range b.Items {
		if strconv.FormatInt(i.ID, 10) != r.PathValue("id") || (!i.Enabled && !admin) {
			continue
		}
		if i.LogoURL != "" {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, i.LogoURL, http.StatusTemporaryRedirect)
			return
		}
		png, e := linkpage.DecodeLogo(i.Logo)
		if e != nil {
			break
		}
		w.Header().Set("Cache-Control", "no-store")
		if target, ok := s.objectStorage.LinkRedirectURL(r.Context(), b.Slug, i); ok {
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(png)
		return
	}
	http.NotFound(w, r)
}
