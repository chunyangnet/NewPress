package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
)

func (s *Server) home(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		if s.tryContentRedirect(writer, request) {
			return
		}
		s.renderError(writer, http.StatusNotFound, "页面没有找到")
		return
	}
	data, err := s.homePage(request, s.themes.SettingsSnapshot().Values)
	if err != nil {
		if errors.Is(err, errHomeQuery) {
			s.renderError(writer, http.StatusBadRequest, "首页筛选或分页参数不正确")
			return
		}
		slog.Error("build homepage", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "内容暂时不可用")
		return
	}
	if data.Discovery.Page > data.Discovery.Pages {
		values, _ := url.ParseQuery(request.URL.RawQuery)
		http.Redirect(writer, request, discoveryURL("/", values, data.Discovery.Pages)+"#latest", http.StatusFound)
		return
	}
	s.render(writer, http.StatusOK, "home", data)
}

func (s *Server) community(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/community/" {
		s.renderError(writer, http.StatusNotFound, "圈子页面没有找到")
		return
	}
	if s.circles != nil && s.config.CommunityEnabled {
		s.circlesPage(writer, request)
		return
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "社区分页参数不正确")
		return
	}
	query, ok := strictQueryShape(query, map[string]bool{
		"page": true, "planet": true, "discussion": true, "discussion_sort": true, "view": true,
	})
	if !ok {
		s.renderError(writer, http.StatusBadRequest, "社区分页参数不正确")
		return
	}
	page, err := positiveQueryInt(query.Get("page"), 1, 100000)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "社区分页参数不正确")
		return
	}
	const pageSize = 8
	published, err := s.repository.ListPublishedPage(request.Context(), pageSize, (page-1)*pageSize)
	if err != nil {
		slog.Error("list posts for community", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "社区内容暂时不可用")
		return
	}
	pages := max(1, (published.Total+pageSize-1)/pageSize)
	if page > pages {
		http.Redirect(writer, request, communityPageURL(query, pages), http.StatusFound)
		return
	}
	canonical := "/community/"
	if page > 1 {
		canonical += "?page=" + strconv.Itoa(page)
	}
	data := s.baseViewFor(writer, "社区讨论", "围绕站点内容建立的公开讨论空间", canonical)
	data.Posts = published.Items
	data.BodyClass = "is-community"
	data.ActiveNav = "community"
	data.Community = communityView{
		Total: published.Total, Page: page, Pages: pages, HasPrevious: page > 1, HasNext: page < pages,
		PreviousURL: communityPageURL(query, max(1, page-1)), NextURL: communityPageURL(query, min(pages, page+1)),
	}
	s.render(writer, http.StatusOK, "community", data)
}

func communityPageURL(query url.Values, page int) string {
	values := make(url.Values)
	for _, key := range []string{"planet", "discussion", "discussion_sort"} {
		if value := query.Get(key); value != "" {
			values.Set(key, value)
		}
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	encoded := values.Encode()
	path := "/community/"
	if encoded != "" {
		path += "?" + encoded
	}
	return path + "#questions"
}

func (s *Server) single(writer http.ResponseWriter, request *http.Request) {
	slug := request.PathValue("slug")
	post, ok := s.loadPublicCommentPost(writer, request, slug)
	if !ok {
		return
	}
	if s.canonicalContentRedirect(writer, request, post) {
		return
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(values["comments_page"]) > 1 || len(values["comment"]) > 1 {
		s.renderError(writer, http.StatusBadRequest, "评论分页参数不正确")
		return
	}
	page, err := positiveQueryInt(values.Get("comments_page"), 1, 1_000_000)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "评论分页参数不正确")
		return
	}
	notice := adminNotice{}
	switch values.Get("comment") {
	case "approved":
		notice = adminNotice{"评论已发布，感谢参与认真讨论。", "success"}
	case "pending":
		notice = adminNotice{s.siteSettings.Snapshot().Settings.Discussion.ModerationNotice, "success"}
	}
	s.renderPublicPost(writer, request, http.StatusOK, post, page, publicCommentForm{}, nil, notice)
}
