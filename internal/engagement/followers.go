package engagement

import (
	"context"
	"sort"
)

// FollowerSource pages account relationships by user ID for publication fanout.
type FollowerSource interface {
	Followers(context.Context, int64, int64, int) ([]Entry, error)
}

func (s *FileStore) Followers(ctx context.Context, author, after int64, limit int) ([]Entry, error) {
	if author <= 0 || after < 0 {
		return nil, ErrInvalid
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := []Entry{}
	for _, e := range s.data.Entries {
		if e.Kind == Follow && e.TargetID == author && e.UserID > after {
			items = append(items, e)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UserID < items[j].UserID })
	return items[:min(len(items), limit)], ctx.Err()
}
