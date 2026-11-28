package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/site"
)

func createAPIInput(request adminContentMutationAPIRequest, principal auth.Principal, now time.Time, defaults site.WritingSettings) (content.PostInput, map[string]string) {
	input := content.PostInput{
		Type: content.TypePost, Status: defaults.DefaultStatus, Category: defaults.DefaultCategory,
		AuthorID: principal.UserID, AuthorName: principal.Label(), Actor: principal.Username,
	}
	if request.Type.Present {
		input.Type = request.Type.Value
	}
	if input.Type == content.TypePage {
		input.Category = ""
		if defaults.PageCommentsEnabled {
			input.CommentStatus = content.CommentOpen
		} else {
			input.CommentStatus = content.CommentClosed
		}
	} else {
		if defaults.PostCommentsEnabled {
			input.CommentStatus = content.CommentOpen
		} else {
			input.CommentStatus = content.CommentClosed
		}
	}
	applyContentMutationAPIRequest(&input, request)
	return normalizeAPIInput(input, request.PublishedAt, now)
}

func (s *Server) requireAdminContentAPIEnabled(writer http.ResponseWriter, request *http.Request) bool {
	if s.siteSettings.Snapshot().Settings.API.AdminContentEnabled {
		return true
	}
	s.writeAPIError(writer, request, http.StatusNotFound, "request.not_found", "资源不存在", nil)
	return false
}

func patchAPIInput(post content.Post, request adminContentMutationAPIRequest, principal auth.Principal, now time.Time) (content.PostInput, map[string]string) {
	input := inputFromPost(post)
	input.Actor = principal.Username
	applyContentMutationAPIRequest(&input, request)
	return normalizeAPIInput(input, request.PublishedAt, now)
}

func applyContentMutationAPIRequest(input *content.PostInput, request adminContentMutationAPIRequest) {
	if request.Resources != nil {
		input.Resources = request.Resources
	}
	if request.Pinned != nil {
		pinned := *request.Pinned
		input.Pinned = &pinned
	}
	if request.ArticleBadge != nil {
		badge := *request.ArticleBadge
		input.ArticleBadge = &badge
	}
	if request.Meta != nil && request.Meta.AIProMax.Present {
		input.AIPolicy = request.Meta.AIProMax.Value
		if input.AIPolicy == "" {
			input.AIPolicy = "inherit"
		}
	}
	if request.Type.Present {
		input.Type = request.Type.Value
	}
	if request.Status.Present {
		input.Status = request.Status.Value
	}
	if request.Slug.Present {
		input.Slug = request.Slug.Value
	}
	if request.Title.Present {
		input.Title = request.Title.Value
	}
	if request.Excerpt.Present {
		input.Excerpt = request.Excerpt.Value
	}
	if request.ContentSource.Present {
		input.ContentSource = request.ContentSource.Value
	}
	if request.CoverURL.Present {
		input.CoverURL = request.CoverURL.Value
	}
	if request.CategoryIDs.Present {
		input.CategoryIDs = append([]int64(nil), request.CategoryIDs.Value...)
		input.Category = ""
	}
	if request.Tags.Present {
		input.TagNames = append([]string(nil), request.Tags.Value...)
	}
	if request.CommentStatus.Present {
		input.CommentStatus = request.CommentStatus.Value
	}
	if request.PublishedAt.Present && request.PublishedAt.Null {
		input.PublishedAt = time.Time{}
	}
}

func normalizeAPIInput(input content.PostInput, publishedAt apiOptionalTimeText, now time.Time) (content.PostInput, map[string]string) {
	parseFields := make(map[string]string)
	if publishedAt.Present && !publishedAt.Null {
		parsed, err := time.Parse(time.RFC3339, publishedAt.Value)
		if err != nil {
			parseFields["published_at"] = "发布时间必须是带时区的 RFC3339 时间或 null"
		} else {
			input.PublishedAt = parsed.UTC()
		}
	}
	normalized, fields := content.NormalizePostInput(input, now)
	if normalized.Type == content.TypeFlash {
		fields["type"] = "请使用独立快报管理接口"
	}
	for key, message := range parseFields {
		fields[key] = message
	}
	return normalized, fields
}

func (s *Server) loadAdminContentAPI(writer http.ResponseWriter, request *http.Request) (content.Post, bool) {
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		s.writeAPIError(writer, request, http.StatusNotFound, "content.not_found", "内容没有找到", nil)
		return content.Post{}, false
	}
	post, err := s.repository.GetByID(request.Context(), id)
	if errors.Is(err, content.ErrNotFound) {
		s.writeAPIError(writer, request, http.StatusNotFound, "content.not_found", "内容没有找到", nil)
		return content.Post{}, false
	}
	if err != nil {
		slog.Error("load content API", "id", id, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "content.unavailable", "内容暂时不可用", nil)
		return content.Post{}, false
	}
	return post, true
}

func (s *Server) requireContentAPIObjectRead(writer http.ResponseWriter, request *http.Request, principal auth.Principal, post content.Post) bool {
	if canReadContentObject(principal, post) {
		return true
	}
	s.writeAPIError(writer, request, http.StatusForbidden, "content.owner_required", "当前账号不能访问其他作者的内容", nil)
	return false
}

func (s *Server) requireContentAPIObjectWrite(writer http.ResponseWriter, request *http.Request, principal auth.Principal, post content.Post) bool {
	if canWriteContentObject(principal, post) {
		return true
	}
	s.writeAPIError(writer, request, http.StatusForbidden, "content.owner_required", "当前账号不能修改其他作者的内容", nil)
	return false
}

func (s *Server) requireContentAPIPrecondition(writer http.ResponseWriter, request *http.Request, post content.Post) bool {
	if _, ok := s.recheckContentAPIWrite(writer, request, post.ID); !ok {
		return false
	}
	ifMatch := strings.TrimSpace(request.Header.Get("If-Match"))
	if ifMatch == "" {
		s.writeAPIError(writer, request, http.StatusPreconditionRequired, "content.precondition_required", "写入内容必须携带当前 If-Match ETag", map[string]any{
			"current_etag": contentAPIETag(post), "current_version": post.Version,
		})
		return false
	}
	if !strongETagMatches(ifMatch, contentAPIETag(post)) {
		s.writeContentPreconditionFailed(writer, request, post)
		return false
	}
	return true
}

func strongETagMatches(header, current string) bool {
	for _, candidate := range strings.Split(header, ",") {
		if strings.TrimSpace(candidate) == current {
			return true
		}
	}
	return false
}

func (s *Server) writeContentPreconditionFailed(writer http.ResponseWriter, request *http.Request, post content.Post) {
	if _, ok := s.recheckContentAPIWrite(writer, request, post.ID); !ok {
		return
	}
	writer.Header().Set("ETag", contentAPIETag(post))
	s.writeAPIError(writer, request, http.StatusPreconditionFailed, "content.precondition_failed", "内容已被其他编辑更新，请重新读取后合并", map[string]any{
		"current_etag": contentAPIETag(post), "current_version": post.Version,
	})
}

func (s *Server) handleContentCreateAPIError(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, content.ErrSnippetMaintenance) {
		s.writeAPIError(writer, request, 503, "content.maintenance", "编辑回执正在维护，请保留当前输入及原请求，等待管理员处理", nil)
		return
	}
	if errors.Is(err, content.ErrSlugConflict) {
		s.writeAPIError(writer, request, http.StatusConflict, "content.slug_conflict", "同类型内容已使用这个别名", map[string]any{
			"fields": map[string]string{"slug": "同类型内容已使用这个别名"},
		})
		return
	}
	if fields := content.FieldErrors(err); len(fields) > 0 {
		s.writeContentValidationAPIError(writer, request, fields)
		return
	}
	slog.Error("create content API", "error", err, "request_id", request.Header.Get("X-Request-ID"))
	s.writeAPIError(writer, request, http.StatusInternalServerError, "content.create_failed", "创建内容失败", nil)
}

func (s *Server) handleContentMutationAPIError(writer http.ResponseWriter, request *http.Request, id int64, err error) {
	if errors.Is(err, content.ErrSnippetMaintenance) {
		s.writeAPIError(writer, request, 503, "content.maintenance", "编辑回执正在维护，请保留当前输入及原请求，等待管理员处理", nil)
		return
	}
	if errors.Is(err, content.ErrRedirectConflict) {
		s.writeAPIError(writer, request, 409, "content.redirect_conflict", "旧地址已被其他规则占用，请在搜索与链接中核对", nil)
		return
	}
	if errors.Is(err, content.ErrNotFound) {
		s.writeAPIError(writer, request, http.StatusNotFound, "content.not_found", "内容或修订记录没有找到", nil)
		return
	}
	if errors.Is(err, content.ErrVersionConflict) {
		current, loadErr := s.repository.GetByID(request.Context(), id)
		if loadErr == nil {
			s.writeContentPreconditionFailed(writer, request, current)
			return
		}
		if errors.Is(loadErr, content.ErrNotFound) {
			s.writeAPIError(writer, request, http.StatusNotFound, "content.not_found", "内容没有找到", nil)
			return
		}
		slog.Error("load current content after API conflict", "id", id, "error", loadErr, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "content.unavailable", "无法读取当前内容版本", nil)
		return
	}
	if errors.Is(err, content.ErrSlugConflict) {
		s.writeAPIError(writer, request, http.StatusConflict, "content.slug_conflict", "同类型内容已使用这个别名", map[string]any{
			"fields": map[string]string{"slug": "同类型内容已使用这个别名"},
		})
		return
	}
	if fields := content.FieldErrors(err); len(fields) > 0 {
		s.writeContentValidationAPIError(writer, request, fields)
		return
	}
	slog.Error("mutate content API", "id", id, "error", err, "request_id", request.Header.Get("X-Request-ID"))
	s.writeAPIError(writer, request, http.StatusInternalServerError, "content.update_failed", "更新内容失败", nil)
}

func (s *Server) writeContentValidationAPIError(writer http.ResponseWriter, request *http.Request, fields map[string]string) {
	s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "content.validation_failed", "内容没有通过服务端校验", map[string]any{"fields": fields})
}

func (s *Server) handleIdempotencyAPIError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, content.ErrSnippetMaintenance):
		s.writeAPIError(writer, request, 503, "content.maintenance", "编辑回执正在维护，请保留当前输入及原请求，等待管理员处理", nil)
	case errors.Is(err, errIdempotencyConflict):
		s.writeAPIError(writer, request, http.StatusConflict, "request.idempotency_conflict", "同一 Idempotency-Key 已用于不同请求", nil)
	case errors.Is(err, errIdempotencyCapacity):
		writer.Header().Set("Retry-After", "1")
		s.writeAPIError(writer, request, http.StatusServiceUnavailable, "request.idempotency_unavailable", "幂等注册表暂时繁忙，请稍后重试", nil)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		s.writeAPIError(writer, request, http.StatusRequestTimeout, "request.cancelled", "等待同一幂等请求完成时连接已取消", nil)
	default:
		slog.Error("begin content idempotency", "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "request.idempotency_failed", "无法建立幂等写入保护", nil)
	}
}

func strictIdempotencyKey(request *http.Request) (string, error) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || len(values[0]) < 8 || len(values[0]) > 128 {
		return "", errors.New("invalid idempotency key length")
	}
	for _, character := range values[0] {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == ':' || character == '-' {
			continue
		}
		return "", errors.New("invalid idempotency key character")
	}
	return values[0], nil
}
