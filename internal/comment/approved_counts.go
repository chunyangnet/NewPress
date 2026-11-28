package comment

import "context"

// ApprovedCountsReader is optional so custom stores can keep using Store.
// Missing post IDs have zero approved comments; counts never include bodies.
type ApprovedCountsReader interface {
	CountApprovedByPost(context.Context, []int64) (map[int64]int, error)
}

func ReadApprovedCounts(ctx context.Context, store Store, postIDs []int64) (map[int64]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	counts := make(map[int64]int, len(postIDs))
	ids := make([]int64, 0, len(postIDs))
	for _, id := range postIDs {
		if id <= 0 {
			return nil, ErrInvalidInput
		}
		if _, exists := counts[id]; !exists {
			ids = append(ids, id)
			counts[id] = 0
		}
	}
	if len(ids) == 0 {
		return counts, nil
	}
	if reader, ok := store.(ApprovedCountsReader); ok {
		values, err := reader.CountApprovedByPost(ctx, ids)
		if err != nil {
			return nil, err
		}
		for id, count := range values {
			if _, wanted := counts[id]; !wanted || count < 0 {
				return nil, ErrInvalidInput
			}
			counts[id] = count
		}
	} else {
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			_, count, err := store.ListApproved(ctx, id, 1, 0)
			if err != nil {
				return nil, err
			}
			if count < 0 {
				return nil, ErrInvalidInput
			}
			counts[id] = count
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

func (s *FileStore) CountApprovedByPost(ctx context.Context, postIDs []int64) (map[int64]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, ErrInvalidInput
	}
	counts := make(map[int64]int, len(postIDs))
	for _, id := range postIDs {
		if id <= 0 {
			return nil, ErrInvalidInput
		}
		counts[id] = 0
	}
	if len(counts) == 0 {
		return counts, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	scanned := 0
	for _, c := range s.items {
		if scanned%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		scanned++
		if _, wanted := counts[c.PostID]; wanted && c.Status == StatusApproved {
			counts[c.PostID]++
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}
