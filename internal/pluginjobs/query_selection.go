package pluginjobs

import (
	"container/heap"
	"slices"
	"strings"
	"time"
)

type taskPosition struct {
	id      string
	updated time.Time
}

func compareTaskPosition(a, b taskPosition) int {
	if order := b.updated.Compare(a.updated); order != 0 {
		return order
	}
	return strings.Compare(a.id, b.id)
}

// Keep only the leading offset+limit identities. Full summaries and their
// nested review values are copied only for the returned page.
type taskSelection struct{ values []taskPosition }

func (s *taskSelection) Len() int           { return len(s.values) }
func (s *taskSelection) Less(i, j int) bool { return compareTaskPosition(s.values[i], s.values[j]) > 0 }
func (s *taskSelection) Swap(i, j int)      { s.values[i], s.values[j] = s.values[j], s.values[i] }
func (s *taskSelection) Push(value any)     { s.values = append(s.values, value.(taskPosition)) }
func (s *taskSelection) Pop() any {
	last := len(s.values) - 1
	value := s.values[last]
	s.values[last] = taskPosition{}
	s.values = s.values[:last]
	return value
}

func taskPage(state State, query Query) Page {
	limit := query.Offset + query.Limit // NormalizeQuery bounds both values.
	selection := &taskSelection{values: make([]taskPosition, 0, min(limit, 100))}
	page := Page{Items: []Summary{}}
	add := func(id string, updated time.Time) {
		page.Total++
		candidate := taskPosition{id: id, updated: updated}
		if selection.Len() < limit {
			heap.Push(selection, candidate)
		} else if compareTaskPosition(candidate, selection.values[0]) < 0 {
			selection.values[0] = candidate
			heap.Fix(selection, 0)
		}
	}
	if query.Archived {
		for _, job := range state.ArchivedJobs {
			if job.Plugin == query.Plugin && (query.Status == "" || job.Status == query.Status) {
				add(job.ID, job.UpdatedAt)
			}
		}
	} else {
		for _, job := range state.Jobs {
			if job.Plugin == query.Plugin && (query.Status == "" || job.Status == query.Status) {
				add(job.ID, job.UpdatedAt)
			}
		}
	}
	slices.SortFunc(selection.values, compareTaskPosition)
	start := min(query.Offset, selection.Len())
	page.Items = make([]Summary, 0, selection.Len()-start)
	for _, position := range selection.values[start:] {
		if query.Archived {
			item := state.ArchivedJobs[position.id].Summary
			if item.Reissue != nil {
				reissue := *item.Reissue
				item.Reissue = &reissue
			}
			page.Items = append(page.Items, item)
		} else {
			page.Items = append(page.Items, Summarize(state.Jobs[position.id]))
		}
	}
	return page
}
