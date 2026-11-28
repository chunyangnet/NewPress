package content

import "sort"

// Entries refer to posts protected by the caller's repository read lock. Only
// the selected page is cloned; neither this window nor its entries are cached.
type publishedCandidate struct {
	index int
	score int
	title string
}

type publishedWindow struct {
	limit  int
	items  []publishedCandidate
	before func(publishedCandidate, publishedCandidate) bool
}

func (window *publishedWindow) add(candidate publishedCandidate) {
	if window.limit == 0 {
		return
	}
	if len(window.items) < window.limit {
		if len(window.items) == cap(window.items) {
			// Grow lazily for selective queries without reserving more than the
			// requested prefix, even when the offset is very large.
			items := make([]publishedCandidate, len(window.items), min(window.limit, max(8, 2*cap(window.items))))
			copy(items, window.items)
			window.items = items
		}
		window.items = append(window.items, candidate)
		if len(window.items) == window.limit {
			for i := len(window.items)/2 - 1; i >= 0; i-- {
				window.down(i)
			}
		}
		return
	}
	// The root is the worst retained result. Later matches can replace it,
	// including a better match encountered at the end of the collection.
	if window.before(candidate, window.items[0]) {
		window.items[0] = candidate
		window.down(0)
	}
}

func (window *publishedWindow) down(parent int) {
	for {
		child := 2*parent + 1
		if child >= len(window.items) {
			return
		}
		if child+1 < len(window.items) && window.before(window.items[child], window.items[child+1]) {
			child++
		}
		if !window.before(window.items[parent], window.items[child]) {
			return
		}
		window.items[parent], window.items[child] = window.items[child], window.items[parent]
		parent = child
	}
}

func (window *publishedWindow) order() {
	sort.Slice(window.items, func(i, j int) bool { return window.before(window.items[i], window.items[j]) })
}
