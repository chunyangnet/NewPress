package httpserver

import (
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/media"
)

type adminMediaAPIItem struct {
	ID        int64          `json:"id"`
	Version   int64          `json:"version"`
	Status    string         `json:"status"`
	ETag      string         `json:"etag"`
	PublicURL string         `json:"public_url"`
	Filename  string         `json:"filename"`
	Title     string         `json:"title"`
	AltText   string         `json:"alt_text"`
	Caption   string         `json:"caption"`
	MIMEType  string         `json:"mime_type"`
	Kind      string         `json:"kind"`
	SizeBytes int64          `json:"size_bytes"`
	Width     int            `json:"width"`
	Height    int            `json:"height"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	TrashedAt time.Time      `json:"trashed_at,omitzero"`
	Image     mediaImageView `json:"image"`
}

type adminMediaListAPIResponse struct {
	Items          []adminMediaAPIItem               `json:"items"`
	Pagination     adminContentPaginationAPIResponse `json:"pagination"`
	CanUpload      bool                              `json:"can_upload"`
	MaxUploadBytes int64                             `json:"max_upload_bytes"`
	StatusCounts   map[string]int                    `json:"status_counts"`
}

func mediaAPIItem(item media.Item) adminMediaAPIItem {
	return adminMediaAPIItem{
		ID: item.ID, Version: item.Version, PublicURL: item.PublicURL, Filename: item.Filename,
		Status: item.Status(), ETag: mediaAPIETag(item), UpdatedAt: item.UpdatedAt, TrashedAt: item.TrashedAt,
		Title: item.Title, AltText: item.AltText, Caption: item.Caption, MIMEType: item.MIMEType,
		Kind: item.Kind, SizeBytes: item.SizeBytes, Width: item.Width, Height: item.Height, CreatedAt: item.CreatedAt,
		Image: imageView(item),
	}
}

func (s *Server) adminMediaListAPI(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityMediaRead)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, map[string]bool{"status": true, "kind": true, "q": true, "page": true}); !ok {
		return
	}
	query, page, err := mediaQueryFromRequest(request)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "media.invalid_query", "媒体筛选条件不正确", nil)
		return
	}
	result, err := s.media.List(query)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusInternalServerError, "media.unavailable", "媒体库暂时不可用", nil)
		return
	}
	items := make([]adminMediaAPIItem, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, mediaAPIItem(item))
	}
	pages := max(1, (result.Total+mediaPageSize-1)/mediaPageSize)
	pagination := adminContentPaginationAPIResponse{Page: page, PerPage: mediaPageSize, Total: result.Total, Pages: pages}
	if page > 1 {
		pagination.Previous = mediaAPIListURL(query, min(page-1, pages))
	}
	if page < pages {
		pagination.Next = mediaAPIListURL(query, page+1)
	}
	writeJSON(writer, http.StatusOK, adminMediaListAPIResponse{
		Items: items, Pagination: pagination, CanUpload: !readOnly && principal.Can(auth.CapabilityMediaWrite),
		MaxUploadBytes: s.effectiveMaxUploadBytes(),
		StatusCounts:   result.StatusCounts,
	})
}

func mediaAPIListURL(query media.Query, page int) string {
	values := url.Values{"page": {strconv.Itoa(page)}}
	if query.Status == media.StatusTrash {
		values.Set("status", query.Status)
	}
	if query.Kind != "" {
		values.Set("kind", query.Kind)
	}
	if query.Search != "" {
		values.Set("q", query.Search)
	}
	return "/api/v1/admin/media?" + values.Encode()
}

func (s *Server) adminMediaItemAPI(writer http.ResponseWriter, request *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityMediaRead); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, map[string]bool{}); !ok {
		return
	}
	item, ok := s.loadMediaAPIItem(writer, request)
	if !ok {
		return
	}
	writeMediaAPISuccess(writer, item)
}

func (s *Server) adminMediaUploadAPI(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityMediaWrite)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, map[string]bool{}); !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		s.writeAPIError(writer, request, http.StatusUnsupportedMediaType, "media.multipart_required", "上传请求必须使用 multipart/form-data", nil)
		return
	}
	maximum := s.effectiveMaxUploadBytes()
	request.Body = http.MaxBytesReader(writer, request.Body, maximum+(1<<20))
	if err := request.ParseMultipartForm(512 << 10); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeAPIError(writer, request, http.StatusRequestEntityTooLarge, "media.too_large", "上传文件超过站点上限", nil)
		} else {
			s.writeAPIError(writer, request, http.StatusBadRequest, "media.invalid_form", "上传表单格式不正确", nil)
		}
		return
	}
	defer request.MultipartForm.RemoveAll()
	if invalidMultipartMediaForm(request.MultipartForm.Value, request.MultipartForm.File) {
		s.writeAPIError(writer, request, http.StatusBadRequest, "media.invalid_form", "上传字段未知、重复或缺失", nil)
		return
	}
	request, ok = s.freshSessionRequest(writer, request)
	if !ok {
		return
	}
	principal, ok = s.requireAdminJSONWrite(writer, request, auth.CapabilityMediaWrite)
	if !ok {
		return
	}
	values := request.MultipartForm.Value
	if !s.auth.CheckCSRF(principal, values["csrf_token"][0]) {
		s.writeAPIError(writer, request, http.StatusForbidden, "auth.csrf_validation_failed", "上传表单已失效，请重新打开编辑器", nil)
		return
	}
	header := request.MultipartForm.File["file"][0]
	if header.Size > maximum {
		s.writeAPIError(writer, request, http.StatusRequestEntityTooLarge, "media.too_large", "上传文件超过站点上限", nil)
		return
	}
	file, err := header.Open()
	if err != nil {
		s.writeAPIError(writer, request, http.StatusInternalServerError, "media.read_failed", "无法读取上传文件", nil)
		return
	}
	defer file.Close()
	source := "media-api"
	if values["upload_source"] != nil {
		source = values["upload_source"][0]
	}
	item, err := s.media.Upload(request.Context(), file, media.UploadInput{
		UploadSource: source,
		Filename:     header.Filename, Title: values["title"][0], AltText: values["alt_text"][0],
		Caption: values["caption"][0], UploadedBy: principal.Username,
	}, maximum)
	switch {
	case err == nil, errors.Is(err, media.ErrDuplicate):
		status := http.StatusCreated
		duplicate := errors.Is(err, media.ErrDuplicate)
		if duplicate {
			status = http.StatusOK
		}
		writer.Header().Set("Location", "/api/v1/admin/media/"+strconv.FormatInt(item.ID, 10))
		writer.Header().Set("ETag", mediaAPIETag(item))
		writeJSON(writer, status, struct {
			Item      adminMediaAPIItem `json:"item"`
			Duplicate bool              `json:"duplicate"`
		}{Item: mediaAPIItem(item), Duplicate: duplicate})
	case errors.Is(err, media.ErrDuplicateTrash):
		s.writeAPIError(writer, request, http.StatusConflict, "media.duplicate_trashed", "相同文件已在回收站中，请恢复后使用", map[string]any{"media_id": item.ID})
	case errors.Is(err, media.ErrTooLarge):
		s.writeAPIError(writer, request, http.StatusRequestEntityTooLarge, "media.too_large", "上传文件超过站点上限", nil)
	case errors.Is(err, media.ErrUnsupportedType):
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "media.unsupported_type", "文件类型未通过校验，图片支持 JPEG、PNG、GIF 和 WebP", nil)
	case errors.Is(err, media.ErrInvalidInput):
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "media.invalid_input", "文件内容或媒体信息不正确", mediaUploadFieldErrors(err))
	default:
		slog.Error("upload media through API", "error", err)
		s.writeAPIError(writer, request, http.StatusInternalServerError, "media.upload_failed", "上传未能完成，请重试", nil)
	}
}
