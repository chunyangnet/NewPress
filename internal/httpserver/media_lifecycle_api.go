package httpserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/mediausage"
)

func mediaAPIETag(item media.Item) string {
	return fmt.Sprintf(`"media-%d-v%d"`, item.ID, item.Version)
}

func writeMediaAPISuccess(writer http.ResponseWriter, item media.Item) {
	writer.Header().Set("ETag", mediaAPIETag(item))
	writer.Header().Set("Cache-Control", "private, no-store")
	writeJSON(writer, http.StatusOK, mediaAPIItem(item))
}

func (s *Server) loadMediaAPIItem(writer http.ResponseWriter, request *http.Request) (media.Item, bool) {
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		s.writeAPIError(writer, request, http.StatusNotFound, "media.not_found", "媒体没有找到", nil)
		return media.Item{}, false
	}
	item, err := s.media.Get(id)
	if err != nil {
		s.handleMediaAPIError(writer, request, id, err)
		return media.Item{}, false
	}
	return item, true
}

func (s *Server) requireMediaAPIPrecondition(writer http.ResponseWriter, request *http.Request, item media.Item) bool {
	values := request.Header.Values("If-Match")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		s.writeAPIError(writer, request, http.StatusPreconditionRequired, "media.precondition_required", "写入媒体必须携带当前 If-Match ETag", map[string]any{
			"current_etag": mediaAPIETag(item), "current_version": item.Version,
		})
		return false
	}
	if !strongETagMatches(values[0], mediaAPIETag(item)) {
		s.writeMediaPreconditionFailed(writer, request, item)
		return false
	}
	return true
}

func (s *Server) writeMediaPreconditionFailed(writer http.ResponseWriter, request *http.Request, item media.Item) {
	writer.Header().Set("ETag", mediaAPIETag(item))
	s.writeAPIError(writer, request, http.StatusPreconditionFailed, "media.precondition_failed", "媒体已被其他请求更新，请重新读取后合并", map[string]any{
		"current_etag": mediaAPIETag(item), "current_version": item.Version,
	})
}

func (s *Server) adminMediaPatchAPI(writer http.ResponseWriter, request *http.Request) {
	if _, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityMediaWrite); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	current, ok := s.loadMediaAPIItem(writer, request)
	if !ok || !s.requireMediaAPIPrecondition(writer, request, current) {
		return
	}
	var payload struct {
		Title   apiOptionalString `json:"title"`
		AltText apiOptionalString `json:"alt_text"`
		Caption apiOptionalString `json:"caption"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(writer, request, &payload, 32<<10, "32 KiB"); !ok {
		return
	}
	if !payload.Title.Present && !payload.AltText.Present && !payload.Caption.Present {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.empty_patch", "PATCH 至少需要一个可写字段", nil)
		return
	}
	input := media.UpdateInput{Title: current.Title, AltText: current.AltText, Caption: current.Caption}
	if payload.Title.Present {
		input.Title = payload.Title.Value
	}
	if payload.AltText.Present {
		input.AltText = payload.AltText.Value
	}
	if payload.Caption.Present {
		input.Caption = payload.Caption.Value
	}
	updated, err := s.media.Update(current.ID, current.Version, input)
	if err != nil {
		s.handleMediaAPIError(writer, request, current.ID, err)
		return
	}
	writeMediaAPISuccess(writer, updated)
}

func (s *Server) adminMediaLifecycleAPI(action string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if _, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityMediaWrite); !ok {
			return
		}
		if _, ok := s.strictAPIQuery(writer, request, nil); !ok || !s.requireEmptyAPIBody(writer, request) {
			return
		}
		current, ok := s.loadMediaAPIItem(writer, request)
		if !ok || !s.requireMediaAPIPrecondition(writer, request, current) {
			return
		}
		var err error
		switch action {
		case "trash":
			err = s.mediaUsage.Trash(request.Context(), current.ID, current.Version)
		case "restore":
			err = s.mediaUsage.Restore(request.Context(), current.ID, current.Version)
		case "delete":
			err = s.mediaUsage.Delete(request.Context(), current.ID, current.Version)
		default:
			err = media.ErrInvalidInput
		}
		if err != nil {
			s.handleMediaAPIError(writer, request, current.ID, err)
			return
		}
		if action == "delete" {
			writer.Header().Set("Cache-Control", "private, no-store")
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		if updated, ok := s.loadMediaAPIItem(writer, request); ok {
			writeMediaAPISuccess(writer, updated)
		}
	}
}

func (s *Server) handleMediaAPIError(writer http.ResponseWriter, request *http.Request, id int64, err error) {
	switch {
	case errors.Is(err, media.ErrNotFound):
		s.writeAPIError(writer, request, http.StatusNotFound, "media.not_found", "媒体已移除，请重新选择", nil)
	case errors.Is(err, media.ErrVersionConflict):
		current, loadErr := s.media.Get(id)
		if loadErr != nil {
			s.handleMediaAPIError(writer, request, id, loadErr)
			return
		}
		s.writeMediaPreconditionFailed(writer, request, current)
	case errors.Is(err, media.ErrInvalidInput):
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "media.invalid_input", "媒体信息没有通过校验", map[string]any{"fields": mediaUploadFieldErrors(err)})
	case errors.Is(err, media.ErrTrashed):
		s.writeAPIError(writer, request, http.StatusConflict, "media.already_trashed", "媒体已在回收站中", nil)
	case errors.Is(err, media.ErrNotTrashed):
		s.writeAPIError(writer, request, http.StatusConflict, "media.not_trashed", "此操作要求媒体已在回收站中", nil)
	case errors.Is(err, mediausage.ErrInUse):
		s.writeAPIError(writer, request, http.StatusConflict, "media.in_use", "媒体仍被内容或设置引用，无法移入回收站或永久删除", nil)
	case errors.Is(err, mediausage.ErrUnavailable):
		s.writeAPIError(writer, request, http.StatusServiceUnavailable, "media.references_unavailable", "引用检查暂时不可用，文件已保留", nil)
	case errors.Is(err, media.ErrObjectCorrupt), errors.Is(err, media.ErrObjectMissing):
		s.writeAPIError(writer, request, http.StatusConflict, "media.object_unavailable", "原始文件缺失或校验不一致，无法恢复，请从站点备份恢复文件", nil)
	default:
		slog.Error("mutate media API", "id", id, "error", err)
		s.writeAPIError(writer, request, http.StatusInternalServerError, "media.update_failed", "媒体操作未完成，请重试", nil)
	}
}
