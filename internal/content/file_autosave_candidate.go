package content

// cloneAutosaveContentState is only for FileRepository.SaveAutosave and
// DeleteAutosave while the file lock is held. Those operations replace or
// delete autosaves; all other persisted data stays immutable. Other file
// mutations continue to deep-clone before writing any canonical or historical
// content. Do not use this candidate for arbitrary MemoryRepository mutations.
func cloneAutosaveContentState(source *MemoryRepository) *MemoryRepository {
	clone := &MemoryRepository{
		posts: source.posts, terms: source.terms, revisions: source.revisions,
		nextID: source.nextID, nextRevisionID: source.nextRevisionID, nextTermID: source.nextTermID,
		now: source.now, activities: source.activities, series: source.series,
		nextSeriesID: source.nextSeriesID, redirects: source.redirects, nextRedirectID: source.nextRedirectID,
		imports: source.imports, submissions: source.submissions,
		snippets: source.snippets, snippetReceipts: source.snippetReceipts,
		snippetReceiptBytes: source.snippetReceiptBytes, snippetReceiptCount: source.snippetReceiptCount,
		nextSnippetID: source.nextSnippetID, contentSchema: source.contentSchema,
		snippetQuarantines: source.snippetQuarantines, snippetPostCounts: source.snippetPostCounts,
		searchBodies: source.searchBodies,
		autosaves:    make(map[int64]map[string]Autosave, len(source.autosaves)),
	}
	// Every owner map is independent, including groups not being written:
	// GetAutosave can remove expired entries on either retained snapshot. The
	// stored values are immutable; save and public reads detach their metadata.
	for id, owners := range source.autosaves {
		copied := make(map[string]Autosave, len(owners))
		for owner, autosave := range owners {
			copied[owner] = autosave
		}
		clone.autosaves[id] = copied
	}
	return clone
}
