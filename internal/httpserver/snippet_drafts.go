package httpserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/idempotency"
)

func (s *Server) snippetDraftInput(w http.ResponseWriter, r *http.Request) (content.SnippetDraftRequest, content.SnippetAccess, bool) {
	s.previewPrivateHeaders(w)
	a, ok := s.snippetPrincipal(w, r, true, false)
	if !ok {
		return content.SnippetDraftRequest{}, a, false
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return content.SnippetDraftRequest{}, a, false
	}
	id, ok := s.snippetID(w, r)
	if !ok {
		return content.SnippetDraftRequest{}, a, false
	}
	var u content.SnippetDraftRequest
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &u, 4096, "4 KiB"); !ok {
		return u, a, false
	}
	if u.PostID != id {
		s.writeAPIError(w, r, 400, "snippet_draft.id_mismatch", "文章编号与请求路径不一致", nil)
		return u, a, false
	}
	return u, a, true
}

func (s *Server) adminSnippetDraftPreviewAPI(w http.ResponseWriter, r *http.Request) {
	u, a, ok := s.snippetDraftInput(w, r)
	if !ok {
		return
	}
	plan, err := s.repository.PreviewSnippetDraft(r.Context(), u, a)
	if err != nil {
		s.snippetDraftError(w, r, err)
		return
	}
	writeJSON(w, 200, plan)
}

func (s *Server) adminSnippetDraftCreateAPI(w http.ResponseWriter, r *http.Request) {
	u, a, ok := s.snippetDraftInput(w, r)
	if !ok {
		return
	}
	request, err := content.NewSnippetDraftSubmission(u, a)
	if err != nil {
		s.snippetDraftError(w, r, err)
		return
	}
	result, err := s.repository.Submit(r.Context(), request)
	if err != nil {
		s.snippetDraftError(w, r, err)
		return
	}
	if result.Replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/admin/contents/%d", result.Receipt.PostID))
	writeJSON(w, 201, map[string]any{"id": result.Receipt.PostID, "version": result.Receipt.Version,
		"request_key": u.RequestKey, "replayed": result.Replayed, "editor_url": fmt.Sprintf("/admin/content/%d/edit/", result.Receipt.PostID)})
}

func (s *Server) snippetDraftError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 503, "result_unconfirmed", "结果暂时无法确认，请保留页面并核对同一次请求"
	switch {
	case errors.Is(err, content.ErrSnippetPermission):
		status, code, message = 403, "permission_denied", "当前账号无权生成或核对这份草稿"
	case errors.Is(err, content.ErrSnippetMaintenance):
		status, code, message = 503, "maintenance", "共享片段或编辑回执正在维护，请保留原请求并等待管理员处理"
	case errors.Is(err, content.ErrNotFound):
		status, code, message = 404, "source_unavailable", "文章或引用版本已不可用，请核对保存版本及片段共享状态"
	case errors.Is(err, content.ErrVersionConflict):
		status, code, message = 412, "version_conflict", "文章或片段已变化，请重新预览后确认"
	case errors.Is(err, idempotency.ErrConflict):
		status, code, message = 409, "request_conflict", "请求键已用于另一份内容，请核对原请求"
	case errors.Is(err, idempotency.ErrCapacity):
		status, code, message = 409, "capacity", "创建回执已达到保留上限，未生成草稿"
	case errors.Is(err, content.ErrSubmissionResultUnavailable):
		status, code, message = 409, "result_unavailable", "请求已完成，但原草稿已不可读取；不会重新生成"
	case errors.Is(err, content.ErrSlugConflict):
		status, code, message = 409, "slug_conflict", "草稿地址已被使用，请核对后重新预览"
	default:
		if fields := content.FieldErrors(err); len(fields) > 0 {
			s.writeAPIError(w, r, 422, "snippet_draft.invalid_input", "请检查正文和预览请求", map[string]any{"fields": fields})
			return
		}
		slog.Error("snippet draft result not confirmed", "error", err)
	}
	s.writeAPIError(w, r, status, "snippet_draft."+code, message, nil)
}
