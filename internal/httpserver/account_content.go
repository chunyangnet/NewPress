package httpserver

import (
	"errors"
	"net/http"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"strconv"
	"time"
)

func (s *Server) bindCommentAccount(r *http.Request, form *publicCommentForm) int64 {
	if p, ok := s.currentAdmin(r); ok && p.UserID > 0 {
		form.AuthorName, form.AuthorEmail = p.Label(), p.Email
		return p.UserID
	}
	return 0
}

func (s *Server) socialOwnContent(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"kind": true, "q": true, "page": true, "status": true})
	if !ok {
		return
	}
	page, err := positiveQueryInt(q.Get("page"), 1, 10000)
	if err != nil || q.Get("kind") != "posts" && q.Get("kind") != "comments" {
		s.writeAPIError(w, r, 422, "account.invalid_query", "请检查内容类型或分页", nil)
		return
	}
	if q.Get("kind") == "posts" {
		s.ownPosts(w, r, p, page, q.Get("q"), q.Get("status"))
		return
	}
	if !s.moduleEnabled("comments") {
		s.writeAPIError(w, r, 404, "module.disabled", "评论功能已暂停", map[string]string{"module": "comments"})
		return
	}
	query, err := comment.NormalizeQuery(comment.Query{UserID: p.UserID, OwnOnly: true, Search: q.Get("q"), Status: q.Get("status"), Limit: 24, Offset: (page - 1) * 24})
	if err != nil {
		s.writeAPIError(w, r, 422, "account.invalid_query", "请检查评论搜索与状态", nil)
		return
	}
	result, err := s.comments.ListAdmin(r.Context(), query)
	if err != nil {
		s.engagementError(w, r, err)
		return
	}
	items := []map[string]any{}
	for _, c := range result.Items {
		// Only the writer's text and public metadata are returned. A withdrawn
		// article does not expose its private title, slug or updated contents.
		item := map[string]any{"id": c.ID, "content": c.ContentSource, "status": c.Status, "created_at": c.CreatedAt, "title": "原文章当前不可用", "url": ""}
		post, err := s.repository.GetByID(r.Context(), c.PostID)
		if err != nil && !errors.Is(err, content.ErrNotFound) {
			s.engagementError(w, r, err)
			return
		}
		if err == nil && s.publicEngagementTarget(post) {
			item["title"], item["url"] = post.Title, s.contentPath(post)+"#comments"
		}
		items = append(items, item)
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": result.Total, "page": page, "pages": max(1, (result.Total+23)/24), "status_counts": map[string]int{"pending": result.StatusCounts[comment.StatusPending], "approved": result.StatusCounts[comment.StatusApproved]}})
}

func (s *Server) ownPosts(w http.ResponseWriter, r *http.Request, p auth.Principal, page int, search, status string) {
	query, err := content.NormalizeAdminQuery(content.AdminQuery{AuthorID: p.UserID, Type: content.TypePost, Search: search, Status: status, Limit: 24, Offset: (page - 1) * 24})
	if err != nil {
		s.writeAPIError(w, r, 422, "account.invalid_query", "请检查文章搜索与状态", nil)
		return
	}
	result, err := s.repository.ListAdmin(r.Context(), query)
	if err != nil {
		s.engagementError(w, r, err)
		return
	}
	items := []map[string]any{}
	for _, post := range result.Items {
		path, edit := "", ""
		if content.PublicAt(post, time.Now()) {
			path = s.contentPath(post)
		}
		if p.Can(auth.CapabilityAdminAccess) && (p.Can(auth.CapabilityContentWrite) || p.Can(auth.CapabilityContentWriteOwn)) {
			edit = "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/edit/"
		}
		items = append(items, map[string]any{"id": post.ID, "title": post.Title, "excerpt": post.Excerpt, "status": post.Status, "url": path, "edit_url": edit, "updated_at": post.UpdatedAt})
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": result.Total, "page": page, "pages": max(1, (result.Total+23)/24), "status_counts": result.StatusCounts})
}
