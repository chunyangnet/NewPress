package content

import (
	"context"
	"sort"
)

// Frozen full-collection implementation used as an independent ordering and
// pagination oracle. Do not replace it with the bounded production window.
func referencePublishedSearch(repository *MemoryRepository, ctx context.Context, query PublishedQuery) (PublishedPage, error) {
	if err := ctx.Err(); err != nil {
		return PublishedPage{}, err
	}
	query, err := NormalizePublishedQuery(query)
	if err != nil {
		return PublishedPage{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	now := repository.now()
	needle := NormalizePublicSearchText(query.Search)
	categorySlug := NormalizeTermSlug(query.Category)
	categoryName := NormalizePublicSearchText(query.Category)
	scores := make(map[int64]int)
	var titles map[int64]string
	if query.Sort == "title" {
		titles = make(map[int64]string)
	}
	selected := make(map[int64]int, len(query.PostIDs))
	for i, id := range query.PostIDs {
		selected[id] = i
	}
	selectedAuthors := make(map[int64]bool, len(query.AuthorIDs))
	for _, id := range query.AuthorIDs {
		selectedAuthors[id] = true
	}
	posts := make([]Post, 0)
	for _, post := range repository.posts {
		if err := ctx.Err(); err != nil {
			return PublishedPage{}, err
		}
		if query.PostIDs != nil {
			if _, ok := selected[post.ID]; !ok {
				continue
			}
		}
		if query.AuthorIDs != nil && !selectedAuthors[post.AuthorID] {
			continue
		}
		if (post.Type != query.Type && !(query.IncludeFlash && post.Type == TypeFlash)) || query.Important && !FlashMetadata(post).Important || !PublicAt(post, now) || query.AuthorID > 0 && post.AuthorID != query.AuthorID ||
			query.LegacyAuthor != "" && (post.AuthorID != 0 || post.AuthorName != query.LegacyAuthor) {
			continue
		}
		if query.Category != "" {
			matched := false
			for _, term := range post.Categories {
				if term.Slug == categorySlug || NormalizePublicSearchText(term.Name) == categoryName {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if needle != "" {
			score := publicSearchScore(post, needle, repository.searchBodies.body(post.ContentHTML))
			if score == 0 {
				continue
			}
			scores[post.ID] = score
		}
		if titles != nil {
			titles[post.ID] = NormalizePublicSearchText(post.Title)
		}
		posts = append(posts, post)
	}
	if err := ctx.Err(); err != nil {
		return PublishedPage{}, err
	}
	sort.Slice(posts, func(i, j int) bool {
		left, right := posts[i], posts[j]
		if query.Sort == "published" && !query.IgnorePinning && left.IsPinned() != right.IsPinned() {
			return left.IsPinned()
		}
		switch query.Sort {
		case "relevance":
			if scores[left.ID] != scores[right.ID] {
				return scores[left.ID] > scores[right.ID]
			}
			if !left.PublishedAt.Equal(right.PublishedAt) {
				return left.PublishedAt.After(right.PublishedAt)
			}
		case "selection":
			return selected[left.ID] < selected[right.ID]
		case "title":
			if titles[left.ID] != titles[right.ID] {
				return titles[left.ID] < titles[right.ID]
			}
		case "updated":
			if !left.UpdatedAt.Equal(right.UpdatedAt) {
				return left.UpdatedAt.After(right.UpdatedAt)
			}
		case "oldest":
			if !left.PublishedAt.Equal(right.PublishedAt) {
				return left.PublishedAt.Before(right.PublishedAt)
			}
			return left.ID < right.ID
		default:
			if !left.PublishedAt.Equal(right.PublishedAt) {
				return left.PublishedAt.After(right.PublishedAt)
			}
		}
		return left.ID > right.ID
	})
	if err := ctx.Err(); err != nil {
		return PublishedPage{}, err
	}
	total := len(posts)
	start := min(query.Offset, total)
	end := start + min(query.Limit, total-start)
	items := make([]Post, end-start)
	for i, post := range posts[start:end] {
		items[i] = clonePost(post)
	}
	return PublishedPage{Items: items, Total: total}, nil
}
