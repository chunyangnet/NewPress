package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

type discoveryPageLink struct {
	Page     int
	URL      string
	Current  bool
	Ellipsis bool
}

type discoveryView struct {
	Directory                    bool
	Total, Page, Pages, From, To int
	Sort, SortLabel, View, Path  string
	HasPrevious, HasNext         bool
	PreviousURL, NextURL         string
	Links                        []discoveryPageLink
}

type publicAuthorView struct {
	ID                                 int64
	Name, Initial, Description, Joined string
	AvatarURL                          string
	Legacy                             bool
	Badges                             []progressionBadge
}

func discoveryQuery(request *http.Request, defaultSort string, limit int) (url.Values, content.PublishedQuery, int, error) {
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return nil, content.PublishedQuery{}, 0, err
	}
	values, valid := strictQueryShape(values, map[string]bool{"q": true, "category": true, "sort": true, "view": true, "page": true})
	if !valid {
		return nil, content.PublishedQuery{}, 0, errors.New("invalid discovery query")
	}
	page, err := positiveQueryInt(values.Get("page"), 1, 100000)
	if err != nil {
		return nil, content.PublishedQuery{}, 0, err
	}
	if view := values.Get("view"); view != "" && view != "list" && view != "grid" {
		return nil, content.PublishedQuery{}, 0, errors.New("invalid content view")
	}
	sort := values.Get("sort")
	if sort == "" {
		sort = defaultSort
		if strings.TrimSpace(values.Get("q")) != "" {
			sort = "relevance"
		}
	}
	query, err := content.NormalizePublishedQuery(content.PublishedQuery{Search: values.Get("q"), Category: values.Get("category"), Sort: sort, Limit: limit, Offset: (page - 1) * limit})
	return values, query, page, err
}

func discoveryURL(path string, values url.Values, page int) string {
	query := make(url.Values)
	for _, key := range []string{"q", "category", "sort", "view"} {
		if value := strings.TrimSpace(values.Get(key)); value != "" {
			query.Set(key, value)
		}
	}
	if page > 1 {
		query.Set("page", strconv.Itoa(page))
	}
	if query.Encode() != "" {
		return path + "?" + query.Encode()
	}
	return path
}

func newDiscoveryView(path string, values url.Values, query content.PublishedQuery, page, total int) discoveryView {
	pages := max(1, (total+query.Limit-1)/query.Limit)
	view := discoveryView{
		Total: total, Page: page, Pages: pages, From: min(query.Offset+1, total), To: min(query.Offset+query.Limit, total),
		Sort: query.Sort, SortLabel: map[string]string{"updated": "最近更新", "published": "最新发布", "oldest": "最早发布", "title": "标题排序", "relevance": "相关性"}[query.Sort],
		View: "grid", Path: path, HasPrevious: page > 1, HasNext: page < pages,
		PreviousURL: discoveryURL(path, values, max(1, page-1)), NextURL: discoveryURL(path, values, min(pages, page+1)),
	}
	if values.Get("view") == "list" {
		view.View = "list"
	}
	last := 0
	candidates := []int{1}
	for i := max(2, page-2); i <= min(pages-1, page+2); i++ {
		candidates = append(candidates, i)
	}
	if pages > 1 {
		candidates = append(candidates, pages)
	}
	for _, i := range candidates {
		if i-last > 1 {
			view.Links = append(view.Links, discoveryPageLink{Ellipsis: true})
		}
		view.Links = append(view.Links, discoveryPageLink{Page: i, URL: discoveryURL(path, values, i), Current: i == page})
		last = i
	}
	return view
}

func (s *Server) explore(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/explore/" {
		s.renderError(writer, 404, "发现页面没有找到")
		return
	}
	values, query, page, err := discoveryQuery(request, "updated", s.siteSnapshot(request.Context()).Settings.Reading.ExplorePosts)
	if err != nil {
		s.renderError(writer, 400, "搜索、排序或分页参数不正确")
		return
	}
	if query.Search == "" && query.Category == "" && values.Get("sort") == "" && values.Get("page") == "" && values.Get("view") == "" {
		data := s.baseViewFor(writer, "发现", "按栏目浏览站点入口", "/explore/")
		data.Discovery = discoveryView{Directory: true, Sort: "updated", View: "grid"}
		data.BodyClass, data.ActiveNav = "is-explore", "explore"
		s.render(writer, http.StatusOK, "explore", data)
		return
	}
	result, err := s.repository.SearchPublished(request.Context(), query)
	if err != nil {
		slog.Error("search public content", "error", err)
		s.renderError(writer, 500, "内容暂时不可用")
		return
	}
	view := newDiscoveryView("/explore/", values, query, page, result.Total)
	if page > view.Pages {
		http.Redirect(writer, request, discoveryURL("/explore/", values, view.Pages), http.StatusFound)
		return
	}
	categories, err := s.repository.ListTerms(request.Context(), content.TermQuery{Taxonomy: content.TaxonomyCategory, Limit: 500})
	if err != nil {
		s.renderError(writer, 500, "分类暂时不可用")
		return
	}
	data := s.baseViewFor(writer, "发现内容", "搜索、筛选并发现本站的文章与创作者", discoveryURL("/explore/", values, page))
	data.Posts, data.SearchQuery, data.Discovery = result.Items, query.Search, view
	data.Category, data.CategorySlug = query.Category, query.Category
	for _, category := range categories.Items {
		if category.Count > 0 {
			data.Categories = append(data.Categories, category)
		}
		if category.Slug == content.NormalizeTermSlug(query.Category) || strings.EqualFold(category.Name, query.Category) {
			data.Category, data.CategorySlug = category.Name, category.Slug
		}
	}
	data.Featured, err = s.repository.ListPublished(request.Context(), 3)
	if err != nil {
		s.renderError(writer, 500, "内容暂时不可用")
		return
	}
	data.BodyClass, data.ActiveNav = "is-explore", "explore"
	if event, ok := request.Context().Value(analyticsContextKey{}).(*analyticsObservation); ok && query.Search != "" && page == 1 {
		event.Search, event.Miss, event.Term = true, result.Total == 0, query.Search
	}
	s.render(writer, http.StatusOK, "explore", data)
}

func (s *Server) author(writer http.ResponseWriter, request *http.Request) {
	slug := request.PathValue("slug")
	if request.URL.Path != "/authors/"+slug+"/" {
		s.renderError(writer, 404, "创作者没有找到")
		return
	}
	values, query, page, err := discoveryQuery(request, "published", 12)
	if err != nil {
		s.renderError(writer, 400, "作者内容分页参数不正确")
		return
	}
	author := publicAuthorView{}
	if slug == "newpress-team" {
		author = publicAuthorView{Name: "NewPress Team", Initial: "NP", Description: "记录内容平台、主题设计与扩展架构的实践。", Legacy: true}
		query.LegacyAuthor = author.Name
	} else {
		id, err := strconv.ParseInt(slug, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != slug {
			s.renderError(writer, 404, "创作者没有找到")
			return
		}
		user, err := s.auth.GetUser(request.Context(), id)
		if errors.Is(err, auth.ErrUserNotFound) || errors.Is(err, auth.ErrUserStoreNotAvailable) || err == nil && user.Status != auth.UserStatusActive {
			s.renderError(writer, 404, "创作者没有找到")
			return
		}
		if err != nil {
			s.renderError(writer, 500, "创作者信息暂时不可用")
			return
		}
		initial, _ := utf8.DecodeRuneInString(user.DisplayName)
		author = publicAuthorView{ID: user.ID, Name: user.DisplayName, Initial: string(initial), Description: "在这里分享作品、记录思考，和读者一起讨论。", Joined: s.monthLabel(user.CreatedAt, request.Context())}
		author.AvatarURL = s.avatarURL(user.AvatarMediaID)
		if user.Bio != "" {
			author.Description = user.Bio
		}
		query.AuthorID = user.ID
		author.Badges = s.progressionBadges(request.Context(), user.ID, true)
	}
	result, err := s.repository.SearchPublished(request.Context(), query)
	if err != nil {
		slog.Error("list author content", "error", err)
		s.renderError(writer, 500, "创作者内容暂时不可用")
		return
	}
	path := "/authors/" + slug + "/"
	view := newDiscoveryView(path, values, query, page, result.Total)
	if page > view.Pages {
		http.Redirect(writer, request, discoveryURL(path, values, view.Pages), http.StatusFound)
		return
	}
	data := s.baseViewFor(writer, author.Name, author.Description, discoveryURL(path, values, page))
	data.Posts, data.Author, data.Discovery = result.Items, author, view
	data.SearchQuery = query.Search
	data.Category, data.CategorySlug = query.Category, query.Category
	categories, err := s.repository.ListTerms(request.Context(), content.TermQuery{Taxonomy: content.TaxonomyCategory, Limit: 500})
	if err != nil {
		s.renderError(writer, 500, "分类暂时不可用")
		return
	}
	for _, category := range categories.Items {
		if category.Count > 0 {
			data.Categories = append(data.Categories, category)
		}
		if category.Slug == content.NormalizeTermSlug(query.Category) || strings.EqualFold(category.Name, query.Category) {
			data.Category, data.CategorySlug = category.Name, category.Slug
		}
	}
	data.BodyClass, data.ActiveNav = "is-author", "explore"
	s.render(writer, http.StatusOK, "author", data)
}
