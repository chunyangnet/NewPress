package notification

import (
	"container/heap"
	"slices"
)

// Keep only the lowest limit IDs after the cursor. The largest selected ID is
// the heap root, so later candidates can displace it without copying every
// remaining subscriber or sorting the complete preference registry.
type subscriberSelection []int64

func (s subscriberSelection) Len() int           { return len(s) }
func (s subscriberSelection) Less(i, j int) bool { return s[i] > s[j] }
func (s subscriberSelection) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s *subscriberSelection) Push(value any)    { *s = append(*s, value.(int64)) }
func (s *subscriberSelection) Pop() any {
	last := len(*s) - 1
	value := (*s)[last]
	*s = (*s)[:last]
	return value
}

func subscriberPage(preferences map[int64]Preferences, after int64, limit int) []Preferences {
	ids := make(subscriberSelection, 0, min(limit, len(preferences)))
	for id, p := range preferences {
		if id <= after || !p.Weekly {
			continue
		}
		if len(ids) < limit {
			ids = append(ids, id)
			if len(ids) == limit {
				heap.Init(&ids)
			}
		} else if id < ids[0] {
			ids[0] = id
			heap.Fix(&ids, 0)
		}
	}
	slices.Sort(ids)
	items := make([]Preferences, len(ids))
	for i, id := range ids {
		items[i] = preferences[id]
	}
	return items
}
