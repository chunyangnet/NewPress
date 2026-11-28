package content

import "time"

// normalizeFileAutosave is only for values owned by decodeContentDocument after
// its strict, typed JSON decode. That decode already rejects ambiguous keys,
// invalid text, unknown fields and excess depth, preserves json.Number, and
// creates independent maps and slices. Re-encoding the entire snapshot would
// repeat those checks and allocate another copy of its body and metadata.
// Caller-supplied Go values must still go through NormalizeAutosave.
func normalizeFileAutosave(autosave Autosave) (Autosave, error) {
	valid := normalizeFileAutosaveTimes(&autosave.Snapshot)
	if err := validateAutosave(autosave, valid); err != nil {
		return Autosave{}, err
	}
	// These omitempty fields previously became nil during the JSON round trip.
	if len(autosave.Snapshot.Categories) == 0 {
		autosave.Snapshot.Categories = nil
	}
	if len(autosave.Snapshot.Tags) == 0 {
		autosave.Snapshot.Tags = nil
	}
	if len(autosave.Snapshot.Meta) == 0 {
		autosave.Snapshot.Meta = nil
	}
	autosave.Snapshot.ContentHTML = sanitizeContent(autosave.Snapshot.ContentSource)
	return autosave, nil
}

func normalizeFileAutosaveTimes(snapshot *Post) bool {
	for _, stamp := range []*time.Time{&snapshot.PublishedAt, &snapshot.CreatedAt, &snapshot.UpdatedAt} {
		if !normalizeFileAutosaveTime(stamp) {
			return false
		}
	}
	for _, terms := range [][]Term{snapshot.Categories, snapshot.Tags} {
		for i := range terms {
			if !normalizeFileAutosaveTime(&terms[i].CreatedAt) || !normalizeFileAutosaveTime(&terms[i].UpdatedAt) {
				return false
			}
		}
	}
	return true
}

func normalizeFileAutosaveTime(stamp *time.Time) bool {
	// time.Time can accept offsets while decoding that it cannot marshal.
	// Retain that rejection and the same canonical UTC/location representation.
	payload, err := stamp.MarshalJSON()
	if err != nil {
		return false
	}
	var normalized time.Time
	if err := normalized.UnmarshalJSON(payload); err != nil {
		return false
	}
	*stamp = normalized
	return true
}
