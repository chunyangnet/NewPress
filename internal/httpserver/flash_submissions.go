package httpserver

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/idempotency"
)

type flashWriteInput struct {
	ExpectedUserID    int64  `json:"expected_user_id"`
	ID                int64  `json:"id"`
	Version           int64  `json:"version"`
	Action            string `json:"action"`
	Title             string `json:"title"`
	Body              string `json:"body"`
	SourceName        string `json:"source_name"`
	SourceURL         string `json:"source_url"`
	Important         bool   `json:"important"`
	ReviewNote        string `json:"review_note"`
	PublishedAt       string `json:"published_at"`
	Comments          bool   `json:"comments"`
	PublishedTimezone string `json:"published_timezone,omitempty"`
}

// flashWrite never accepts an author or arbitrary metadata from the client.
func (s *Server) flashWrite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	admin := strings.HasPrefix(r.URL.Path, "/api/v1/admin/")
	if admin && !p.Can(auth.CapabilityContentWrite) {
		s.writeAPIError(w, r, 403, "flash.forbidden", "没有审核快报的权限", nil)
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var b flashWriteInput
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &b, 40<<10, "40 KiB"); !ok {
		return
	}
	if b.ExpectedUserID != p.UserID || p.UserID <= 0 {
		s.writeAPIError(w, r, 409, "flash.identity_changed", "账号已变化，请恢复原账号后重试，当前输入已保留", nil)
		return
	}
	key, err := strictIdempotencyKey(r)
	if err != nil || key == "" {
		s.writeAPIError(w, r, 400, "flash.retry_key", "保存请求缺少重试标识", nil)
		return
	}
	digest := func(v string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(v))) }
	encoded, _ := json.Marshal(b)
	submission := content.Submission{Key: digest(fmt.Sprint(p.UserID) + "\x00" + r.URL.Path + "\x00" + key), Fingerprint: digest(string(encoded)), Actor: p.Username, PostID: b.ID, Version: b.Version, Action: "save"}
	if result, e := s.repository.FindSubmission(r.Context(), submission.Key, submission.Fingerprint); e == nil {
		s.flashWriteResult(w, r, result)
		return
	} else if !errors.Is(e, content.ErrNotFound) {
		s.flashError(w, r, e)
		return
	}
	if allowed, _ := s.accessRate.allow("flash-write:"+strconv.FormatInt(p.UserID, 10), 30, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "flash.rate_limited", "提交过于频繁，请稍后重试", nil)
		return
	}
	current := content.Post{Type: content.TypeFlash, AuthorID: p.UserID, AuthorName: p.Label(), Status: content.StatusDraft}
	if b.ID != 0 {
		current, err = s.repository.GetByID(r.Context(), b.ID)
		if err != nil {
			s.flashError(w, r, err)
			return
		}
		if current.Type != content.TypeFlash || (!admin && current.AuthorID != p.UserID) {
			s.writeAPIError(w, r, 404, "flash.not_found", "快报不存在", nil)
			return
		}
		if b.Version <= 0 || b.Version != current.Version {
			s.flashError(w, r, content.ErrVersionConflict)
			return
		}
	} else {
		if b.Version != 0 {
			s.writeAPIError(w, r, 400, "flash.invalid_version", "新快报版本必须为零", nil)
			return
		}
		submission.Action = "create"
	}
	status := content.StatusDraft
	fields := content.FlashMetadata(current)
	if !admin && (b.Important || b.ReviewNote != "" || b.PublishedAt != "" || b.PublishedTimezone != "") {
		s.writeAPIError(w, r, 403, "flash.editor_fields", "重要标记、审核说明与发布时间只能由编辑设置", nil)
		return
	}
	if !admin && b.ID != 0 && current.Status != content.StatusDraft && !(current.Status == content.StatusPending && b.Action == "withdraw") {
		s.writeAPIError(w, r, 409, "flash.locked", "请先撤回待审稿；已发布内容请联系编辑修改", nil)
		return
	}
	if current.Status == content.StatusTrash && b.Action != "restore" {
		s.writeAPIError(w, r, 409, "flash.invalid_state", "回收站快报必须先恢复为草稿", nil)
		return
	}
	if b.Action == "submit" && current.Status != content.StatusDraft {
		s.writeAPIError(w, r, 409, "flash.invalid_state", "只有草稿可以提交审核", nil)
		return
	}
	if b.Action == "unpublish" && current.Status != content.StatusPublish && current.Status != content.StatusFuture {
		s.writeAPIError(w, r, 409, "flash.invalid_state", "只有已发布或定时快报可以下架", nil)
		return
	}
	switch b.Action {
	case "save":
		if admin && b.ID != 0 {
			status = current.Status
		}
	case "submit":
		status = content.StatusPending
		fields.ReviewNote = ""
	case "withdraw":
		if b.ID == 0 || current.Status != content.StatusPending {
			s.writeAPIError(w, r, 409, "flash.invalid_state", "只有待审核快报可以撤回", nil)
			return
		}
	case "publish":
		if !admin {
			s.writeAPIError(w, r, 403, "flash.forbidden", "投稿需经编辑审核后发布", nil)
			return
		}
		status = content.StatusPublish
		fields.ReviewNote = ""
	case "reject":
		if !admin || current.Status != content.StatusPending || strings.TrimSpace(b.ReviewNote) == "" {
			s.writeAPIError(w, r, 422, "flash.review_required", "退回待审稿需要填写审核说明", nil)
			return
		}
		fields.ReviewNote = b.ReviewNote
	case "unpublish", "trash", "restore":
		if !admin || b.ID == 0 {
			s.writeAPIError(w, r, 403, "flash.forbidden", "没有执行此操作的权限", nil)
			return
		}
		if b.Action == "trash" {
			status = content.StatusTrash
		}
		if b.Action == "restore" && current.Status != content.StatusTrash {
			s.writeAPIError(w, r, 409, "flash.invalid_state", "只能恢复回收站中的快报", nil)
			return
		}
	default:
		s.writeAPIError(w, r, 400, "flash.invalid_action", "不支持的快报操作", nil)
		return
	}
	input := content.PostInput{Type: content.TypeFlash, AuthorID: current.AuthorID, AuthorName: current.AuthorName, Actor: p.Username, Status: status, Slug: current.Slug, Title: b.Title, ContentSource: b.Body, CommentStatus: content.CommentOpen, PublishedAt: current.PublishedAt}
	if !b.Comments {
		input.CommentStatus = content.CommentClosed
	}
	if b.ID == 0 {
		input.Slug = "flash-" + submission.Key[:24]
	}
	fields.SourceName, fields.SourceURL = b.SourceName, b.SourceURL
	if admin {
		fields.Important = b.Important
	}
	stateOnly := b.Action == "withdraw" || b.Action == "reject" || b.Action == "trash" || b.Action == "restore" || b.Action == "unpublish"
	if stateOnly {
		input.Title, input.ContentSource, input.CommentStatus = current.Title, current.ContentSource, current.CommentStatus
		saved := content.FlashMetadata(current)
		fields.SourceName, fields.SourceURL, fields.Important = saved.SourceName, saved.SourceURL, saved.Important
	}
	if admin && !stateOnly {
		input.PublishedAt = time.Time{}
		if b.PublishedAt != "" {
			location := s.siteLocation(r.Context())
			if b.PublishedTimezone != location.String() {
				message := "站点时区已变化或缺少时区来源，请按当前 " + location.String() + " 核对发布时间后重新保存"
				s.writeAPIError(w, r, 422, "flash.timezone_changed", message, map[string]any{"published_timezone": location.String()})
				return
			}
			// The minute input cannot express stored seconds or distinguish a
			// repeated local hour. Preserve the authorized canonical instant.
			if !current.PublishedAt.IsZero() && b.PublishedAt == current.PublishedAt.In(location).Format(publishedLocalLayout) {
				input.PublishedAt = current.PublishedAt
			} else {
				input.PublishedAt, err = parsePublishedLocal(b.PublishedAt, location)
				if err != nil {
					s.writeAPIError(w, r, 422, "flash.invalid_time", err.Error(), map[string]any{"fields": map[string]string{"published_at": err.Error()}})
					return
				}
			}
		}
	}
	input.Flash = &fields
	submission.Input = input
	result, err := s.repository.Submit(r.Context(), submission)
	if err != nil {
		s.flashError(w, r, err)
		return
	}
	s.flashWriteResult(w, r, result)
}

func (s *Server) flashWriteResult(w http.ResponseWriter, r *http.Request, result content.SubmissionResult) {
	writeJSON(w, 200, map[string]any{"id": result.Receipt.PostID, "version": result.Receipt.Version, "replayed": result.Replayed, "message": "快报已保存"})
}
func (s *Server) flashError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, content.ErrNotFound):
		s.writeAPIError(w, r, 404, "flash.not_found", "快报不存在", nil)
	case errors.Is(err, content.ErrVersionConflict), errors.Is(err, idempotency.ErrConflict):
		s.writeAPIError(w, r, 409, "flash.conflict", "内容已变化或重试内容不一致，请核对后重试，当前输入已保留", nil)
	case len(content.FieldErrors(err)) > 0:
		s.writeAPIError(w, r, 422, "flash.validation", "请检查填写内容", map[string]any{"fields": content.FieldErrors(err)})
	default:
		s.writeAPIError(w, r, 500, "flash.unavailable", "快报暂时不可用，请稍后重试", nil)
	}
}
