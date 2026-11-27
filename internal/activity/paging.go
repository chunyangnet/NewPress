package activity

import (
	"sort"
	"time"
)

// PendingAfter selects a bounded page in bytewise key order. It does not mutate
// the source slice or use offsets, which would skip rows deleted by ACKs.
func PendingAfter(events []Event, now time.Time, limit int, after string) []Event {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	items := make([]Event, 0, limit)
	for _, event := range events {
		if event.Key <= after || event.Due.After(now) {
			continue
		}
		pos := sort.Search(len(items), func(i int) bool { return items[i].Key >= event.Key })
		if pos == limit {
			continue
		}
		if len(items) < limit {
			items = append(items, Event{})
		}
		copy(items[pos+1:], items[pos:len(items)-1])
		items[pos] = event
	}
	for i := range items {
		items[i].Mentions = append([]string(nil), items[i].Mentions...)
		items[i].Recipients = append([]int64(nil), items[i].Recipients...)
	}
	return items
}
