package contenthtml

import "slices"

// SnippetCount is a rebuildable index entry. Parsing uses exactly the same
// token ownership rules as a reference update, including malformed subtrees.
type SnippetCount struct {
	SnippetKey
	Count int
}

func CountSnippets(source string) []SnippetCount {
	counts := map[SnippetKey]int{}
	for _, ref := range SnippetReferences(source) {
		if ref.ID > 0 && ref.Version > 0 {
			counts[ref.SnippetKey]++
		}
	}
	result := make([]SnippetCount, 0, len(counts))
	for key, count := range counts {
		result = append(result, SnippetCount{SnippetKey: key, Count: count})
	}
	slices.SortFunc(result, func(a, b SnippetCount) int {
		if a.ID != b.ID {
			if a.ID < b.ID {
				return -1
			}
			return 1
		}
		if a.Version < b.Version {
			return -1
		}
		if a.Version > b.Version {
			return 1
		}
		return 0
	})
	return result
}
