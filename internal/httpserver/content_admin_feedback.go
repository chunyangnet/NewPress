package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func (s *Server) handleContentCreateError(writer http.ResponseWriter, request *http.Request, principal auth.Principal, input content.PostInput, err error) {
	fields := content.FieldErrors(err)
	status := http.StatusUnprocessableEntity
	notice := adminNotice{"内容没有通过服务端校验", "error"}
	if errors.Is(err, content.ErrSlugConflict) {
		fields = map[string]string{"slug": "同类型内容已使用这个别名"}
		status = http.StatusConflict
		notice.Message = "别名冲突，请更换后重试"
	} else if len(fields) == 0 {
		slog.Error("create admin content", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "保存内容失败")
		return
	}
	s.renderContentEditor(writer, request, status, principal, false, content.Post{}, input, fields, notice, false, false)
}

func (s *Server) handleContentUpdateError(writer http.ResponseWriter, request *http.Request, principal auth.Principal, id int64, action string, form url.Values, err error) {
	if errors.Is(err, content.ErrNotFound) {
		s.renderError(writer, http.StatusNotFound, "内容或修订没有找到")
		return
	}
	current, loadErr := s.repository.GetByID(request.Context(), id)
	if loadErr != nil {
		s.handleContentLoadError(writer, id, loadErr)
		return
	}
	input := inputFromPost(current)
	if action == "save" {
		input, _ = s.parseEditorInput(request, form, principal, time.Now(), current)
		input.AuthorID = current.AuthorID
		input.AuthorName = current.AuthorName
	}
	if errors.Is(err, content.ErrVersionConflict) {
		s.renderContentEditor(writer, request, http.StatusConflict, principal, false, current, input, nil,
			adminNotice{"这篇内容已在其他会话中更新。下方保留了你的输入，请核对后再次保存。", "error"}, true, false)
		return
	}
	fields := content.FieldErrors(err)
	if errors.Is(err, content.ErrRedirectConflict) {
		fields = map[string]string{"slug": "旧地址已被其他规则占用，请由管理员在「搜索与链接」中核对"}
	}
	if errors.Is(err, content.ErrSlugConflict) {
		fields = map[string]string{"slug": "同类型内容已使用这个别名"}
	}
	if len(fields) > 0 {
		s.renderContentEditor(writer, request, http.StatusUnprocessableEntity, principal, false, current, input, fields,
			adminNotice{"操作未通过校验，请检查标记字段", "error"}, false, false)
		return
	}
	slog.Error("update admin content", "id", id, "action", action, "error", err)
	s.renderError(writer, http.StatusInternalServerError, "更新内容失败")
}

func (s *Server) handleContentLoadError(writer http.ResponseWriter, id int64, err error) {
	if errors.Is(err, content.ErrNotFound) {
		s.renderError(writer, http.StatusNotFound, "内容没有找到")
		return
	}
	slog.Error("load admin content", "id", id, "error", err)
	s.renderError(writer, http.StatusInternalServerError, "内容暂时不可用")
}

func positivePathID(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("invalid positive id")
	}
	return value, nil
}

func positiveQueryInt(raw string, fallback, maximum int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		return 0, errors.New("invalid positive query integer")
	}
	return value, nil
}

func contentListURL(postType, status, search string, page int) string {
	values := make(url.Values)
	if postType != "" {
		values.Set("type", postType)
	}
	if status != "" {
		values.Set("status", status)
	}
	if search != "" {
		values.Set("q", search)
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	if encoded := values.Encode(); encoded != "" {
		return "/admin/content/?" + encoded
	}
	return "/admin/content/"
}

func contentListNoticeURL(postType, status, search string, page int, notice string) string {
	target, err := url.Parse(contentListURL(postType, status, search, page))
	if err != nil {
		return "/admin/content/"
	}
	values := target.Query()
	if notice != "" {
		values.Set("notice", notice)
	}
	target.RawQuery = values.Encode()
	return target.String()
}

func bulkContentNotice(status string) string {
	switch status {
	case content.StatusDraft:
		return "bulk-drafted"
	case content.StatusPending:
		return "bulk-pending"
	case content.StatusPublish:
		return "bulk-published"
	case content.StatusPrivate:
		return "bulk-private"
	case content.StatusTrash:
		return "bulk-trashed"
	default:
		return ""
	}
}

func contentTypeLabel(value string) string {
	if value == content.TypePage {
		return "页面"
	}
	return "文章"
}

func contentStatusLabel(value string) string {
	switch value {
	case content.StatusPublish:
		return "已发布"
	case content.StatusFuture:
		return "定时发布"
	case content.StatusPending:
		return "等待审核"
	case content.StatusPrivate:
		return "私密"
	case content.StatusTrash:
		return "回收站"
	default:
		return "草稿"
	}
}

func contentStatusTone(value string) string {
	switch value {
	case content.StatusPublish:
		return "live"
	case content.StatusFuture:
		return "scheduled"
	case content.StatusPending:
		return "pending"
	case content.StatusPrivate:
		return "private"
	case content.StatusTrash:
		return "trash"
	default:
		return "draft"
	}
}

func contentNoticeFromQuery(value string) adminNotice {
	switch value {
	case "created":
		return adminNotice{"内容已创建，并生成首个修订版本", "success"}
	case "saved":
		return adminNotice{"更改已保存，内容版本号已递增", "success"}
	case "trashed":
		return adminNotice{"内容已移入回收站，公开地址立即停止提供", "success"}
	case "restored":
		return adminNotice{"内容已从回收站恢复为草稿", "success"}
	case "published":
		return adminNotice{"内容已立即发布", "success"}
	case "revision-restored":
		return adminNotice{"历史修订已恢复，并保留当前发布状态", "success"}
	case "autosave-discarded":
		return adminNotice{"自动保存恢复点已放弃，正式内容没有改变", "success"}
	case "bulk-drafted":
		return adminNotice{"所选内容已在同一次原子操作中恢复或设为草稿，并分别生成新修订", "success"}
	case "bulk-pending":
		return adminNotice{"所选内容已原子提交审核，并分别生成新修订", "success"}
	case "bulk-published":
		return adminNotice{"所选内容已原子发布；任何一项版本冲突都会阻止整批写入", "success"}
	case "bulk-private":
		return adminNotice{"所选内容已原子设为私密，并停止公开提供", "success"}
	case "bulk-deleted":
		return adminNotice{"所选内容已永久删除", "success"}
	case "bulk-trashed":
		return adminNotice{"所选内容已原子移入回收站，公开地址立即停止提供", "success"}
	case "bulk-conflict":
		return adminNotice{"批量操作未写入：至少一项内容已在其他会话更新，请刷新清单后重新选择", "warning"}
	default:
		return adminNotice{}
	}
}
