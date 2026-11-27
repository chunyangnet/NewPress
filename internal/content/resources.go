package content

import (
	"encoding/json"
	"newpress.local/newpress/internal/resources"
)

func ApplyResourceMetadata(meta map[string]any, input PostInput) {
	if input.Resources == nil {
		return
	}
	if len(input.Resources.Items) == 0 {
		delete(meta, resources.MetaKey)
		return
	}
	// Store ordinary JSON values so snapshots, revisions and both repositories own them.
	raw, _ := json.Marshal(input.Resources)
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	meta[resources.MetaKey] = value
}
func (in PostInput) ResourcesJSON() string {
	if in.Resources == nil {
		return `{"items":[]}`
	}
	raw, _ := json.Marshal(in.Resources)
	return string(raw)
}
