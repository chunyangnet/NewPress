package content

import "sort"

// File commits hold their detached candidate unchanged while encoding. Keep
// only ordering references instead of copying every large history record.
type contentEncodingHistory struct {
	revisions []*Revision
	autosaves []contentEncodingAutosave
}

type contentEncodingAutosave struct {
	owners   map[string]Autosave
	key      string
	postID   int64
	ownerKey string
}

func contentHistoryForEncoding(state *MemoryRepository) contentEncodingHistory {
	count := 0
	for _, rows := range state.revisions {
		count += len(rows)
	}
	history := contentEncodingHistory{revisions: make([]*Revision, 0, count)}
	for _, rows := range state.revisions {
		for i := range rows {
			history.revisions = append(history.revisions, &rows[i])
		}
	}
	sort.Slice(history.revisions, func(i, j int) bool { return history.revisions[i].ID < history.revisions[j].ID })
	// Select once, before any output, using the existing per-record clock
	// checks. Entries that expire during output remain in this selection.
	for _, owners := range state.autosaves {
		for key, autosave := range owners {
			if autosave.ExpiresAt.After(state.now()) {
				history.autosaves = append(history.autosaves, contentEncodingAutosave{owners, key, autosave.PostID, autosave.OwnerKey})
			}
		}
	}
	sort.Slice(history.autosaves, func(i, j int) bool {
		left, right := history.autosaves[i], history.autosaves[j]
		if left.postID == right.postID {
			return left.ownerKey < right.ownerKey
		}
		return left.postID < right.postID
	})
	return history
}

func (history *contentEncodingHistory) write(e *contentJSONEncoder) {
	contentJSONArray(e, "revisions", history.revisions, false)
	if e.err != nil {
		return
	}
	e.key("autosaves")
	e.text("[")
	// Map entries are not addressable. Reuse one value slot for synchronous
	// standard encoding; do not allocate one complete record per selection.
	var autosave Autosave
	for i, ref := range history.autosaves {
		if e.err != nil {
			return
		}
		if i != 0 {
			e.text(",")
		}
		autosave = ref.owners[ref.key]
		e.value(&autosave)
	}
	e.text("]")
}
