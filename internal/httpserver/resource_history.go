package httpserver

import (
	"container/heap"
	"net/http"
	"sort"
	"time"

	"newpress.local/newpress/internal/content"
)

const resourceHistoryLimit = 200

type resourceHistoryEntry struct {
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Variant   string    `json:"variant,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	index     int
}

func newerResourceHistory(a, b resourceHistoryEntry) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.URL < b.URL
}

// Keep the oldest retained item at the root, so all sources can contribute
// their newest distinct resources without retaining or sorting whole histories.
type resourceHistoryHeap []*resourceHistoryEntry

func (h resourceHistoryHeap) Len() int           { return len(h) }
func (h resourceHistoryHeap) Less(i, j int) bool { return newerResourceHistory(*h[j], *h[i]) }
func (h resourceHistoryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *resourceHistoryHeap) Push(value any) {
	entry := value.(*resourceHistoryEntry)
	entry.index = len(*h)
	*h = append(*h, entry)
}
func (h *resourceHistoryHeap) Pop() any {
	last := len(*h) - 1
	entry := (*h)[last]
	(*h)[last] = nil
	*h = (*h)[:last]
	return entry
}

type resourceHistoryPage struct {
	items resourceHistoryHeap
	byURL map[string]*resourceHistoryEntry
}

func (p *resourceHistoryPage) accepts(at time.Time) bool {
	return len(p.items) < resourceHistoryLimit || !at.Before(p.items[0].CreatedAt)
}

func (p *resourceHistoryPage) add(entry resourceHistoryEntry) {
	if old := p.byURL[entry.URL]; old != nil {
		if entry.CreatedAt.After(old.CreatedAt) {
			entry.index = old.index
			*old = entry
			heap.Fix(&p.items, old.index)
		}
		return
	}
	if len(p.items) >= resourceHistoryLimit {
		if !newerResourceHistory(entry, *p.items[0]) {
			return
		}
		old := heap.Pop(&p.items).(*resourceHistoryEntry)
		delete(p.byURL, old.URL)
	}
	if p.byURL == nil {
		p.byURL = make(map[string]*resourceHistoryEntry)
	}
	retained := new(resourceHistoryEntry)
	*retained = entry
	p.byURL[entry.URL] = retained
	heap.Push(&p.items, retained)
}

func (p *resourceHistoryPage) result() []resourceHistoryEntry {
	items := make([]resourceHistoryEntry, len(p.items))
	for i, entry := range p.items {
		items[i] = *entry
	}
	sort.Slice(items, func(i, j int) bool { return newerResourceHistory(items[i], items[j]) })
	return items
}

func (s *Server) resourceHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := s.currencyPrincipal(w, r, false)
	if !ok {
		return
	}
	st, err := s.resources.Backend.Read(r.Context())
	if err != nil {
		s.resourceError(w, r, err)
		return
	}
	actor, now := resourceActorID(p.UserID), time.Now()
	paths := make(map[int64]string)
	contentPath := s.permalinkBuilder()
	postPath := func(id int64) string {
		if path, found := paths[id]; found {
			return path
		}
		path := ""
		if post, err := s.repository.GetByID(r.Context(), id); err == nil && content.PublicAt(post, now) {
			path = contentPath(post)
		}
		paths[id] = path
		return path
	}
	var page resourceHistoryPage
	for i := len(st.Deliveries) - 1; i >= 0; i-- {
		d := st.Deliveries[i]
		if d.Actor == actor && page.accepts(d.CreatedAt) {
			if path := postPath(d.PostID); path != "" {
				page.add(resourceHistoryEntry{Title: d.Title, URL: path + "#resource-" + d.Resource, Variant: d.Variant, CreatedAt: d.CreatedAt})
			}
		}
	}
	for i := len(st.Unlocks) - 1; i >= 0; i-- {
		u := st.Unlocks[i]
		if u.Actor == actor && page.accepts(u.CreatedAt) {
			if path := postPath(u.PostID); path != "" {
				page.add(resourceHistoryEntry{Title: u.Title, URL: path + "#resource-" + u.Resource, Variant: u.Variant, CreatedAt: u.CreatedAt})
			}
		}
	}
	if s.commerce != nil {
		if cs, err := s.commerce.Backend.Read(r.Context()); err == nil {
			for _, o := range cs.Orders {
				if o.UserID == p.UserID && o.Kind == "resource" && o.Status == "completed" && o.ResourceURL != "" {
					page.add(resourceHistoryEntry{Title: o.Title, URL: o.ResourceURL, CreatedAt: o.CompletedAt})
				}
			}
		}
	}
	if err := r.Context().Err(); err != nil {
		s.resourceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": page.result()})
}
