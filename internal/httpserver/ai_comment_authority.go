package httpserver

import (
	"errors"
	"net/http"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

var (
	errAICommentContextChanged = errors.New("comment reply context changed")
	errAICommentPrivateContext = errors.New("comment reply context no longer authorized")
	errAICommentLeaseInactive  = errors.New("comment reply generation lease inactive")
)

// The model receives a fixed snapshot. Recheck its version and current object
// authority without repeatedly loading the article body during generation.
func (s *Server) aiCommentReplyAccess(r *http.Request, item comment.Comment, post content.Post, lease aiProMaxAccess) error {
	if _, err := s.commentPrincipal(r, true); err != nil {
		return err
	}
	if !lease.active() {
		return errAICommentLeaseInactive
	}
	states, readErr := comment.ReadStates(r.Context(), s.comments, []int64{item.ID})
	var headers map[int64]content.PostHeader
	if readErr == nil {
		headers, readErr = content.ReadPostHeaders(r.Context(), s.repository, []int64{post.ID})
	}
	principal, err := s.commentPrincipal(r, true)
	if err != nil {
		return err
	}
	if !lease.active() {
		return errAICommentLeaseInactive
	}
	if errors.Is(readErr, comment.ErrNotFound) || errors.Is(readErr, content.ErrNotFound) {
		return errAICommentContextChanged
	}
	if readErr != nil {
		return readErr
	}
	state := states[item.ID]
	if state.Version != item.Version || state.PostID != item.PostID || state.Status != item.Status || (state.Status != comment.StatusPending && state.Status != comment.StatusApproved) {
		return errAICommentContextChanged
	}
	header, ok := headers[post.ID]
	if !ok {
		return errAICommentContextChanged
	}
	current := header.Post()
	now := time.Now()
	if (!content.PublicAt(post, now) && !canWriteContentObject(principal, post)) || (!content.PublicAt(current, now) && !canWriteContentObject(principal, current)) {
		return errAICommentPrivateContext
	}
	if current.Version != post.Version || current.AuthorID != post.AuthorID || current.Type != post.Type || current.Status != post.Status || current.Title != post.Title || current.Slug != post.Slug || !current.PublishedAt.Equal(post.PublishedAt) || !current.UpdatedAt.Equal(post.UpdatedAt) {
		return errAICommentContextChanged
	}
	return r.Context().Err()
}

func (s *Server) aiCommentReplyAccessError(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Cache-Control", "no-store")
	for _, name := range []string{"Content-Length", "ETag", "Last-Modified", "Location"} {
		w.Header().Del(name)
	}
	switch {
	case errors.Is(err, errAICommentContextChanged):
		s.writeAPIError(w, r, 409, "comments.context_changed", "评论或所属文章已更新，请刷新后重新生成草稿", nil)
	case errors.Is(err, errAICommentPrivateContext):
		s.writeAPIError(w, r, 403, "comments.private_context", "当前账号不能使用这篇未公开文章生成回复", nil)
	case errors.Is(err, errAICommentLeaseInactive):
		s.writeAPIError(w, r, 409, "ai_pro_max.inactive", "插件或 AI 模块已停用，草稿生成已中断", nil)
	default:
		s.commentAccessError(w, r, err)
	}
}
