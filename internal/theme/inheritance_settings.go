package theme

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"

	"newpress.local/newpress/internal/packagefs"
)

func overlaySettings(directory string, manifest Manifest, parent *settingSchema) (*settingSchema, error) {
	f, _, err := packagefs.OpenRegularNoSymlink(filepath.Join(directory, filepath.FromSlash(manifest.SettingsSchema)))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var patch struct {
		JSONSchema           string                     `json:"$schema"`
		Title                string                     `json:"title"`
		Description          string                     `json:"description"`
		Type                 string                     `json:"type"`
		AdditionalProperties bool                       `json:"additionalProperties"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
		UI                   schemaUI                   `json:"x-newpress"`
	}
	if err := decodeJSONDocument(f, 1<<20, &patch, true); err != nil {
		return nil, err
	}
	if patch.Type != "object" || patch.AdditionalProperties {
		return nil, fmt.Errorf("child settings must be a closed object")
	}
	encoded, err := json.Marshal(parent)
	if err != nil {
		return nil, err
	}
	var merged settingSchema
	if err = json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	merged.Sources = make(map[string]string, len(merged.Properties))
	for key := range merged.Properties {
		merged.Sources[key] = "继承父主题字段"
	}
	for key, raw := range patch.Properties {
		if property, exists := merged.Properties[key]; exists {
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(raw, &fields); err != nil {
				return nil, err
			}
			if fields == nil {
				return nil, fmt.Errorf("invalid setting patch %s", key)
			}
			for name, value := range fields {
				switch name {
				case "default":
					err = json.Unmarshal(value, &property.Default)
				case "title":
					err = json.Unmarshal(value, &property.Title)
				case "description":
					err = json.Unmarshal(value, &property.Description)
				default:
					return nil, fmt.Errorf("inherited setting %s cannot change %s", key, name)
				}
				if err != nil {
					return nil, err
				}
			}
			if _, changed := fields["default"]; changed {
				merged.Sources[key] = "子主题调整默认值"
			} else if len(fields) > 0 {
				merged.Sources[key] = "继承父主题字段 · 子主题调整说明"
			}
			merged.Properties[key] = property
		} else {
			var property settingProperty
			if err = decodeJSONDocument(bytes.NewReader(raw), 1<<20, &property, true); err != nil {
				return nil, err
			}
			merged.Properties[key] = property
			merged.Sources[key] = "子主题新增字段"
		}
	}
	for _, group := range patch.UI.Groups {
		for _, existing := range merged.UI.Groups {
			if existing.ID == group.ID {
				return nil, fmt.Errorf("duplicate inherited settings group %s", group.ID)
			}
		}
		merged.UI.Groups = append(merged.UI.Groups, group)
	}
	if patch.UI.Version != 0 && patch.UI.Version != 1 {
		return nil, fmt.Errorf("unsupported settings UI version")
	}
	for _, key := range patch.Required {
		if !slices.Contains(merged.Required, key) {
			merged.Required = append(merged.Required, key)
		}
	}
	if patch.Title != "" {
		merged.Title = patch.Title
	}
	if patch.Description != "" {
		merged.Description = patch.Description
	}
	if err = validateSettingSchema(&merged); err != nil {
		return nil, err
	}
	return &merged, nil
}
