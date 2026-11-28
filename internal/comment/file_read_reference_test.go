// Frozen pre-optimization implementation for query contract comparisons.
package comment

import (
	"errors"
	"strings"
)

func referenceCommentAdmin(store *FileStore, query Query) (List, error) {
	if store == nil {
		return List{}, errors.New("comment store is nil")
	}
	normalized, err := NormalizeQuery(query)
	if err != nil {
		return List{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	all := store.itemsLocked()
	if normalized.UserID > 0 || normalized.OwnOnly {
		owned := make([]Comment, 0)
		for _, item := range all {
			if item.UserID == normalized.UserID && (!normalized.OwnOnly || item.Status == StatusPending || item.Status == StatusApproved) {
				owned = append(owned, item)
			}
		}
		all = owned
	}
	needle := strings.ToLower(normalized.Search)
	filtered := make([]Comment, 0, len(all))
	for _, item := range all {
		if normalized.PostID > 0 && item.PostID != normalized.PostID {
			continue
		}
		if normalized.Status != "" && item.Status != normalized.Status {
			continue
		}
		if needle != "" {
			haystack := strings.ToLower(strings.Join([]string{item.AuthorName, item.AuthorEmail, item.ContentSource}, " "))
			if normalized.OwnOnly {
				haystack = strings.ToLower(item.ContentSource)
			}
			if !strings.Contains(haystack, needle) {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	SortAdmin(filtered)
	result := List{Total: len(filtered), StatusCounts: StatusCounts(all), Items: []Comment{}}
	if normalized.Offset >= len(filtered) {
		return result, nil
	}
	end := min(len(filtered), normalized.Offset+normalized.Limit)
	result.Items = append(result.Items, filtered[normalized.Offset:end]...)
	return result, nil
}

func referenceCommentApproved(store *FileStore, postID int64, limit, offset int) ([]Comment, int, error) {
	if store == nil || postID <= 0 || offset < 0 {
		return nil, 0, ErrInvalidInput
	}
	if limit <= 0 || limit > MaxListLimit {
		limit = defaultPubSize
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	items := make([]Comment, 0)
	for _, item := range store.items {
		if item.PostID == postID && item.Status == StatusApproved {
			items = append(items, item)
		}
	}
	SortPublic(items)
	total := len(items)
	if offset >= total {
		return []Comment{}, total, nil
	}
	end := min(total, offset+limit)
	return append([]Comment(nil), items[offset:end]...), total, nil
}
