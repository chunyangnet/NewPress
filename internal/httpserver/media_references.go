package httpserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/mediausage"
)

type adminMediaReference struct {
	mediausage.Reference
	URL   string `json:"url"`
	Label string `json:"label"`
}

type adminMediaReferencesView struct {
	Items       []adminMediaReference
	Total       int
	Hidden      int
	Unavailable bool
	CanDelete   bool
	CanTrash    bool
	PreviousURL string
	NextURL     string
	Page        int
	Pages       int
}

func mediaReferenceAccess(principal auth.Principal) mediausage.Access {
	owner, _ := autosaveOwnerKey(principal)
	return mediausage.Access{
		ContentRead: principal.Can(auth.CapabilityContentRead), ContentAuthorID: contentAuthorScope(principal),
		SnippetsManage: principal.Can(auth.CapabilityContentWrite),
		AutosaveOwner:  owner, Settings: principal.Can(auth.CapabilitySettingsManage),
		Appearance: principal.Can(auth.CapabilityAppearanceManage), Navigation: principal.Can(auth.CapabilityNavigationManage),
		Users:    principal.Can(auth.CapabilityUsersRead),
		Taxonomy: principal.Can(auth.CapabilityTaxonomyManage),
	}
}

func mediaReferenceItem(reference mediausage.Reference) adminMediaReference {
	item := adminMediaReference{Reference: reference}
	contentURL := "/admin/content/" + strconv.FormatInt(reference.ID, 10)
	switch reference.Source {
	case content.MediaSourceCurrent:
		item.URL, item.Label = contentURL+"/edit/", "当前内容"
	case content.MediaSourceRevision:
		item.URL = contentURL + "/revisions/compare/?from=" + strconv.FormatInt(reference.RevisionID, 10) + "&to=current"
		item.Label = fmt.Sprintf("历史版本 v%d", reference.Version)
	case content.MediaSourceAutosave:
		item.URL, item.Label = contentURL+"/edit/?recovery=autosave", "我的自动保存"
	case content.MediaSourceSeries:
		item.URL, item.Label = "/admin/series/?edit="+strconv.FormatInt(reference.ID, 10), "专题与系列"
	case content.MediaSourceSnippet:
		item.URL, item.Label = "/admin/snippets/?edit="+strconv.FormatInt(reference.ID, 10), fmt.Sprintf("共享片段历史 v%d", reference.Version)
	case mediausage.SourceSite:
		item.URL, item.Label = "/admin/settings/#site-settings-identity", "站点设置"
	case mediausage.SourceTheme:
		item.URL, item.Label = "/admin/appearance/", "主题设置"
	case mediausage.SourceNavigation:
		item.URL, item.Label = "/admin/navigation/?menu="+strconv.FormatInt(reference.ID, 10), "导航菜单"
	case mediausage.SourceProfile:
		item.URL, item.Label = "/admin/users/"+strconv.FormatInt(reference.ID, 10)+"/edit/", "账号头像"
	case mediausage.SourceLink:
		item.URL, item.Label = "/admin/features/links/?edit="+url.QueryEscape(reference.Key)+"&item="+strconv.FormatInt(reference.ID, 10)+"#link-editor", "友链图标"
	case mediausage.SourceShop:
		item.URL, item.Label = "/admin/shop/", "商品图片"
	case mediausage.SourceBadge:
		item.URL, item.Label = "/admin/recognition/", "勋章图片"
	}
	if reference.ContentType != "" && reference.ContentType != "series" && reference.ContentType != "snippet" {
		kind := "文章"
		if reference.ContentType == content.TypePage {
			kind = "页面"
		}
		fields := make([]string, 0, len(reference.Fields))
		for _, field := range reference.Fields {
			switch field {
			case "cover_url":
				fields = append(fields, "封面")
			case "content_source":
				fields = append(fields, "正文")
			}
		}
		item.Label += " · " + kind + " · " + strings.Join(fields, "、")
	}
	return item
}

func (s *Server) mediaReferencesView(request *http.Request, item media.Item, principal auth.Principal) adminMediaReferencesView {
	page, err := positiveQueryInt(request.URL.Query().Get("references_page"), 1, 100000)
	if err != nil {
		page = 1
	}
	report, err := s.mediaUsage.Report(request.Context(), item, mediaReferenceAccess(principal), (page-1)*mediausage.PageSize)
	if err != nil {
		slog.Error("list media references", "id", item.ID, "error", err)
		return adminMediaReferencesView{Unavailable: true}
	}
	view := adminMediaReferencesView{
		Total: report.Total, Hidden: report.Hidden,
		CanDelete: report.Total == 0 && item.Status() == media.StatusTrash,
		CanTrash:  report.Total == 0 && item.Status() == media.StatusActive,
		Page:      page, Pages: max(1, (report.Visible+mediausage.PageSize-1)/mediausage.PageSize),
	}
	for _, reference := range report.Items {
		view.Items = append(view.Items, mediaReferenceItem(reference))
	}
	base := "/admin/media/" + strconv.FormatInt(item.ID, 10) + "/edit/?references_page="
	if page > 1 {
		view.PreviousURL = base + strconv.Itoa(min(page-1, view.Pages)) + "#media-references"
	}
	if report.HasMore {
		view.NextURL = base + strconv.Itoa(page+1) + "#media-references"
	}
	return view
}

func (s *Server) adminMediaReferencesAPI(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityMediaRead)
	if !ok {
		return
	}
	query, ok := s.strictAPIQuery(writer, request, map[string]bool{"page": true})
	if !ok {
		return
	}
	page, err := positiveQueryInt(query.Get("page"), 1, 100000)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "media.invalid_query", "引用页码不正确", nil)
		return
	}
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		s.writeAPIError(writer, request, http.StatusNotFound, "media.not_found", "媒体没有找到", nil)
		return
	}
	item, err := s.media.Get(id)
	if err != nil {
		if errors.Is(err, media.ErrNotFound) {
			s.writeAPIError(writer, request, http.StatusNotFound, "media.not_found", "媒体没有找到", nil)
		} else {
			s.writeAPIError(writer, request, http.StatusServiceUnavailable, "media.unavailable", "媒体暂时不可用", nil)
		}
		return
	}
	report, err := s.mediaUsage.Report(request.Context(), item, mediaReferenceAccess(principal), (page-1)*mediausage.PageSize)
	if err != nil {
		slog.Error("list media references API", "id", id, "error", err)
		s.writeAPIError(writer, request, http.StatusServiceUnavailable, "media.references_unavailable", "引用检查暂时不可用", nil)
		return
	}
	items := make([]adminMediaReference, 0, len(report.Items))
	for _, reference := range report.Items {
		items = append(items, mediaReferenceItem(reference))
	}
	pagination := adminContentPaginationAPIResponse{
		Page: page, PerPage: mediausage.PageSize, Total: report.Visible,
		Pages: max(1, (report.Visible+mediausage.PageSize-1)/mediausage.PageSize),
	}
	base := "/api/v1/admin/media/" + strconv.FormatInt(id, 10) + "/references?page="
	if page > 1 {
		pagination.Previous = base + strconv.Itoa(min(page-1, pagination.Pages))
	}
	if report.HasMore {
		pagination.Next = base + strconv.Itoa(page+1)
	}
	writeJSON(writer, http.StatusOK, struct {
		Items      []adminMediaReference             `json:"items"`
		Pagination adminContentPaginationAPIResponse `json:"pagination"`
		Total      int                               `json:"total"`
		Hidden     int                               `json:"hidden"`
		InUse      bool                              `json:"in_use"`
		CanDelete  bool                              `json:"can_delete"`
		CanTrash   bool                              `json:"can_trash"`
	}{items, pagination, report.Total, report.Hidden, report.Total > 0,
		report.Total == 0 && item.Status() == media.StatusTrash && !readOnly && principal.Can(auth.CapabilityMediaWrite),
		report.Total == 0 && item.Status() == media.StatusActive && !readOnly && principal.Can(auth.CapabilityMediaWrite)})
}
