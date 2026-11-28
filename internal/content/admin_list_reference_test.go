package content

import (
	"sort"
	"strings"
)

// Frozen pre-optimization implementation for independent query and pagination comparisons.
func referenceAdminPage(repository *MemoryRepository, query AdminQuery) (AdminList, error) {
	query, err := NormalizeAdminQuery(query)
	if err != nil {
		return AdminList{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	search := strings.ToLower(query.Search)
	allMatches := make([]Post, 0, len(repository.posts))
	statusCounts := make(map[string]int)
	typeCounts := make(map[string]int)
	for _, post := range repository.posts {
		if query.AuthorID > 0 && post.AuthorID != query.AuthorID {
			continue
		}
		if query.Type != "" && post.Type != query.Type {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(post.Title+"\n"+post.Slug+"\n"+post.Excerpt+"\n"+post.AuthorName+"\n"+strings.Join(termNames(post.Categories), " ")+"\n"+strings.Join(termNames(post.Tags), " ")), search) {
			continue
		}
		if !query.PublishedAfter.IsZero() && !post.PublishedAt.After(query.PublishedAfter) || !query.PublishedBefore.IsZero() && (post.PublishedAt.IsZero() || post.PublishedAt.After(query.PublishedBefore)) {
			continue
		}
		statusCounts[post.Status]++
		typeCounts[post.Type]++
		if query.Status != "" && post.Status != query.Status {
			continue
		}
		allMatches = append(allMatches, clonePost(post))
	}
	sort.SliceStable(allMatches, func(left, right int) bool {
		if query.PublicationAscending {
			if allMatches[left].PublishedAt.Equal(allMatches[right].PublishedAt) {
				return allMatches[left].ID < allMatches[right].ID
			}
			return allMatches[left].PublishedAt.Before(allMatches[right].PublishedAt)
		}
		if allMatches[left].UpdatedAt.Equal(allMatches[right].UpdatedAt) {
			return allMatches[left].ID > allMatches[right].ID
		}
		return allMatches[left].UpdatedAt.After(allMatches[right].UpdatedAt)
	})
	total := len(allMatches)
	start := min(query.Offset, total)
	end := min(start+query.Limit, total)
	return AdminList{
		Items: append([]Post(nil), allMatches[start:end]...), Total: total,
		StatusCounts: statusCounts, TypeCounts: typeCounts,
	}, nil
}
