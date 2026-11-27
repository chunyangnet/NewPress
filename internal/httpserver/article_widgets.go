package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	htmlparser "golang.org/x/net/html"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
)

const (
	maxArticleWidgetPosts     = 24
	maxRenderedArticleWidgets = 200
	maxArticleWidgetBodyBytes = 4 << 20
)

type articleWidgetReference struct {
	start, end int
	id         int64
	layout     string
	avatar     string
	likes      int
	stats      bool
}

// Locate only actual aside elements, keeping surrounding source byte-for-byte.
// A widget owns its whole subtree, so nested or malformed fallback markup can
// never expose author-supplied snapshots after the referenced post is hidden.
func articleWidgetReferences(source string) []articleWidgetReference {
	if !strings.Contains(strings.ToLower(source), "data-np-widget") {
		return nil
	}
	tokens := htmlparser.NewTokenizer(strings.NewReader(source))
	var refs []articleWidgetReference
	offset, depth := 0, 0
	var current articleWidgetReference
	for kind := tokens.Next(); kind != htmlparser.ErrorToken; kind = tokens.Next() {
		start := offset
		offset += len(tokens.Raw())
		if kind != htmlparser.StartTagToken && kind != htmlparser.EndTagToken && kind != htmlparser.SelfClosingTagToken {
			continue
		}
		token := tokens.Token()
		if token.Data != "aside" {
			continue
		}
		if depth > 0 {
			if kind == htmlparser.EndTagToken {
				depth--
				if depth == 0 {
					current.end = offset
					refs = append(refs, current)
				}
			} else {
				depth++
			}
			continue
		}
		if kind == htmlparser.EndTagToken {
			continue
		}
		widget, id, layout := "", "", "card"
		for _, attr := range token.Attr {
			switch attr.Key {
			case "data-np-widget":
				widget = attr.Val
			case "data-np-post":
				id = attr.Val
			case "data-np-layout":
				if attr.Val == "compact" {
					layout = "compact"
				}
			}
		}
		if widget != "post" {
			continue
		}
		postID, err := strconv.ParseInt(id, 10, 64)
		if err != nil || postID <= 0 || strconv.FormatInt(postID, 10) != id {
			postID = 0
		}
		current = articleWidgetReference{start: start, id: postID, layout: layout}
		depth = 1
	}
	if depth > 0 {
		current.end = len(source)
		refs = append(refs, current)
	}
	return refs
}

func (s *Server) prepareArticleWidgets(ctx context.Context, data *pageView) {
	if data.IsAdmin || data.Post.ContentHTML == "" {
		return
	}
	refs := articleWidgetReferences(data.Post.ContentHTML)
	if len(refs) == 0 {
		return
	}
	ids := make([]int64, 0, maxArticleWidgetPosts)
	seen := map[int64]bool{}
	for index, ref := range refs {
		if index >= maxRenderedArticleWidgets {
			break
		}
		if ref.id > 0 && !seen[ref.id] && len(ids) < maxArticleWidgetPosts {
			seen[ref.id] = true
			ids = append(ids, ref.id)
		}
	}
	posts := map[int64]content.Post{}
	if len(ids) > 0 && s.repository != nil {
		// One bounded public query checks visibility at request time. Never load
		// a stored private snapshot, and never render a referenced body.
		if result, err := s.repository.SearchPublished(ctx, content.PublishedQuery{PostIDs: ids, Limit: maxArticleWidgetPosts}); err == nil {
			now := time.Now()
			for _, post := range result.Items {
				if seen[post.ID] && post.Type == content.TypePost && content.PublicAt(post, now) {
					posts[post.ID] = post
				}
			}
		}
	}
	likeCounts := map[int64]int{}
	if s.engagements != nil && len(ids) > 0 {
		if states, err := s.engagements.States(ctx, 0, engagement.Like, ids); err == nil {
			for _, state := range states {
				likeCounts[state.TargetID] = state.Count
			}
		}
	}
	avatars := map[int64]string{}
	for _, post := range posts {
		if _, seen := avatars[post.AuthorID]; !seen && post.AuthorID > 0 && s.auth != nil {
			avatars[post.AuthorID] = ""
			if user, err := s.auth.GetUser(ctx, post.AuthorID); err == nil && user.Status == auth.UserStatusActive {
				avatars[post.AuthorID] = s.avatarURL(user.AvatarMediaID)
			}
		}
	}
	var out strings.Builder
	start := 0
	projectedBytes := len(data.Post.ContentHTML)
	for index, ref := range refs {
		out.WriteString(data.Post.ContentHTML[start:ref.start])
		post, ok := posts[ref.id]
		ref.likes, ref.stats = likeCounts[ref.id]
		ref.avatar = avatars[post.AuthorID]
		replacement := ""
		if index < maxRenderedArticleWidgets {
			replacement = renderArticleWidget(ref, post, ok, s.contentPath)
			if projectedBytes-(ref.end-ref.start)+len(replacement) > maxArticleWidgetBodyBytes {
				replacement = renderArticleWidget(ref, content.Post{}, false)
			}
			if projectedBytes-(ref.end-ref.start)+len(replacement) > maxArticleWidgetBodyBytes {
				replacement = ""
			}
		}
		projectedBytes += len(replacement) - (ref.end - ref.start)
		out.WriteString(replacement)
		start = ref.end
	}
	out.WriteString(data.Post.ContentHTML[start:])
	data.Post.ContentHTML = out.String()
}

func renderArticleWidget(ref articleWidgetReference, post content.Post, available bool, paths ...contentPathBuilder) string {
	var out strings.Builder
	out.WriteString(`<aside data-np-widget="post"`)
	if ref.id > 0 {
		fmt.Fprintf(&out, ` data-np-post="%d"`, ref.id)
	}
	fmt.Fprintf(&out, ` data-np-layout="%s">`, ref.layout)
	if !available {
		out.WriteString(`<span data-np-part="body"><strong data-np-part="title">文章暂不可用</strong><span data-np-part="meta">原文章可能已下架或暂未公开。</span></span></aside>`)
		return out.String()
	}
	escape := html.EscapeString
	fmt.Fprintf(&out, `<a data-np-part="link" href="%s">`, escape(resolvedContentPath(post, paths...)))
	if cover := articleWidgetCover(post.CoverURL); cover != "" {
		fmt.Fprintf(&out, `<span data-np-part="cover"><img src="%s" alt="" loading="lazy" width="240" height="160"></span>`, escape(cover))
	}
	out.WriteString(`<span data-np-part="body"><span data-np-part="eyebrow">推荐阅读`)
	if post.Category != "" {
		out.WriteString(" · " + escape(post.Category))
	}
	fmt.Fprintf(&out, `</span><strong data-np-part="title">%s</strong>`, escape(post.Title))
	if post.Excerpt != "" {
		fmt.Fprintf(&out, `<span data-np-part="excerpt">%s</span>`, escape(post.Excerpt))
	}
	if post.AuthorName != "" {
		out.WriteString(`<span data-np-part="meta">`)
		if avatar := articleWidgetCover(ref.avatar); avatar != "" {
			fmt.Fprintf(&out, `<img data-np-part="avatar" src="%s" alt="" width="24" height="24" loading="lazy">`, escape(avatar))
		}
		out.WriteString(escape(post.AuthorName))
		if !post.PublishedAt.IsZero() {
			fmt.Fprintf(&out, `<span data-np-part="date">%s</span>`, post.PublishedAt.Format("1月2日"))
		}
		if ref.stats {
			fmt.Fprintf(&out, `<span data-np-part="likes" aria-label="%d 次喜欢">♥ %d</span>`, ref.likes, ref.likes)
		}
		out.WriteString(`</span>`)
	}
	out.WriteString(`</span></a></aside>`)
	return out.String()
}

func articleWidgetCover(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n") {
		return ""
	}
	return value
}

type articleWidgetSummary struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Excerpt    string `json:"excerpt"`
	CoverURL   string `json:"cover_url"`
	AuthorName string `json:"author_name"`
	Category   string `json:"category"`
	Permalink  string `json:"permalink"`
}

type articleWidgetPagination struct {
	Page  int `json:"page"`
	Pages int `json:"pages"`
	Total int `json:"total"`
}

func (s *Server) adminArticleWidgetsAPI(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityContentRead); !ok {
		return
	}
	s.publishedArticleWidgets(w, r)
}

func (s *Server) publishedArticleWidgets(w http.ResponseWriter, r *http.Request) {
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"q": true, "page": true})
	if !ok {
		return
	}
	page, err := positiveQueryInt(values.Get("page"), 1, 1_000_000)
	query, queryErr := content.NormalizePublishedQuery(content.PublishedQuery{Search: values.Get("q"), Limit: 8, Offset: (page - 1) * 8})
	if err != nil || queryErr != nil {
		s.writeAPIError(w, r, http.StatusBadRequest, "request.invalid_query", "搜索条件或分页参数不正确", nil)
		return
	}
	result, err := s.repository.SearchPublished(r.Context(), query)
	if err != nil {
		s.writeAPIError(w, r, http.StatusInternalServerError, "content.unavailable", "文章暂时不可用，请重试", nil)
		return
	}
	items := make([]articleWidgetSummary, 0, len(result.Items))
	for _, post := range result.Items {
		items = append(items, articleWidgetSummary{ID: post.ID, Title: post.Title, Excerpt: post.Excerpt, CoverURL: articleWidgetCover(post.CoverURL), AuthorName: post.AuthorName, Category: post.Category, Permalink: s.contentPath(post)})
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []articleWidgetSummary  `json:"items"`
		Pagination articleWidgetPagination `json:"pagination"`
	}{Items: items, Pagination: articleWidgetPagination{Page: page, Pages: max(1, (result.Total+7)/8), Total: result.Total}})
}

// Insert into the actual head closing tag, not text inside scripts, comments or
// article markup. Themes without a head retain their readable HTML fallback.
func withArticleWidgetStyles(output []byte, bases ...string) []byte {
	tokens := htmlparser.NewTokenizer(bytes.NewReader(output))
	offset, inHead := 0, false
	for kind := tokens.Next(); kind != htmlparser.ErrorToken; kind = tokens.Next() {
		start := offset
		offset += len(tokens.Raw())
		if kind != htmlparser.StartTagToken && kind != htmlparser.EndTagToken {
			continue
		}
		token := tokens.Token()
		if kind == htmlparser.StartTagToken && token.Data == "body" {
			return output
		}
		if token.Data != "head" {
			continue
		}
		if kind == htmlparser.StartTagToken {
			inHead = true
		} else if inHead {
			link := `<link rel="stylesheet" href="` + html.EscapeString(publicCoreAsset("css/newpress-article-widgets.css", bases...)) + `">`
			result := make([]byte, 0, len(output)+len(link))
			result = append(result, output[:start]...)
			result = append(result, link...)
			return append(result, output[start:]...)
		}
	}
	return output
}
