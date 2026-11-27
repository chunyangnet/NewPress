package content

import "newpress.local/newpress/internal/contenthtml"

func (r *MemoryRepository) indexSnippetReferencesLocked(post Post) {
	counts := contenthtml.CountSnippets(post.ContentSource)
	if len(counts) == 0 {
		delete(r.snippetPostCounts, post.ID)
		return
	}
	r.snippetPostCounts[post.ID] = counts
}

func indexedSnippetImpact(p Post, id, target int64, counts []contenthtml.SnippetCount) SnippetImpact {
	impact := SnippetImpact{ID: p.ID, Version: p.Version, Title: p.Title, Status: p.Status, Versions: []int64{}}
	for _, ref := range counts {
		if ref.ID != id {
			continue
		}
		impact.Versions = append(impact.Versions, ref.Version)
		impact.References += ref.Count
		if ref.Version != target {
			impact.Changes += ref.Count
		}
	}
	return impact
}
