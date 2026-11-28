package objectstorage

import (
	"container/heap"
	"slices"
	"strings"
	"time"
)

type recordPosition struct {
	id      string
	updated time.Time
}

// recordSelection retains only the requested leading records. The heap root is
// the least preferred retained entry, so every other record costs one comparison.
type recordSelection struct {
	values []recordPosition
	limit  int
	newest bool
}

func newRecordSelection(limit int, newest bool) *recordSelection {
	limit = max(0, limit)
	return &recordSelection{values: make([]recordPosition, 0, min(100, limit)), limit: limit, newest: newest}
}

func (s *recordSelection) compare(a, b recordPosition) int {
	order := a.updated.Compare(b.updated)
	if s.newest {
		order = -order
	}
	if order != 0 {
		return order
	}
	return strings.Compare(a.id, b.id)
}

func (s *recordSelection) Len() int           { return len(s.values) }
func (s *recordSelection) Less(i, j int) bool { return s.compare(s.values[i], s.values[j]) > 0 }
func (s *recordSelection) Swap(i, j int)      { s.values[i], s.values[j] = s.values[j], s.values[i] }
func (s *recordSelection) Push(value any)     { s.values = append(s.values, value.(recordPosition)) }
func (s *recordSelection) Pop() any {
	last := len(s.values) - 1
	value := s.values[last]
	s.values[last] = recordPosition{}
	s.values = s.values[:last]
	return value
}

func (s *recordSelection) add(r Record) {
	if s.limit == 0 {
		return
	}
	position := recordPosition{r.ID, r.Updated}
	if len(s.values) < s.limit {
		heap.Push(s, position)
	} else if s.compare(position, s.values[0]) < 0 {
		s.values[0] = position
		heap.Fix(s, 0)
	}
}

func (s *recordSelection) sorted() []recordPosition {
	slices.SortFunc(s.values, s.compare)
	return s.values
}
