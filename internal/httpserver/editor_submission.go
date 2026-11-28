package httpserver

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/idempotency"
)

func (s *Server) adminEditorialSubmission(writer http.ResponseWriter, request *http.Request, principal auth.Principal, form url.Values, current content.Post) {
	s.previewPrivateHeaders(writer)
	reject := func(status int, code, message string, fields map[string]string) {
		payload := map[string]any{"saved": false, "error": code, "message": message, "fields": fields}
		if fields["published_at"] != "" {
			payload["published_timezone"] = s.siteLocation(request.Context()).String()
		}
		writeJSON(writer, status, payload)
	}
	if len(request.Header.Values("X-NewPress-Editor")) != 1 || request.Header.Get("X-NewPress-Editor") != "1" {
		reject(http.StatusBadRequest, "invalid_editor_protocol", "保存请求不正确，请重新打开编辑器。", nil)
		return
	}
	key, err := strictIdempotencyKey(request)
	if err != nil || key == "" {
		reject(http.StatusBadRequest, "invalid_submission_key", "保存请求缺少有效的重试标识。", nil)
		return
	}
	owner, ok := autosaveOwnerKey(principal)
	if !ok {
		reject(http.StatusForbidden, "identity_required", "当前身份无法保存。", nil)
		return
	}
	values := make(url.Values, len(form))
	for name, value := range form {
		if name != "csrf_token" {
			values[name] = append([]string(nil), value...)
		}
	}
	submission := content.Submission{
		Key:         fmt.Sprintf("%x", sha256.Sum256([]byte(owner+"\x00editor\x00"+request.URL.Path+"\x00"+key))),
		Fingerprint: fmt.Sprintf("%x", sha256.Sum256([]byte(values.Encode()))),
		Action:      form.Get("action"), PostID: current.ID, Actor: principal.Username,
	}
	// Confirm a committed request before time-sensitive field/media validation.
	// Refresh authority after lookup, without comparing the current version to
	// the receipt: later edits do not invalidate a committed request's receipt.
	previous, lookupErr := s.repository.FindSubmission(request.Context(), submission.Key, submission.Fingerprint)
	// Creation receipts belong to the request owner even if the new object was
	// subsequently removed; never turn a known creation into a new submission.
	if !s.recheckContentWrite(writer, request, current.ID) {
		return
	}
	if result, err := previous, lookupErr; !errors.Is(err, content.ErrNotFound) {
		if errors.Is(err, idempotency.ErrConflict) {
			reject(http.StatusConflict, "submission_conflict", "同一重试标识对应不同内容；请核对原保存结果。", nil)
		} else if err != nil {
			reject(http.StatusServiceUnavailable, "result_unconfirmed", "保存结果暂时无法确认，请保留页面并重试同一次保存。", nil)
		} else {
			s.writeEditorSubmissionResult(writer, key, result)
		}
		return
	}
	if current.ID == 0 {
		if submission.Action != "save" {
			reject(http.StatusBadRequest, "invalid_action", "新内容只能先保存。", nil)
			return
		}
		submission.Action = "create"
	} else {
		submission.Version, err = strconv.ParseInt(form.Get("version"), 10, 64)
		if err != nil || submission.Version <= 0 {
			reject(http.StatusBadRequest, "invalid_version", "保存版本不正确。", nil)
			return
		}
	}
	if strings.HasPrefix(submission.Action, "restore_revision-") {
		submission.RevisionID, err = strconv.ParseInt(strings.TrimPrefix(submission.Action, "restore_revision-"), 10, 64)
		if err != nil || submission.RevisionID <= 0 {
			reject(http.StatusBadRequest, "invalid_revision", "修订编号不正确。", nil)
			return
		}
		submission.Action = "restore_revision"
	}
	if !content.ValidSubmissionAction(submission.Action) || (current.ID != 0 && submission.Action == "create") {
		reject(http.StatusBadRequest, "invalid_action", "保存操作不受支持。", nil)
		return
	}
	if submission.Action == "create" || submission.Action == "save" || (submission.Action == "publish" && form.Has("ai_pro_max")) {
		input, fields := s.parseEditorMutation(request, form, principal, current)
		if len(fields) > 0 {
			reject(http.StatusUnprocessableEntity, "validation_failed", "请检查标记字段，输入已保留。", fields)
			return
		}
		submission.Input = input
	}

	if !s.recheckContentWrite(writer, request, current.ID) {
		return
	}
	result, err := s.repository.Submit(request.Context(), submission)
	if err != nil {
		switch {
		case errors.Is(err, idempotency.ErrConflict):
			reject(http.StatusConflict, "submission_conflict", "同一重试标识对应不同内容；请核对原保存结果。", nil)
		case errors.Is(err, content.ErrVersionConflict):
			reject(http.StatusConflict, "version_conflict", "正式版本已变化，请在新窗口核对；当前输入已保留。", nil)
		case errors.Is(err, content.ErrNotFound):
			reject(http.StatusNotFound, "not_found", "原内容或修订已不可用；当前输入已保留。", nil)
		case errors.Is(err, content.ErrSlugConflict):
			reject(http.StatusConflict, "slug_conflict", "别名已被使用，请修改后保存。", map[string]string{"slug": "同类型内容已使用这个别名"})
		case errors.Is(err, content.ErrRedirectConflict):
			reject(http.StatusConflict, "redirect_conflict", "旧地址已被其他规则使用，请核对后保存。", map[string]string{"slug": "旧地址冲突"})
		case len(content.FieldErrors(err)) > 0:
			reject(http.StatusUnprocessableEntity, "validation_failed", "请检查标记字段，输入已保留。", content.FieldErrors(err))
		default:
			slog.Error("editorial submission not confirmed", "error", err, "post_id", current.ID)
			reject(http.StatusServiceUnavailable, "result_unconfirmed", "保存结果暂时无法确认，请保留页面并重试同一次保存。", nil)
		}
		return
	}
	if !s.recheckContentWrite(writer, request, current.ID) {
		return
	}
	s.writeEditorSubmissionResult(writer, key, result)
}

func (s *Server) writeEditorSubmissionResult(writer http.ResponseWriter, key string, result content.SubmissionResult) {
	receipt := result.Receipt
	base := "/admin/content/" + strconv.FormatInt(receipt.PostID, 10)
	writeJSON(writer, http.StatusOK, map[string]any{
		"saved": true, "submission_key": key, "id": receipt.PostID, "version": receipt.Version,
		"action": receipt.Action, "replayed": result.Replayed,
		"editor_url": base + "/edit/?notice=saved", "autosave_url": base + "/autosave/",
		"autosave_enabled": s.siteSnapshot(responseContext(writer)).Settings.Writing.AutosaveEnabled,
	})
}
