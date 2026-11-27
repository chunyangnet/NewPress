package httpserver

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/announcement"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

type announcementAdminView struct {
	State                    announcement.State
	Current                  announcement.Item
	Editing                  bool
	Search, Status, Date     string
	Items                    []announcement.Item
	Total, Published, Hidden int
}

type homeAnnouncement struct {
	Title, URL string
	Date       time.Time
	Pinned     bool
}

// Only published, currently visible items reach the public theme adapter.
type announcementPublicItem struct {
	ID         int64
	Title, URL string
	Date       time.Time
	Pinned     bool
}

type announcementPageLink struct {
	Number  int
	URL     string
	Current bool
}

type announcementParagraph struct {
	Text    string
	Heading bool
}

var announcementSectionTitle = regexp.MustCompile(`^[一二三四五六七八九十百]{1,3}、\S`)

func publicAnnouncementParagraph(line string) announcementParagraph {
	text := strings.TrimSpace(line)
	return announcementParagraph{Text: line, Heading: utf8.RuneCountInString(text) <= 48 && announcementSectionTitle.MatchString(text)}
}

type announcementPublicView struct {
	Items                                    []announcementPublicItem
	Current                                  announcementPublicItem
	PreviousItem, NextItem                   *announcementPublicItem
	Paragraphs                               []announcementParagraph
	RelatedURL, Query, Scope                 string
	Total, PinnedTotal, Results, Page, Pages int
	Previous, Next                           string
	PageLinks                                []announcementPageLink
}

func (s *Server) SetAnnouncementStore(store *announcement.Store) {
	if store != nil {
		s.announcements = store
	}
}

func (s *Server) registerAnnouncementRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/announcements/{$}", s.adminAnnouncements)
	mux.HandleFunc("POST /admin/announcements/{$}", s.saveAnnouncement)
	mux.HandleFunc("GET /announcements/{$}", s.publicAnnouncements)
	mux.HandleFunc("GET /announcements/{id}/{$}", s.publicAnnouncements)
}

func (s *Server) adminAnnouncements(w http.ResponseWriter, r *http.Request) {
	p, ro, ok := s.requireAdminPageCapability(w, r, auth.CapabilityContentWrite)
	if !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"edit": true, "q": true, "status": true, "notice": true})
	if !ok {
		return
	}
	v := announcementAdminView{Search: strings.TrimSpace(q.Get("q")), Status: q.Get("status")}
	if len([]rune(v.Search)) > 120 || (v.Status != "" && v.Status != "published" && v.Status != "hidden") {
		s.renderError(w, 400, "公告筛选条件无效")
		return
	}
	state, err := s.announcements.Snapshot(r.Context())
	if err != nil {
		s.renderError(w, 500, "公告列表暂时无法读取")
		return
	}
	v.State = state
	if edit := q.Get("edit"); edit != "" {
		v.Editing = true
		v.Current = announcement.Item{Published: true, Date: time.Now().In(s.siteLocation(r.Context()))}
		if edit != "new" {
			id, err := strconv.ParseInt(edit, 10, 64)
			if err != nil || id < 1 {
				s.renderError(w, 404, "公告不存在")
				return
			}
			found := false
			for _, i := range state.Items {
				if i.ID == id {
					v.Current = i
					found = true
					break
				}
			}
			if !found {
				s.renderError(w, 404, "公告不存在")
				return
			}
		}
	}
	msg := adminNotice{}
	if q.Get("notice") == "saved" {
		msg = adminNotice{"公告已保存，公开内容已同步更新。", "success"}
	}
	if q.Get("notice") == "deleted" {
		msg = adminNotice{"公告已删除。", "success"}
	}
	s.renderAnnouncementsAdmin(w, r, 200, p, ro, v, msg)
}

func (s *Server) renderAnnouncementsAdmin(w http.ResponseWriter, r *http.Request, status int, p auth.Principal, ro bool, v announcementAdminView, notice adminNotice) {
	v.Total = len(v.State.Items)
	for _, i := range v.State.Items {
		if i.Published {
			v.Published++
		} else {
			v.Hidden++
		}
		if v.Status == "published" && !i.Published || v.Status == "hidden" && i.Published {
			continue
		}
		if v.Search != "" && !strings.Contains(strings.ToLower(i.Title+" "+i.Body), strings.ToLower(v.Search)) {
			continue
		}
		v.Items = append(v.Items, i)
	}
	announcement.Sort(v.Items)
	zone := s.siteLocation(r.Context())
	if zone == nil {
		zone = time.UTC
	}
	if v.Date == "" && !v.Current.Date.IsZero() {
		v.Date = v.Current.Date.In(zone).Format("2006-01-02")
	}
	d := s.baseViewFor(w, "公告管理", "独立维护站点公告列表", "/admin/announcements/")
	d.IsAdmin, d.BodyClass = true, "is-admin is-admin-announcements"
	d.Admin = s.adminView("announcements", p, ro)
	d.Admin.Notice, d.Admin.NoticeTone = notice.Message, notice.Tone
	d.Announcements = v
	s.render(w, status, "admin-announcements", d)
}

func (s *Server) saveAnnouncement(w http.ResponseWriter, r *http.Request) {
	p, form, ok := s.requireContentWriteLimit(w, r, 96<<10, "announcement_form_too_large")
	if !ok {
		return
	}
	if !p.Can(auth.CapabilityContentWrite) {
		s.renderError(w, 403, "当前账号没有管理公告的权限")
		return
	}
	if r.URL.RawQuery != "" {
		s.renderError(w, 400, "公告表单格式不正确")
		return
	}
	allowed := map[string]bool{"csrf_token": true, "revision": true, "expected_user_id": true, "action": true, "id": true, "title": true, "body": true, "url": true, "date": true, "order": true, "published": true, "pinned": true}
	for key, entries := range form {
		if !allowed[key] || len(entries) != 1 {
			s.renderError(w, 400, "公告字段未知或重复")
			return
		}
	}
	if form.Get("expected_user_id") != strconv.FormatInt(p.UserID, 10) {
		s.renderError(w, 409, "登录账号已变化，请重新载入公告列表")
		return
	}
	id, err := strconv.ParseInt(form.Get("id"), 10, 64)
	if err != nil || id < 0 {
		s.renderError(w, 400, "公告标识无效")
		return
	}
	if action := form.Get("action"); action != "save" && action != "delete" {
		s.renderError(w, 400, "公告操作无效")
		return
	}
	state, err := s.announcements.Snapshot(r.Context())
	if err != nil {
		s.renderError(w, 500, "公告列表暂时无法读取")
		return
	}
	item := announcement.Item{ID: id, Title: form.Get("title"), Body: strings.ReplaceAll(form.Get("body"), "\r\n", "\n"), URL: form.Get("url"), Published: form.Get("published") == "true", Pinned: form.Get("pinned") == "true"}
	if form.Get("action") == "save" {
		zone := s.siteLocation(r.Context())
		if zone == nil {
			zone = time.UTC
		}
		item.Date, err = time.ParseInLocation("2006-01-02", form.Get("date"), zone)
		if err == nil {
			item.Order, err = strconv.Atoi(form.Get("order"))
		}
		for _, key := range []string{"published", "pinned"} {
			if form.Get(key) != "" && form.Get(key) != "true" {
				err = announcement.ErrInvalid
			}
		}
		if err != nil {
			s.renderAnnouncementsAdmin(w, r, 422, p, false, announcementAdminView{State: state, Current: item, Editing: true, Date: form.Get("date")}, adminNotice{"请检查公告日期、排序和状态。", "error"})
			return
		}
	}
	_, err = s.announcements.Save(r.Context(), form.Get("revision"), form.Get("action"), item)
	if err != nil {
		status := 422
		message := err.Error()
		if errors.Is(err, announcement.ErrConflict) {
			status = 409
		} else if errors.Is(err, announcement.ErrNotFound) {
			status = 404
		} else if !errors.Is(err, announcement.ErrInvalid) && !errors.Is(err, announcement.ErrCapacity) {
			status = 500
			message = "公告保存失败，请稍后重试。"
		}
		s.renderAnnouncementsAdmin(w, r, status, p, false, announcementAdminView{State: state, Current: item, Editing: form.Get("action") == "save"}, adminNotice{message, "error"})
		return
	}
	notice := "saved"
	if form.Get("action") == "delete" {
		notice = "deleted"
	}
	http.Redirect(w, r, "/admin/announcements/?notice="+notice, http.StatusSeeOther)
}

func announcementURL(i announcement.Item) string {
	if i.URL != "" {
		return i.URL
	}
	return fmt.Sprintf("/announcements/%d/", i.ID)
}

func (s *Server) publicAnnouncements(w http.ResponseWriter, r *http.Request) {
	allowed := map[string]bool{"q": true, "scope": true, "page": true}
	if r.PathValue("id") != "" {
		allowed = nil
	}
	query, ok := s.strictAPIQuery(w, r, allowed)
	if !ok {
		return
	}
	page, pageErr := positiveQueryInt(query.Get("page"), 1, 100000)
	v := announcementPublicView{Query: strings.TrimSpace(query.Get("q")), Scope: query.Get("scope"), Page: 1, Pages: 1}
	if pageErr != nil || !utf8.ValidString(v.Query) || utf8.RuneCountInString(v.Query) > 120 || strings.ContainsAny(v.Query, "\x00\r\n\t") || !oneOf(v.Scope, "", "pinned") {
		s.renderError(w, 400, "公告筛选条件无效")
		return
	}
	state, err := s.announcements.Snapshot(r.Context())
	if err != nil {
		s.renderError(w, 500, "公告暂时无法读取")
		return
	}
	items := announcement.Public(state, time.Now().In(s.siteLocation(r.Context())))
	v.Total = len(items)
	project := func(i announcement.Item) announcementPublicItem {
		return announcementPublicItem{ID: i.ID, Title: i.Title, URL: announcementURL(i), Date: i.Date, Pinned: i.Pinned}
	}
	for _, item := range items {
		if item.Pinned {
			v.PinnedTotal++
		}
	}
	title := "通知公告"
	var body strings.Builder
	updated := time.Now().In(s.siteLocation(r.Context()))
	if raw := r.PathValue("id"); raw != "" {
		id, e := strconv.ParseInt(raw, 10, 64)
		var current *announcement.Item
		for index, i := range items {
			if i.ID == id {
				current = &i
				if index > 0 {
					previous := project(items[index-1])
					v.PreviousItem = &previous
				}
				if index+1 < len(items) {
					next := project(items[index+1])
					v.NextItem = &next
				}
				break
			}
		}
		if e != nil || current == nil {
			s.renderError(w, 404, "公告不存在或尚未公开")
			return
		}
		title, updated = current.Title, current.Date
		v.Current, v.RelatedURL = project(*current), current.URL
		body.WriteString(`<p><a href="/announcements/">← 全部公告</a></p>`)
		if current.Body == "" {
			v.Paragraphs = append(v.Paragraphs, publicAnnouncementParagraph(current.Title))
			body.WriteString("<p>" + html.EscapeString(current.Title) + "</p>")
		}
		for _, line := range strings.Split(current.Body, "\n") {
			if line != "" {
				v.Paragraphs = append(v.Paragraphs, publicAnnouncementParagraph(line))
				body.WriteString("<p>" + html.EscapeString(line) + "</p>")
			}
		}
		if current.URL != "" {
			body.WriteString(`<p><a href="` + html.EscapeString(current.URL) + `">查看相关内容 →</a></p>`)
		}
	} else {
		filtered := make([]announcement.Item, 0, len(items))
		for _, item := range items {
			if v.Scope == "pinned" && !item.Pinned || v.Query != "" && !strings.Contains(strings.ToLower(item.Title+" "+item.Body), strings.ToLower(v.Query)) {
				continue
			}
			filtered = append(filtered, item)
		}
		v.Results = len(filtered)
		v.Pages = max(1, (v.Results+9)/10)
		v.Page = min(page, v.Pages)
		if v.Page > 1 {
			v.Previous = questionLink("/announcements/", query, v.Page-1)
		}
		if v.Page < v.Pages {
			v.Next = questionLink("/announcements/", query, v.Page+1)
		}
		for n := max(1, v.Page-2); n <= min(v.Pages, v.Page+2); n++ {
			v.PageLinks = append(v.PageLinks, announcementPageLink{Number: n, URL: questionLink("/announcements/", query, n), Current: n == v.Page})
		}
		start := (v.Page - 1) * 10
		items = filtered[start:min(start+10, len(filtered))]
		body.WriteString("<ul>")
		for _, i := range items {
			v.Items = append(v.Items, project(i))
			pin := ""
			if i.Pinned {
				pin = "<strong>置顶 · </strong>"
			}
			body.WriteString(`<li>` + pin + `<a href="` + html.EscapeString(announcementURL(i)) + `">` + html.EscapeString(i.Title) + `</a><br><small>` + i.Date.In(updated.Location()).Format("2006-01-02") + `</small></li>`)
		}
		body.WriteString("</ul>")
		if len(items) == 0 {
			body.WriteString("<p>暂无公开公告。</p>")
		}
		if v.Previous != "" {
			body.WriteString(`<p><a href="` + html.EscapeString(v.Previous) + `">上一页</a></p>`)
		}
		if v.Next != "" {
			body.WriteString(`<p><a href="` + html.EscapeString(v.Next) + `">下一页</a></p>`)
		}
	}
	d := s.baseViewFor(w, title, "站点通知与公告", r.URL.Path)
	d.BodyClass = "is-page is-announcements"
	d.AnnouncementPage = v
	d.Post = content.Post{Type: content.TypePage, Title: title, ContentHTML: body.String(), AuthorName: d.Site.Title, UpdatedAt: updated, PublishedAt: updated, CommentStatus: content.CommentClosed}
	s.render(w, 200, "page", d)
}
