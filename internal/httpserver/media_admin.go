package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/mediausage"
)

const mediaPageSize = 24

type adminMediaView struct {
	Rows            []adminMediaRow
	KindFilters     []adminMediaFilter
	StatusFilters   []adminMediaFilter
	Status          string
	ListURL         string
	Search          string
	Kind            string
	Total           int
	TotalSize       string
	Page            int
	Pages           int
	PreviousURL     string
	NextURL         string
	HasPrevious     bool
	HasNext         bool
	UploadLimit     string
	UploadInput     media.UploadInput
	HasSelected     bool
	Selected        adminMediaRow
	EditInput       media.UpdateInput
	LibraryRevision string
	References      adminMediaReferencesView
	Image           mediaImageView
	FocalX          string
	FocalY          string
}

type adminMediaRow struct {
	Item           media.Item
	EditURL        string
	KindLabel      string
	SizeLabel      string
	DimensionLabel string
	CreatedLabel   string
	UpdatedLabel   string
	TrashedLabel   string
	PreviewURL     string
	IsTrashed      bool
	ChecksumShort  string
	IsImage        bool
	IsAudio        bool
	IsVideo        bool
	IsDocument     bool
}

type adminMediaFilter struct {
	Value  string
	Label  string
	Count  int
	URL    string
	Active bool
}

func (s *Server) adminMedia(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/admin/media/" {
		s.renderError(writer, http.StatusNotFound, "媒体管理页面没有找到")
		return
	}
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityMediaRead)
	if !ok {
		return
	}
	query, page, err := mediaQueryFromRequest(request)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "媒体筛选条件不正确")
		return
	}
	readOnly = readOnly || !principal.Can(auth.CapabilityMediaWrite)
	s.renderMediaLibrary(writer, request, http.StatusOK, principal, readOnly, query, page, nil, media.UpdateInput{}, media.UploadInput{}, nil, mediaNoticeFromQuery(request.URL.Query().Get("notice")))
}

func (s *Server) adminMediaEdit(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityMediaRead)
	if !ok {
		return
	}
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		s.renderError(writer, http.StatusNotFound, "媒体没有找到")
		return
	}
	item, err := s.media.Get(id)
	if errors.Is(err, media.ErrNotFound) {
		s.renderError(writer, http.StatusNotFound, "媒体没有找到")
		return
	}
	if err != nil {
		slog.Error("load media item", "id", id, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "媒体暂时不可用")
		return
	}
	query := media.Query{Status: item.Status(), Limit: mediaPageSize}
	readOnly = readOnly || !principal.Can(auth.CapabilityMediaWrite)
	s.renderMediaLibrary(writer, request, http.StatusOK, principal, readOnly, query, 1, &item,
		media.UpdateInput{Title: item.Title, AltText: item.AltText, Caption: item.Caption}, media.UploadInput{}, nil,
		mediaNoticeFromQuery(request.URL.Query().Get("notice")))
}

func (s *Server) adminMediaUpload(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityMediaWrite)
	if !ok {
		return
	}
	if readOnly || !s.auth.Configured() {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "admin_credentials_not_configured"})
		return
	}
	if !s.sameOriginRequest(request) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "cross_site_request_rejected"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "multipart_form_required"})
		return
	}
	maximum := s.effectiveMaxUploadBytes()
	request.Body = http.MaxBytesReader(writer, request.Body, maximum+(1<<20))
	if err := request.ParseMultipartForm(512 << 10); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.renderMediaUploadError(writer, request, principal, http.StatusRequestEntityTooLarge, media.UploadInput{}, map[string]string{"file": "上传文件超过部署上限"}, "上传体积超过限制")
			return
		}
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_media_form"})
		return
	}
	defer request.MultipartForm.RemoveAll()
	if invalidMultipartMediaForm(request.MultipartForm.Value, request.MultipartForm.File) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_form_shape"})
		return
	}
	request, ok = s.freshSessionRequest(writer, request)
	if !ok {
		return
	}
	principal, readOnly, ok = s.requireAdminPageCapability(writer, request, auth.CapabilityMediaWrite)
	if !ok {
		return
	}
	if readOnly || !s.auth.Configured() {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "admin_credentials_not_configured"})
		return
	}
	if !s.auth.CheckCSRF(principal, request.MultipartForm.Value["csrf_token"][0]) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return
	}
	input := media.UploadInput{
		UploadSource: "media-library",
		Filename:     request.MultipartForm.File["file"][0].Filename,
		Title:        request.MultipartForm.Value["title"][0], AltText: request.MultipartForm.Value["alt_text"][0],
		Caption: request.MultipartForm.Value["caption"][0], UploadedBy: principal.Username,
	}
	fileHeader := request.MultipartForm.File["file"][0]
	if fileHeader.Size > maximum {
		s.renderMediaUploadError(writer, request, principal, http.StatusRequestEntityTooLarge, input, map[string]string{"file": "上传文件超过部署上限"}, "上传体积超过限制")
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		slog.Error("open staged media upload", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "读取上传文件失败")
		return
	}
	item, uploadErr := s.media.Upload(request.Context(), file, input, maximum)
	_ = file.Close()
	switch {
	case uploadErr == nil:
		http.Redirect(writer, request, "/admin/media/"+strconv.FormatInt(item.ID, 10)+"/edit/?notice=uploaded", http.StatusSeeOther)
	case errors.Is(uploadErr, media.ErrDuplicate):
		http.Redirect(writer, request, "/admin/media/"+strconv.FormatInt(item.ID, 10)+"/edit/?notice=duplicate", http.StatusSeeOther)
	case errors.Is(uploadErr, media.ErrDuplicateTrash):
		http.Redirect(writer, request, "/admin/media/"+strconv.FormatInt(item.ID, 10)+"/edit/?notice=duplicate-trashed", http.StatusSeeOther)
	case errors.Is(uploadErr, media.ErrTooLarge):
		s.renderMediaUploadError(writer, request, principal, http.StatusRequestEntityTooLarge, input, map[string]string{"file": "上传文件超过部署上限"}, "上传体积超过限制")
	case errors.Is(uploadErr, media.ErrUnsupportedType):
		s.renderMediaUploadError(writer, request, principal, http.StatusUnprocessableEntity, input, map[string]string{"file": "仅支持 JPEG、PNG、GIF、WebP、PDF、MP3、OGG、WAV、MP4 与 WebM；拒绝 SVG、HTML 与未知二进制"}, "文件类型没有通过安全策略")
	case errors.Is(uploadErr, media.ErrInvalidInput):
		s.renderMediaUploadError(writer, request, principal, http.StatusUnprocessableEntity, input, mediaUploadFieldErrors(uploadErr), "媒体信息没有通过校验")
	default:
		slog.Error("store media upload", "filename", fileHeader.Filename, "error", uploadErr)
		s.renderError(writer, http.StatusInternalServerError, "保存媒体失败")
	}
}

func (s *Server) adminMediaUpdate(writer http.ResponseWriter, request *http.Request) {
	principal, form, ok := s.requireMediaWrite(writer, request)
	if !ok {
		return
	}
	if invalidMediaEditForm(form) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_form_shape"})
		return
	}
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		s.renderError(writer, http.StatusNotFound, "媒体没有找到")
		return
	}
	version, err := strconv.ParseInt(form.Get("version"), 10, 64)
	if err != nil || version <= 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_media_version"})
		return
	}
	action := form.Get("action")
	switch action {
	case "save":
		input := media.UpdateInput{Title: form.Get("title"), AltText: form.Get("alt_text"), Caption: form.Get("caption")}
		updated, updateErr := s.media.Update(id, version, input)
		if updateErr == nil {
			http.Redirect(writer, request, "/admin/media/"+strconv.FormatInt(updated.ID, 10)+"/edit/?notice=saved", http.StatusSeeOther)
			return
		}
		s.handleMediaMutationError(writer, request, principal, id, input, updateErr)
	case "trash", "restore", "delete":
		var mutationErr error
		target := "/admin/media/" + strconv.FormatInt(id, 10) + "/edit/"
		switch action {
		case "trash":
			mutationErr = s.mediaUsage.Trash(request.Context(), id, version)
			target += "?notice=trashed"
		case "restore":
			mutationErr = s.mediaUsage.Restore(request.Context(), id, version)
			target += "?notice=restored"
		case "delete":
			mutationErr = s.mediaUsage.Delete(request.Context(), id, version)
			target = "/admin/media/?status=trash&notice=deleted"
		}
		if mutationErr == nil {
			http.Redirect(writer, request, target, http.StatusSeeOther)
			return
		}
		current, loadErr := s.media.Get(id)
		if loadErr != nil {
			s.handleMediaLoadError(writer, id, loadErr)
			return
		}
		s.handleMediaMutationError(writer, request, principal, id, media.UpdateInput{Title: current.Title, AltText: current.AltText, Caption: current.Caption}, mutationErr)
	default:
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unknown_media_action"})
	}
}

func (s *Server) mediaAsset(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	name, format, err := mediaImageQuery(request)
	if err != nil {
		s.renderError(writer, 400, "图片参数不正确")
		return
	}
	var item media.Item
	var reader media.ReadSeekCloser
	if name != "" {
		item, reader, err = s.media.OpenVariant(id, name, format, false)
	} else {
		item, reader, err = s.media.Open(id)
	}
	if errors.Is(err, media.ErrNotFound) {
		http.NotFound(writer, request)
		return
	}
	if err != nil {
		slog.Error("open public media", "id", id, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "媒体文件暂时不可用")
		return
	}
	if request.URL.EscapedPath() != item.PublicURL {
		_ = reader.Close()
		http.NotFound(writer, request)
		return
	}
	defer reader.Close()
	if target, ok := s.objectStorage.RedirectURL(request.Context(), item); ok && request.Method == http.MethodGet {
		writer.Header().Set("Cache-Control", "private, no-store")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(writer, request, target, http.StatusTemporaryRedirect)
		return
	}
	// Bytes are immutable, but availability changes when an item enters trash.
	writer.Header().Set("Cache-Control", "public, no-cache")
	serveMediaObject(writer, request, item, reader)
}

func (s *Server) adminMediaPreview(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "private, no-store")
	if _, _, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityMediaRead); !ok {
		return
	}
	// Authorization helpers may set their own cache policy.
	writer.Header().Set("Cache-Control", "private, no-store")
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	name, format, err := mediaImageQuery(request)
	if err != nil {
		s.renderError(writer, 400, "图片参数不正确")
		return
	}
	var item media.Item
	var reader media.ReadSeekCloser
	if name != "" {
		item, reader, err = s.media.OpenVariant(id, name, format, true)
	} else {
		item, reader, err = s.media.OpenForManagement(id)
	}
	if err != nil {
		s.handleMediaLoadError(writer, id, err)
		return
	}
	defer reader.Close()
	request, ok := s.recheckAdminRead(writer, request, auth.CapabilityMediaRead)
	if !ok {
		return
	}
	writer.Header().Set("Cache-Control", "private, no-store")
	protected := privateResponse(writer, request, func() error { return s.adminReadAccess(request, auth.CapabilityMediaRead) })
	serveMediaObject(protected, request, item, reader)
}

func serveMediaObject(writer http.ResponseWriter, request *http.Request, item media.Item, reader media.ReadSeekCloser) {
	disposition := "inline"
	if item.Kind == media.KindDocument {
		disposition = "attachment"
		writer.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	}
	writer.Header().Set("Content-Type", item.MIMEType)
	writer.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": item.Filename}))
	writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	writer.Header().Set("ETag", `"sha256-`+item.ChecksumSHA256+`"`)
	if etagMatches(request.Header.Get("If-None-Match"), writer.Header().Get("ETag")) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	http.ServeContent(writer, request, item.Filename, item.CreatedAt, reader)
}

func (s *Server) renderMediaLibrary(writer http.ResponseWriter, request *http.Request, status int, principal auth.Principal, readOnly bool, query media.Query, page int, selected *media.Item, editInput media.UpdateInput, uploadInput media.UploadInput, fieldErrors map[string]string, notice adminNotice) {
	if query.Status == "" {
		query.Status = media.StatusActive
	}
	result, err := s.media.List(query)
	if err != nil {
		slog.Error("list media library", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "媒体列表暂时不可用")
		return
	}
	pageCount := max(1, (result.Total+mediaPageSize-1)/mediaPageSize)
	rows := make([]adminMediaRow, 0, len(result.Items))
	for _, item := range result.Items {
		rows = append(rows, s.adminMediaRow(item, request.Context()))
	}
	allCount := 0
	for _, count := range result.KindCounts {
		allCount += count
	}
	filters := []adminMediaFilter{{Label: "全部类型", Count: allCount, URL: mediaListURL(query.Status, "", query.Search, 1), Active: query.Kind == ""}}
	for _, filter := range []struct{ value, label string }{
		{media.KindImage, "图片"}, {media.KindDocument, "文档"}, {media.KindAudio, "音频"}, {media.KindVideo, "视频"},
	} {
		filters = append(filters, adminMediaFilter{
			Value: filter.value, Label: filter.label, Count: result.KindCounts[filter.value],
			URL: mediaListURL(query.Status, filter.value, query.Search, 1), Active: query.Kind == filter.value,
		})
	}
	states := []adminMediaFilter{}
	for _, state := range []struct{ value, label string }{{media.StatusActive, "媒体库"}, {media.StatusTrash, "回收站"}} {
		states = append(states, adminMediaFilter{Value: state.value, Label: state.label, Count: result.StatusCounts[state.value],
			URL: mediaListURL(state.value, query.Kind, query.Search, 1), Active: query.Status == state.value})
	}
	data := s.baseViewFor(writer, "媒体资源", "安全上传、去重、检索并复用站点图片与附件", request.URL.Path)
	data.BodyClass = "is-admin is-admin-media"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("media", principal, readOnly)
	data.Admin.FieldErrors = fieldErrors
	data.Admin.Notice = notice.Message
	data.Admin.NoticeTone = notice.Tone
	snapshot := s.media.Snapshot()
	data.Media = adminMediaView{
		Rows: rows, KindFilters: filters, StatusFilters: states, Status: query.Status,
		ListURL: mediaListURL(query.Status, "", "", 1), Search: query.Search, Kind: query.Kind, Total: result.Total,
		TotalSize: formatBytes(result.TotalBytes), Page: page, Pages: pageCount,
		HasPrevious: page > 1, HasNext: page < pageCount,
		PreviousURL: mediaListURL(query.Status, query.Kind, query.Search, max(1, page-1)),
		NextURL:     mediaListURL(query.Status, query.Kind, query.Search, min(pageCount, page+1)),
		UploadLimit: formatBytes(s.effectiveMaxUploadBytes()), UploadInput: uploadInput,
		LibraryRevision: snapshot.Revision,
	}
	if selected != nil {
		data.Media.HasSelected = true
		data.Media.Selected = s.adminMediaRow(*selected, request.Context())
		data.Media.EditInput = editInput
		data.Media.References = s.mediaReferencesView(request, *selected, principal)
		data.Media.Image = imageView(*selected)
		data.Media.FocalX = strconv.Itoa(data.Media.Image.FocalX)
		data.Media.FocalY = strconv.Itoa(data.Media.Image.FocalY)
		if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/process/") {
			data.Media.FocalX, data.Media.FocalY = request.PostForm.Get("focal_x"), request.PostForm.Get("focal_y")
		}
	}
	s.render(writer, status, "admin-media", data)
}

func (s *Server) adminMediaRow(item media.Item, ctx context.Context) adminMediaRow {
	location := s.siteLocation(ctx)
	dimensions := "—"
	if item.Width > 0 && item.Height > 0 {
		dimensions = fmt.Sprintf("%d × %d px", item.Width, item.Height)
	}
	checksum := item.ChecksumSHA256
	if len(checksum) > 16 {
		checksum = checksum[:16]
	}
	previewURL, trashedLabel := item.PublicURL, ""
	if item.Status() == media.StatusTrash {
		previewURL = "/admin/media/" + strconv.FormatInt(item.ID, 10) + "/preview/"
		trashedLabel = item.TrashedAt.In(location).Format("2006-01-02 15:04")
	}
	return adminMediaRow{
		Item: item, EditURL: "/admin/media/" + strconv.FormatInt(item.ID, 10) + "/edit/",
		KindLabel: mediaKindLabel(item.Kind), SizeLabel: formatBytes(item.SizeBytes), DimensionLabel: dimensions,
		CreatedLabel: item.CreatedAt.In(location).Format("2006-01-02 15:04"), UpdatedLabel: item.UpdatedAt.In(location).Format("2006-01-02 15:04"),
		PreviewURL: previewURL, TrashedLabel: trashedLabel, IsTrashed: item.Status() == media.StatusTrash,
		ChecksumShort: checksum, IsImage: item.Kind == media.KindImage, IsAudio: item.Kind == media.KindAudio,
		IsVideo: item.Kind == media.KindVideo, IsDocument: item.Kind == media.KindDocument,
	}
}

func (s *Server) renderMediaUploadError(writer http.ResponseWriter, request *http.Request, principal auth.Principal, status int, input media.UploadInput, fields map[string]string, message string) {
	query := media.Query{Limit: mediaPageSize}
	s.renderMediaLibrary(writer, request, status, principal, false, query, 1, nil, media.UpdateInput{}, input, fields, adminNotice{message, "error"})
}

func (s *Server) handleMediaMutationError(writer http.ResponseWriter, request *http.Request, principal auth.Principal, id int64, input media.UpdateInput, err error) {
	if errors.Is(err, media.ErrNotFound) {
		s.renderError(writer, http.StatusNotFound, "媒体没有找到")
		return
	}
	current, loadErr := s.media.Get(id)
	if loadErr != nil {
		s.handleMediaLoadError(writer, id, loadErr)
		return
	}
	query := media.Query{Status: current.Status(), Limit: mediaPageSize}
	if errors.Is(err, media.ErrVersionConflict) {
		s.renderMediaLibrary(writer, request, http.StatusConflict, principal, false, query, 1, &current, input, media.UploadInput{}, nil,
			adminNotice{"这项媒体已在其他会话中更新；已保留你的输入，请核对后再次保存。", "error"})
		return
	}
	if errors.Is(err, media.ErrInvalidInput) {
		s.renderMediaLibrary(writer, request, http.StatusUnprocessableEntity, principal, false, query, 1, &current, input, media.UploadInput{}, mediaEditFieldErrors(input),
			adminNotice{"媒体信息没有通过服务端校验", "error"})
		return
	}
	if errors.Is(err, mediausage.ErrInUse) || errors.Is(err, mediausage.ErrUnavailable) {
		status, message := http.StatusConflict, "媒体仍被内容或设置引用，暂时无法移入回收站或永久删除。"
		if errors.Is(err, mediausage.ErrUnavailable) {
			status, message = http.StatusServiceUnavailable, "引用检查暂时不可用，未删除文件，请稍后重试。"
			slog.Error("check media references before deletion", "id", id, "error", err)
		}
		s.renderMediaLibrary(writer, request, status, principal, false, query, 1, &current, input, media.UploadInput{}, nil, adminNotice{message, "error"})
		return
	}
	if errors.Is(err, media.ErrTrashed) || errors.Is(err, media.ErrNotTrashed) || errors.Is(err, media.ErrObjectCorrupt) || errors.Is(err, media.ErrObjectMissing) {
		message := "媒体状态已变化，请核对后重新操作。永久删除前必须先移入回收站。"
		if errors.Is(err, media.ErrObjectCorrupt) || errors.Is(err, media.ErrObjectMissing) {
			message = "原始文件缺失或校验不一致，无法恢复；请从站点备份恢复文件。"
		}
		s.renderMediaLibrary(writer, request, http.StatusConflict, principal, false, query, 1, &current, input, media.UploadInput{}, nil, adminNotice{message, "error"})
		return
	}
	slog.Error("mutate media item", "id", id, "error", err)
	s.renderError(writer, http.StatusInternalServerError, "更新媒体失败")
}

func (s *Server) handleMediaLoadError(writer http.ResponseWriter, id int64, err error) {
	if errors.Is(err, media.ErrNotFound) {
		s.renderError(writer, http.StatusNotFound, "媒体没有找到")
		return
	}
	slog.Error("load media item", "id", id, "error", err)
	s.renderError(writer, http.StatusInternalServerError, "媒体暂时不可用")
}

func (s *Server) requireMediaWrite(writer http.ResponseWriter, request *http.Request) (auth.Principal, url.Values, bool) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityMediaWrite)
	if !ok {
		return auth.Principal{}, nil, false
	}
	if readOnly || !s.auth.Configured() {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "admin_credentials_not_configured"})
		return auth.Principal{}, nil, false
	}
	if !s.sameOriginRequest(request) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "cross_site_request_rejected"})
		return auth.Principal{}, nil, false
	}
	if !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "form_content_type_required"})
		return auth.Principal{}, nil, false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 256<<10)
	if err := request.ParseForm(); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_media_form"})
		return auth.Principal{}, nil, false
	}
	form := request.PostForm
	if len(form["csrf_token"]) != 1 || !s.auth.CheckCSRF(principal, form.Get("csrf_token")) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return auth.Principal{}, nil, false
	}
	return principal, form, true
}

func mediaQueryFromRequest(request *http.Request) (media.Query, int, error) {
	page, err := positiveQueryInt(request.URL.Query().Get("page"), 1, 100000)
	if err != nil {
		return media.Query{}, 0, err
	}
	query := media.Query{
		Status: request.URL.Query().Get("status"), Kind: request.URL.Query().Get("kind"), Search: request.URL.Query().Get("q"),
		Limit: mediaPageSize, Offset: (page - 1) * mediaPageSize,
	}
	if query.Status == "" {
		query.Status = media.StatusActive
	}
	if query.Status != media.StatusActive && query.Status != media.StatusTrash {
		return media.Query{}, 0, errors.New("invalid media status")
	}
	if query.Kind != "" && query.Kind != media.KindImage && query.Kind != media.KindDocument && query.Kind != media.KindAudio && query.Kind != media.KindVideo {
		return media.Query{}, 0, errors.New("invalid media kind")
	}
	query.Search = strings.TrimSpace(query.Search)
	if !utf8.ValidString(query.Search) || utf8.RuneCountInString(query.Search) > 100 {
		return media.Query{}, 0, errors.New("invalid media search")
	}
	return query, page, nil
}

func invalidMultipartMediaForm(values map[string][]string, files map[string][]*multipart.FileHeader) bool {
	allowedValues := map[string]bool{"csrf_token": true, "title": true, "alt_text": true, "caption": true}
	if source, present := values["upload_source"]; present {
		if len(source) != 1 {
			return true
		}
		switch source[0] {
		case "media-api", "editor-body", "editor-cover", "editor-component", "series-cover", "settings-image", "link-logo", "avatar":
		default:
			return true
		}
		allowedValues["upload_source"] = true
	}
	if len(values) != len(allowedValues) || len(files) != 1 {
		return true
	}
	for key, candidates := range values {
		if !allowedValues[key] || len(candidates) != 1 {
			return true
		}
	}
	return len(files["file"]) != 1 || files["file"][0] == nil
}

func invalidMediaEditForm(form url.Values) bool {
	allowed := map[string]bool{"csrf_token": true, "version": true, "action": true, "title": true, "alt_text": true, "caption": true}
	if len(form) != len(allowed) {
		return true
	}
	for key, candidates := range form {
		if !allowed[key] || len(candidates) != 1 {
			return true
		}
	}
	return false
}

func mediaUploadFieldErrors(err error) map[string]string {
	message := err.Error()
	fields := make(map[string]string)
	switch {
	case strings.Contains(message, "title"):
		fields["title"] = "标题最多 160 个字符"
	case strings.Contains(message, "alt_text"):
		fields["alt_text"] = "替代文本最多 250 个字符"
	case strings.Contains(message, "caption"):
		fields["caption"] = "说明最多 2000 个字符"
	case strings.Contains(message, "dimensions"):
		fields["file"] = "图片尺寸超过安全上限或文件结构无效"
	default:
		fields["file"] = "文件内容或元数据无效"
	}
	return fields
}

func mediaEditFieldErrors(input media.UpdateInput) map[string]string {
	fields := make(map[string]string)
	if strings.TrimSpace(input.Title) == "" || len([]rune(input.Title)) > 160 {
		fields["title"] = "标题必填且最多 160 个字符"
	}
	if len([]rune(input.AltText)) > 250 {
		fields["alt_text"] = "替代文本最多 250 个字符"
	}
	if len([]rune(input.Caption)) > 2000 {
		fields["caption"] = "说明最多 2000 个字符"
	}
	return fields
}

func mediaListURL(status, kind, search string, page int) string {
	values := make(url.Values)
	if status == media.StatusTrash {
		values.Set("status", status)
	}
	if kind != "" {
		values.Set("kind", kind)
	}
	if search != "" {
		values.Set("q", search)
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	if encoded := values.Encode(); encoded != "" {
		return "/admin/media/?" + encoded
	}
	return "/admin/media/"
}

func mediaKindLabel(kind string) string {
	switch kind {
	case media.KindImage:
		return "图片"
	case media.KindDocument:
		return "文档"
	case media.KindAudio:
		return "音频"
	case media.KindVideo:
		return "视频"
	default:
		return "媒体"
	}
}

func mediaNoticeFromQuery(value string) adminNotice {
	switch value {
	case "uploaded":
		return adminNotice{"媒体已完成类型识别、SHA-256 校验并原子写入资源库", "success"}
	case "duplicate":
		return adminNotice{"检测到相同内容，已复用现有媒体对象，避免重复占用空间", "success"}
	case "duplicate-trashed":
		return adminNotice{"相同文件已在回收站中，请恢复这项媒体后使用。", "warning"}
	case "saved":
		return adminNotice{"标题、替代文本和说明已更新，公开文件地址保持稳定", "success"}
	case "processed":
		return adminNotice{"已生成六组图片尺寸及 WebP，网页将自动选择合适版本。", "success"}
	case "deleted":
		return adminNotice{"媒体及原始文件已永久删除。", "success"}
	case "trashed":
		return adminNotice{"媒体已移入回收站，原始文件已保留。", "success"}
	case "restored":
		return adminNotice{"媒体已恢复，原公开地址保持不变。", "success"}
	default:
		return adminNotice{}
	}
}

func (s *Server) effectiveMaxUploadBytes() int64 {
	if s.siteSettings != nil {
		if limit := s.siteSettings.Snapshot().Settings.Writing.MaxUploadMiB; limit > 0 {
			return int64(limit) << 20
		}
	}
	if s.config.MaxUploadBytes > 0 {
		return s.config.MaxUploadBytes
	}
	return 64 << 20
}

func formatBytes(value int64) string {
	if value < 0 {
		value = 0
	}
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor, exponent := int64(unit), 0
	for quotient := value / unit; quotient >= unit && exponent < 4; quotient /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}
