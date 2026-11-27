package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/content"
)

type taxonomyArchiveView struct {
	Term        content.Term
	Related     []taxonomyArchiveLink
	Label       string
	Total       int
	Page        int
	Pages       int
	HasPrevious bool
	HasNext     bool
	PreviousURL string
	NextURL     string
}

type taxonomyArchiveLink struct {
	Term content.Term
	URL  string
}

func (s *Server) categoryArchive(writer http.ResponseWriter, request *http.Request) {
	s.renderTaxonomyArchive(writer, request, content.TaxonomyCategory)
}

func (s *Server) tagArchive(writer http.ResponseWriter, request *http.Request) {
	s.renderTaxonomyArchive(writer, request, content.TaxonomyTag)
}

func (s *Server) renderTaxonomyArchive(writer http.ResponseWriter, request *http.Request, taxonomy string) {
	slug := request.PathValue("slug")
	if !content.ValidTermSlug(slug) {
		s.renderError(writer, http.StatusNotFound, "内容归档没有找到")
		return
	}
	query, ok := strictQueryShape(request.URL.Query(), map[string]bool{"page": true})
	if !ok {
		s.renderError(writer, http.StatusBadRequest, "归档筛选参数不正确")
		return
	}
	page, err := positiveQueryInt(query.Get("page"), 1, 100000)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "分页参数不正确")
		return
	}
	pageSize := max(1, s.siteSnapshot(request.Context()).Settings.Reading.ExplorePosts)
	term, posts, total, err := s.repository.ListPublishedByTerm(request.Context(), taxonomy, slug, pageSize, (page-1)*pageSize)
	if errors.Is(err, content.ErrTermNotFound) {
		s.renderError(writer, http.StatusNotFound, "内容归档没有找到")
		return
	}
	if err != nil {
		slog.Error("load taxonomy archive", "taxonomy", taxonomy, "slug", slug, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "内容归档暂时不可用")
		return
	}
	relatedResult, listErr := s.repository.ListTerms(request.Context(), content.TermQuery{Taxonomy: taxonomy, Limit: 24})
	if listErr != nil {
		slog.Error("load related taxonomy terms", "taxonomy", taxonomy, "error", listErr)
	}
	related := make([]taxonomyArchiveLink, 0, 8)
	for _, candidate := range relatedResult.Items {
		if candidate.ID == term.ID || candidate.Count == 0 {
			continue
		}
		related = append(related, taxonomyArchiveLink{Term: candidate, URL: termArchiveURL(candidate)})
		if len(related) == 8 {
			break
		}
	}
	pageCount := max(1, (total+pageSize-1)/pageSize)
	archivePath := termArchiveURL(term)
	description := term.Description
	if description == "" {
		description = "浏览归入“" + term.Name + "”的全部公开内容。"
	}
	label := "分类目录"
	if taxonomy == content.TaxonomyTag {
		label = "文章标签"
	}
	data := s.baseViewFor(writer, term.Name, description, archivePath)
	data.Posts = posts
	data.BodyClass = "is-taxonomy is-taxonomy-" + strings.ReplaceAll(taxonomy, "_", "-")
	data.ActiveNav = "explore"
	data.Archive = taxonomyArchiveView{
		Term: term, Related: related, Label: label, Total: total, Page: page, Pages: pageCount,
		HasPrevious: page > 1, HasNext: page < pageCount,
		PreviousURL: taxonomyArchivePageURL(term, max(1, page-1)),
		NextURL:     taxonomyArchivePageURL(term, min(pageCount, page+1)),
	}
	s.render(writer, http.StatusOK, "archive", data)
}

func strictQueryShape(query url.Values, allowed map[string]bool) (url.Values, bool) {
	for key, values := range query {
		if !allowed[key] || len(values) != 1 {
			return nil, false
		}
	}
	return query, true
}

func taxonomyArchivePageURL(term content.Term, page int) string {
	base := termArchiveURL(term)
	if page <= 1 {
		return base
	}
	return base + "?page=" + strconv.Itoa(page)
}
