package content

import "newpress.local/newpress/internal/packagefs"

// NormalizeAutosave validates the recovery envelope and regenerates its safe
// HTML projection. Editorial fields may be incomplete and must remain unchanged
// so loading a recovery point does not discard the editor's work. Arbitrary
// metadata is frozen as independent JSON values with exact json.Number values;
// its caller-owned Go pointers, buffers and custom encoders are not retained.
func NormalizeAutosave(autosave Autosave) (Autosave, error) {
	payload, encodingErr := packagefs.MarshalUniqueJSON(autosave.Snapshot)
	if err := validateAutosave(autosave, encodingErr == nil); err != nil {
		return Autosave{}, err
	}
	snapshot, err := packagefs.DecodeJSONBytesWithNumbers[Post](payload, int64(len(payload)), "autosave snapshot")
	if err != nil {
		return Autosave{}, &ValidationError{Fields: map[string]string{"snapshot": "自动保存快照无法转换为可持久化的 JSON 数据"}}
	}
	autosave.Snapshot = snapshot
	autosave.Snapshot.ContentHTML = sanitizeContent(autosave.Snapshot.ContentSource)
	return autosave, nil
}
