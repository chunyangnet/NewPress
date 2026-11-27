package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/idempotency"
)

func publicCommentSubmission(key string, postID, userID int64, body publicCommentCreateAPIRequest) comment.Submission {
	// The key's namespace deliberately excludes route and account identity: a
	// changed account, article or request must conflict, never create anew.
	digest := func(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
	name, email := strings.TrimSpace(body.AuthorName), strings.ToLower(strings.TrimSpace(body.Email))
	if userID > 0 {
		name, email = "", ""
	} // Account profile edits do not change the original user identity.
	bodyText := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(body.Content, "\r\n", "\n"), "\r", "\n"))
	fingerprint, _ := json.Marshal(struct {
		PostID, ParentID, UserID      int64
		Name, Email, Content, Website string
	}{postID, body.ParentID, userID, name, email, bodyText, strings.TrimSpace(body.Website)})
	return comment.Submission{Key: digest([]byte("public-comment:v1\x00" + key)), Fingerprint: digest(fingerprint)}
}

func (s *Server) writeCommentSubmissionError(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, idempotency.ErrConflict) {
		s.writeAPIError(writer, request, http.StatusConflict, "comments.submission_conflict", "这次提交的账号或内容与原请求不同，请使用原账号核对评论状态。", nil)
		return
	}
	if errors.Is(err, idempotency.ErrCapacity) {
		s.writeAPIError(writer, request, http.StatusServiceUnavailable, "comments.submission_capacity", "评论提交记录已满，请联系站点管理员；已有提交仍可确认。", nil)
		return
	}
	slog.Error("confirm public comment submission", "error", err, "request_id", request.Header.Get("X-Request-ID"))
	s.writeAPIError(writer, request, http.StatusInternalServerError, "comments.submission_unavailable", "暂时无法确认这次评论，请保留输入后重试。", nil)
}

func (s *Server) writeCommentSubmissionResult(writer http.ResponseWriter, request *http.Request, post content.Post, key string, result comment.SubmissionResult) {
	if result.Receipt.PostID != post.ID {
		s.writeCommentSubmissionError(writer, request, idempotency.ErrConflict)
		return
	}
	current, err := s.comments.Get(request.Context(), result.Receipt.CommentID)
	if err != nil && !errors.Is(err, comment.ErrNotFound) {
		s.writeCommentSubmissionError(writer, request, err)
		return
	}
	response := publicCommentCreateAPIResponse{SubmissionKey: key, Replayed: result.Replayed, Status: "received", Notice: "已确认此前提交，评论当前未公开显示。"}
	status := http.StatusOK
	if err == nil && (current.Status == comment.StatusPending || current.Status == comment.StatusApproved) {
		if current.PostID != post.ID {
			s.writeCommentSubmissionError(writer, request, idempotency.ErrConflict)
			return
		}
		item := s.publicCommentWithOrigin(request, current)
		response.Comment, response.Status = &item, current.Status
		status = http.StatusAccepted
		response.Notice = s.siteSettings.Snapshot().Settings.Discussion.ModerationNotice
		if current.Status == comment.StatusApproved {
			status, response.Notice = http.StatusCreated, "评论已发布"
			writer.Header().Set("Location", s.contentPath(post)+"#comment-"+strconv.FormatInt(current.ID, 10))
		}
		if result.Replayed {
			status = http.StatusOK
			if current.Status == comment.StatusApproved {
				response.Notice = "已确认此前提交，评论当前已公开。"
			} else {
				response.Notice = "已确认此前提交，评论仍在等待审核。"
			}
		}
	}
	if response.Status == "received" {
		response.Replayed = true
	}
	if response.Replayed {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(writer, status, response)
}
