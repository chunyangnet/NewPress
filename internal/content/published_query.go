package content

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PublishedQuery applies filtering and ordering before pagination, across the
// full public collection. LegacyAuthor matches only unowned demonstration
// bylines, never another account that happens to share the same display name.
type PublishedQuery struct {
	IncludeFlash  bool // Explicit opt-in for mixed personal libraries; never the default.
	Type          string
	Important     bool
	Search        string
	Category      string
	AuthorID      int64
	LegacyAuthor  string
	Sort          string
	IgnorePinning bool // Keep publication order chronological for feeds and digests.
	Limit         int
	Offset        int
	// nil means unrestricted; a non-nil empty selection matches nothing.
	PostIDs   []int64
	AuthorIDs []int64
}

func NormalizePublishedQuery(query PublishedQuery) (PublishedQuery, error) {
	if query.Type == "" {
		query.Type = TypePost
	}
	if query.Type != TypePost && query.Type != TypeFlash {
		return PublishedQuery{}, errors.New("invalid public content type")
	}
	if query.Important && query.Type != TypeFlash {
		return PublishedQuery{}, errors.New("importance is only supported for flash")
	}
	for _, ids := range [][]int64{query.PostIDs, query.AuthorIDs} {
		if len(ids) > 2000 {
			return PublishedQuery{}, errors.New("published selection too large")
		}
		seen := make(map[int64]bool, len(ids))
		for _, id := range ids {
			if id <= 0 || seen[id] {
				return PublishedQuery{}, errors.New("invalid published selection")
			}
			seen[id] = true
		}
	}
	query.Search = strings.TrimSpace(query.Search)
	query.Category = strings.TrimSpace(query.Category)
	query.LegacyAuthor = strings.TrimSpace(query.LegacyAuthor)
	// Validate each field independently: incomplete UTF-8 bytes in separate
	// fields can form a valid character when concatenated.
	for _, value := range []string{query.Search, query.Category, query.LegacyAuthor} {
		if !utf8.ValidString(value) || containsControl(value) {
			return PublishedQuery{}, errors.New("invalid published content query")
		}
	}
	if utf8.RuneCountInString(query.Search) > 80 ||
		len(query.Category) > MaxTermSlugBytes || utf8.RuneCountInString(query.LegacyAuthor) > 160 ||
		query.AuthorID < 0 || query.AuthorID > 0 && query.LegacyAuthor != "" {
		return PublishedQuery{}, errors.New("invalid published content query")
	}
	if query.Sort == "" {
		query.Sort = "published"
		if query.Search != "" {
			query.Sort = "relevance"
		}
	}
	if query.Sort == "relevance" && query.Search == "" {
		query.Sort = "published"
	}
	switch query.Sort {
	case "published", "updated", "oldest", "title", "relevance":
	case "selection":
		if query.PostIDs == nil {
			return PublishedQuery{}, errors.New("selection order requires post ids")
		}
	default:
		return PublishedQuery{}, errors.New("invalid published content order")
	}
	if query.Limit <= 0 {
		query.Limit = 20
	}
	query.Limit = min(query.Limit, 100)
	if query.Offset < 0 {
		query.Offset = 0
	}
	return query, nil
}

func (repository *MemoryRepository) SearchPublished(ctx context.Context, query PublishedQuery) (PublishedPage, error) {
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
	selected := make(map[int64]int, len(query.PostIDs))
	for i, id := range query.PostIDs {
		selected[id] = i
	}
	selectedAuthors := make(map[int64]bool, len(query.AuthorIDs))
	for _, id := range query.AuthorIDs {
		selectedAuthors[id] = true
	}
	// Clamp before addition so a maximum-int offset cannot overflow or reserve
	// an unbounded prefix. All matches are still scanned for the exact total.
	prefix := 0
	if query.Offset < len(repository.posts) {
		prefix = query.Offset + min(query.Limit, len(repository.posts)-query.Offset)
	}
	window := publishedWindow{limit: prefix}
	window.before = func(a, b publishedCandidate) bool {
		left, right := &repository.posts[a.index], &repository.posts[b.index]
		if query.Sort == "published" && !query.IgnorePinning && left.IsPinned() != right.IsPinned() {
			return left.IsPinned()
		}
		switch query.Sort {
		case "relevance":
			if a.score != b.score {
				return a.score > b.score
			}
			if !left.PublishedAt.Equal(right.PublishedAt) {
				return left.PublishedAt.After(right.PublishedAt)
			}
		case "selection":
			return selected[left.ID] < selected[right.ID]
		case "title":
			if a.title != b.title {
				return a.title < b.title
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
	}
	total := 0
	for index, post := range repository.posts {
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
		candidate := publishedCandidate{index: index}
		if needle != "" {
			candidate.score = publicSearchScore(post, needle, repository.searchBodies.body(post.ContentHTML))
			if candidate.score == 0 {
				continue
			}
		}
		total++
		if query.Sort == "title" && prefix > 0 {
			candidate.title = NormalizePublicSearchText(post.Title)
		}
		window.add(candidate)
	}
	if err := ctx.Err(); err != nil {
		return PublishedPage{}, err
	}
	window.order()
	if err := ctx.Err(); err != nil {
		return PublishedPage{}, err
	}
	start := min(query.Offset, total)
	end := start + min(query.Limit, total-start)
	items := make([]Post, end-start)
	for i := range items {
		items[i] = clonePost(repository.posts[window.items[start+i].index])
	}
	return PublishedPage{Items: items, Total: total}, nil
}

func (repository *FileRepository) SearchPublished(ctx context.Context, query PublishedQuery) (PublishedPage, error) {
	if err := ctx.Err(); err != nil {
		return PublishedPage{}, err
	}
	// File writes deep-copy posts and install a new state only after durable
	// replacement. Atomic publication also lets a new query read the last
	// committed state while a writer prepares or persists its next candidate.
	// Its shared body cache has its own lock and stores only pure projections.
	return repository.searchState.Load().SearchPublished(ctx, query)
}

// AuthorURL resolves the actual owner instead of pointing every byline at the
// demonstration team profile. Unowned bylines remain discoverable by name.
func (post Post) AuthorURL() string {
	if post.AuthorID > 0 {
		return "/authors/" + strconv.FormatInt(post.AuthorID, 10) + "/"
	}
	if post.AuthorName == "NewPress Team" {
		return "/authors/newpress-team/"
	}
	return "/explore/?q=" + url.QueryEscape(post.AuthorName)
}
