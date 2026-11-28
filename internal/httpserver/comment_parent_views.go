package httpserver

import (
	"net/http"
	"strconv"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

type adminCommentParentView struct {
	content.PostHeader
	EditURL, Permalink string
}

// One request-local batch avoids loading article bodies and cloning metadata
// for every comment. Repository adapters retain deduplicated full-read fallback.
func (s *Server) adminCommentItems(request *http.Request, items []comment.Comment) []adminCommentItemView {
	views := make([]adminCommentItemView, len(items))
	if len(items) == 0 {
		return views
	}
	ids := make([]int64, 0, len(items))
	seen := make(map[int64]bool, len(items))
	for _, item := range items {
		if !seen[item.PostID] {
			seen[item.PostID] = true
			ids = append(ids, item.PostID)
		}
	}
	// Parent metadata has always been best effort for moderators. A failed
	// batch discards partial values and uses the existing missing-parent label.
	// Callers still recheck request cancellation and authority before output.
	headers, _ := content.ReadPostHeaders(request.Context(), s.repository, ids)
	parents := make(map[int64]adminCommentParentView, len(headers))
	var paths contentPathBuilder
	now := time.Now()
	for id, header := range headers {
		parent := adminCommentParentView{PostHeader: header, EditURL: "/admin/content/" + strconv.FormatInt(id, 10) + "/edit/"}
		if post := header.Post(); content.PublicAt(post, now) {
			if paths == nil {
				paths = s.permalinkBuilder()
			}
			parent.Permalink = paths(post)
		}
		parents[id] = parent
	}
	for i, item := range items {
		view := adminCommentItemView{
			Comment: item, PostTitle: "内容 #" + strconv.FormatInt(item.PostID, 10),
			SourceFingerprint: shortCommentFingerprint(item.AuthorIPHash), CanDelete: item.Status == comment.StatusTrash,
			InspectorURL: "/admin/comments/" + strconv.FormatInt(item.ID, 10) + "/edit/",
		}
		view.StatusLabel, view.StatusTone = adminCommentStatusPresentation(item.Status)
		if parent, ok := parents[item.PostID]; ok {
			view.PostTitle, view.PostType, view.PostStatus = parent.Title, parent.Type, parent.Status
			view.postAuthorID = parent.AuthorID
			view.PostEditURL, view.PostPermalink = parent.EditURL, parent.Permalink
		}
		views[i] = view
	}
	return views
}

func (s *Server) adminCommentItem(request *http.Request, item comment.Comment) adminCommentItemView {
	return s.adminCommentItems(request, []comment.Comment{item})[0]
}
