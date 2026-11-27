package httpserver

import (
	"context"
	"slices"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

const (
	maxExpandedSnippetsBytes     = 4 << 20
	maxExpandedSnippetReferences = 200
)

// Expand only immutable revisions in the current sharing period. Failure
// removes every shell; no author-provided fallback or private title survives.
func (s *Server) expandPublicSnippets(ctx context.Context, source string) (string, bool) {
	bodies, found := s.expandPublicSnippetBodies(ctx, []string{source})
	return bodies[0], found
}

// A response shares one lookup and one expansion allowance. References are
// visited in field/document order; duplicates use the same resolved revision
// but every occurrence consumes output bytes and an instance slot.
func (s *Server) expandPublicSnippetBodies(ctx context.Context, sources []string) ([]string, bool) {
	result := slices.Clone(sources)
	keys := []contenthtml.SnippetKey{}
	seen := map[contenthtml.SnippetKey]bool{}
	marked := make([]bool, len(sources))
	found, candidates := false, 0
	for i, source := range sources {
		refs := contenthtml.SnippetReferences(source)
		marked[i] = len(refs) > 0
		found = found || marked[i]
		for _, ref := range refs {
			if ref.ID <= 0 || ref.Version <= 0 {
				continue
			}
			candidates++
			if candidates <= maxExpandedSnippetReferences && !seen[ref.SnippetKey] && len(keys) < content.MaxSnippetResolve {
				keys = append(keys, ref.SnippetKey)
				seen[ref.SnippetKey] = true
			}
		}
	}
	if !found {
		return result, false
	}
	bodies := make(map[contenthtml.SnippetKey]string, len(keys))
	if s.repository != nil && len(keys) > 0 && ctx.Err() == nil {
		if resolved, err := s.repository.ResolveSnippets(ctx, keys); err == nil && ctx.Err() == nil {
			for _, key := range keys {
				body := resolved[key]
				if len(body) > content.MaxSnippetBodyBytes {
					continue
				}
				// Sanitize each distinct body once, even when many posts use it.
				body = contenthtml.Sanitize(body)
				if body != "" && len(body) <= content.MaxSnippetBodyBytes {
					bodies[key] = "<aside>" + body + "</aside>"
				}
			}
		}
	}
	remaining, count := maxExpandedSnippetsBytes, 0
	for i, source := range sources {
		if !marked[i] {
			continue
		}
		// Keep the existing conservative per-body ceiling as well as the shared
		// expansion budget. Ordinary article bytes are never truncated here.
		size := len(source)
		result[i] = contenthtml.RewriteSnippets(source, func(key contenthtml.SnippetKey) string {
			count++
			body := bodies[key]
			if count > maxExpandedSnippetReferences || body == "" || len(body) > remaining || len(body) > maxExpandedSnippetsBytes-size {
				return ""
			}
			remaining -= len(body)
			size += len(body)
			return body
		})
	}
	return result, true
}

func (s *Server) prepareSnippets(ctx context.Context, data *pageView) bool {
	if data.IsAdmin {
		return false
	}
	posts := []*content.Post{&data.Post}
	// Page fields can alias the same backing slice. Clone each collection before
	// changing its presentation so no cached/repository value is modified and
	// repeated fields cannot bypass the aggregate budget.
	appendPosts := func(items *[]content.Post) {
		*items = slices.Clone(*items)
		for i := range *items {
			posts = append(posts, &(*items)[i])
		}
	}
	appendPosts(&data.Posts)
	appendPosts(&data.Featured)
	appendPosts(&data.Home.FocusPosts)
	appendPosts(&data.Home.Updated)
	appendPosts(&data.Home.SpotlightDesktop)
	appendPosts(&data.Home.SpotlightMobile)
	appendPosts(&data.Home.Recent)
	appendPosts(&data.Series.Chapters)
	data.ReadingSeries = slices.Clone(data.ReadingSeries)
	for i := range data.ReadingSeries {
		item := &data.ReadingSeries[i]
		posts = append(posts, &item.Previous, &item.Next)
		appendPosts(&item.Related)
	}
	sources := make([]string, len(posts))
	for i, post := range posts {
		sources[i] = post.ContentHTML
	}
	bodies, found := s.expandPublicSnippetBodies(ctx, sources)
	for i, post := range posts {
		post.ContentHTML = bodies[i]
	}
	return found
}
