package httpserver

import (
	"net/http"
	"strconv"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

type snippetPageView struct {
	Items                                 []content.Snippet
	Current                               content.Snippet
	Body                                  string
	Reference                             string
	Revisions                             []content.SnippetRevisionInfo
	HistoryNextURL, HistoryFirstURL       string
	Editing, Manage, Writable, APIEnabled bool
	Page, Pages, Total                    int
	PreviousURL, NextURL                  string
}

func (s *Server) adminSnippets(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilityContentRead)
	if !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"edit": true, "page": true, "before": true})
	if !ok {
		return
	}
	page, err := positiveQueryInt(q.Get("page"), 1, 100000)
	if err != nil {
		s.renderError(w, 400, "页码不正确")
		return
	}
	before := int64(0)
	if raw := q.Get("before"); raw != "" {
		before, err = positivePathID(raw)
		if err != nil || q.Get("edit") == "" || q.Get("edit") == "new" {
			s.renderError(w, 400, "历史游标不正确")
			return
		}
	}
	a := snippetAccess(p)
	data := s.baseViewFor(w, "共享片段", "保存一份内容，明确更新它的引用。", "/admin/snippets/")
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-snippets"
	data.Admin = s.adminView("snippets", p, readOnly)
	view := snippetPageView{Page: page, Manage: a.Manage, Writable: !readOnly && (a.Manage || a.WriteOwn), APIEnabled: s.siteSettings.Snapshot().Settings.API.AdminContentEnabled}
	if edit := q.Get("edit"); edit != "" {
		view.Editing = true
		view.Current.Visibility = content.SnippetPrivate
		if edit == "new" {
			if !a.Manage {
				s.renderError(w, 403, "当前账号不能创建共享片段")
				return
			}
		} else {
			id, err := strconv.ParseInt(edit, 10, 64)
			if err != nil || id < 1 {
				s.renderError(w, 404, "片段没有找到")
				return
			}
			record, err := s.repository.GetSnippetPage(r.Context(), id, a, before, 24)
			if err != nil {
				s.snippetError(w, r, err)
				return
			}
			view.Current = record.Snippet
			view.Body, view.Revisions = record.Current.ContentSource, record.History
			if record.NextBefore > 0 {
				view.HistoryNextURL = "/admin/snippets/?edit=" + strconv.FormatInt(id, 10) + "&before=" + strconv.FormatInt(record.NextBefore, 10)
			}
			if before > 0 {
				view.HistoryFirstURL = "/admin/snippets/?edit=" + strconv.FormatInt(id, 10)
			}
			if record.Visibility == content.SnippetShared {
				view.Reference = contenthtml.SnippetShell(contenthtml.SnippetKey{ID: id, Version: record.CurrentVersion})
			}
		}
	} else {
		list, err := s.repository.ListSnippets(r.Context(), a, 24, (page-1)*24)
		if err != nil {
			s.snippetError(w, r, err)
			return
		}
		view.Items, view.Total, view.Pages = list.Items, list.Total, max(1, (list.Total+23)/24)
		if page > 1 {
			view.PreviousURL = "/admin/snippets/?page=" + strconv.Itoa(page-1)
		}
		if page < view.Pages {
			view.NextURL = "/admin/snippets/?page=" + strconv.Itoa(page+1)
		}
	}
	data.Snippets = view
	s.render(w, 200, "admin-snippets", data)
}
