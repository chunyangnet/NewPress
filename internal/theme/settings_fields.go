package theme

import (
	"sort"
	"strconv"
)

func (schema *settingSchema) groups(values map[string]any) []SettingGroup {
	required := make(map[string]struct{}, len(schema.Required))
	for _, key := range schema.Required {
		required[key] = struct{}{}
	}
	groups := append([]groupSpec(nil), schema.UI.Groups...)
	if len(groups) == 0 {
		groups = []groupSpec{{ID: "general", Title: "常规设置"}}
	}
	sort.SliceStable(groups, func(left, right int) bool {
		if groups[left].Order == groups[right].Order {
			return groups[left].ID < groups[right].ID
		}
		return groups[left].Order < groups[right].Order
	})
	views := make([]SettingGroup, 0, len(groups))
	byID := make(map[string]int, len(groups))
	for _, group := range groups {
		byID[group.ID] = len(views)
		views = append(views, SettingGroup{ID: group.ID, Title: group.Title, Description: group.Description, Icon: group.Icon})
	}
	keys := make([]string, 0, len(schema.Properties))
	for key := range schema.Properties {
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(left, right int) bool {
		leftProperty := schema.Properties[keys[left]]
		rightProperty := schema.Properties[keys[right]]
		if leftProperty.UI.Group != rightProperty.UI.Group {
			return leftProperty.UI.Group < rightProperty.UI.Group
		}
		if leftProperty.UI.Order == rightProperty.UI.Order {
			return keys[left] < keys[right]
		}
		return leftProperty.UI.Order < rightProperty.UI.Order
	})
	for _, key := range keys {
		property := schema.Properties[key]
		if property.UI.Hidden {
			continue
		}
		groupID := property.UI.Group
		if groupID == "" {
			groupID = groups[0].ID
		}
		index, ok := byID[groupID]
		if !ok {
			continue
		}
		field := settingFieldView(key, property, values[key])
		field.Source = schema.Sources[key]
		if field.Source != "" {
			field.DefaultHint = settingDefaultHint(key, property)
		}
		_, field.Required = required[key]
		views[index].Fields = append(views[index].Fields, field)
	}
	filtered := views[:0]
	for _, group := range views {
		if len(group.Fields) > 0 {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func settingFieldView(key string, property settingProperty, value any) SettingField {
	control := property.UI.Control
	if control == "" {
		switch {
		case property.Type == "boolean":
			control = "toggle"
		case len(property.Enum) > 0 || len(property.OneOf) > 0:
			control = "select"
		case property.Type == "integer" || property.Type == "number":
			control = "number"
		default:
			control = "text"
		}
	}
	field := SettingField{
		Key: key, Group: property.UI.Group, Title: property.Title, Description: property.Description,
		Control: control, Placeholder: property.UI.Placeholder, Unit: property.UI.Unit,
	}
	switch typed := value.(type) {
	case bool:
		field.BoolValue = typed
	case int64:
		field.StringValue = strconv.FormatInt(typed, 10)
	case float64:
		field.StringValue = formatNumber(typed)
	case string:
		field.StringValue = typed
	}
	if property.Minimum != nil {
		field.Min = formatNumber(*property.Minimum)
	}
	if property.Maximum != nil {
		field.Max = formatNumber(*property.Maximum)
	}
	if property.UI.Step != 0 {
		field.Step = formatNumber(property.UI.Step)
	} else if property.Type == "integer" {
		field.Step = "1"
	}
	for _, choice := range property.OneOf {
		if text, ok := choice.Const.(string); ok {
			field.Options = append(field.Options, SettingOption{Value: text, Label: choice.Title, Description: choice.Description})
		}
	}
	if len(field.Options) == 0 {
		for _, raw := range property.Enum {
			if text, ok := raw.(string); ok {
				field.Options = append(field.Options, SettingOption{Value: text, Label: text})
			}
		}
	}
	return field
}
