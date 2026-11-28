package comment

import (
	"cmp"
	"container/heap"
	"slices"
	"time"
)

type commentPosition struct {
	id int64
	at time.Time
}

// A page retains only the leading offset+limit positions. Bodies and private
// metadata are copied from the locked map only for the returned page.
type commentSelection struct {
	values []commentPosition
	limit  int
	newest bool
}

func newCommentSelection(total, limit, offset int, newest bool) *commentSelection {
	capacity := 0
	if offset < total {
		// Offset is an unrestricted non-negative int. Keep the sum <= total
		// without overflowing on a valid but out-of-range page request.
		capacity = offset + min(limit, total-offset)
	}
	return &commentSelection{values: make([]commentPosition, 0, min(capacity, MaxListLimit)), limit: capacity, newest: newest}
}

func (s *commentSelection) compare(a, b commentPosition) int {
	order := a.at.Compare(b.at)
	if order == 0 {
		order = cmp.Compare(a.id, b.id)
	}
	if s.newest {
		return -order
	}
	return order
}

func (s *commentSelection) Len() int           { return len(s.values) }
func (s *commentSelection) Less(i, j int) bool { return s.compare(s.values[i], s.values[j]) > 0 }
func (s *commentSelection) Swap(i, j int)      { s.values[i], s.values[j] = s.values[j], s.values[i] }
func (s *commentSelection) Push(value any)     { s.values = append(s.values, value.(commentPosition)) }
func (s *commentSelection) Pop() any {
	last := len(s.values) - 1
	value := s.values[last]
	s.values[last] = commentPosition{}
	s.values = s.values[:last]
	return value
}

func (s *commentSelection) add(item Comment) {
	if s.limit == 0 {
		return
	}
	at := item.CreatedAt
	if s.newest {
		at = item.UpdatedAt
	}
	candidate := commentPosition{id: item.ID, at: at}
	if len(s.values) < s.limit {
		// Build the initial prefix in one slice; boxing every position through
		// heap.Push would allocate once per skipped row on deep pages.
		s.values = append(s.values, candidate)
		if len(s.values) == s.limit {
			heap.Init(s)
		}
	} else if s.compare(candidate, s.values[0]) < 0 {
		s.values[0] = candidate
		heap.Fix(s, 0)
	}
}

func (s *commentSelection) page(items map[int64]Comment, offset int) []Comment {
	result := []Comment{}
	if offset >= len(s.values) {
		return result
	}
	slices.SortFunc(s.values, s.compare)
	result = make([]Comment, 0, len(s.values)-offset)
	for _, p := range s.values[offset:] {
		result = append(result, items[p.id])
	}
	return result
}
