package theme

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func (m *Manager) SettingsCSS() string {
	m.mu.RLock()
	schema := m.settingsSchema
	values := cloneSettings(m.settings)
	m.mu.RUnlock()
	return settingsCSS(schema, values)
}

func settingsCSS(schema *settingSchema, values map[string]any) string {
	if schema == nil {
		return ""
	}
	keys := make([]string, 0, len(schema.Properties))
	for key := range schema.Properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var css strings.Builder
	css.WriteString(":root{\n")
	for _, key := range keys {
		property := schema.Properties[key]
		if property.UI.CSSVariable == "" {
			continue
		}
		if rendered, ok := settingCSSValue(property, values[key]); ok {
			fmt.Fprintf(&css, "  %s:%s;\n", property.UI.CSSVariable, rendered)
		}
	}
	css.WriteString("}\n")
	return css.String()
}

func (m *Manager) SettingsBodyClasses() string {
	m.mu.RLock()
	schema := m.settingsSchema
	values := cloneSettings(m.settings)
	m.mu.RUnlock()
	return settingsBodyClasses(schema, values)
}

func settingsBodyClasses(schema *settingSchema, values map[string]any) string {
	if schema == nil {
		return ""
	}
	classes := make([]string, 0, 8)
	for key, property := range schema.Properties {
		value := values[key]
		if property.Type == "boolean" {
			boolean, _ := value.(bool)
			candidate := property.UI.ClassWhenFalse
			if boolean {
				candidate = property.UI.ClassWhenTrue
			}
			if candidate != "" {
				classes = append(classes, candidate)
			}
		}
		if property.UI.ClassPrefix != "" {
			if text, ok := value.(string); ok {
				candidate := property.UI.ClassPrefix + text
				if cssClassPattern.MatchString(candidate) {
					classes = append(classes, candidate)
				}
			}
		}
	}
	sort.Strings(classes)
	return strings.Join(classes, " ")
}

// PreviewSettings validates and projects an isolated candidate. It never changes
// the manager's active settings, revision, timestamps or persisted document.
func (m *Manager) PreviewSettings(values map[string]any) (SettingsSnapshot, string, string, map[string]string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.settingsSchema == nil {
		return SettingsSnapshot{}, "", "", map[string]string{"theme": "当前主题没有设置项"}
	}
	normalized, fields := m.settingsSchema.validate(values)
	if len(fields) > 0 {
		return SettingsSnapshot{}, "", "", fields
	}
	snapshot := m.settingsSnapshot(normalized)
	return snapshot, settingsCSS(m.settingsSchema, normalized), settingsBodyClasses(m.settingsSchema, normalized), nil
}

func settingCSSValue(property settingProperty, value any) (string, bool) {
	if len(property.UI.CSSMap) > 0 {
		text, ok := value.(string)
		mapped, exists := property.UI.CSSMap[text]
		return mapped, ok && exists && safeCSSValue(mapped)
	}
	switch typed := value.(type) {
	case string:
		if property.UI.Control == "color" && regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(typed) {
			return strings.ToLower(typed), true
		}
	case int64:
		if safeCSSUnit(property.UI.CSSUnit) {
			return strconv.FormatInt(typed, 10) + property.UI.CSSUnit, true
		}
	case float64:
		if safeCSSUnit(property.UI.CSSUnit) {
			return formatNumber(typed) + property.UI.CSSUnit, true
		}
	}
	return "", false
}

func safeCSSUnit(unit string) bool {
	switch unit {
	case "", "px", "rem", "em", "%", "vw", "vh", "s", "ms":
		return true
	default:
		return false
	}
}

func safeCSSValue(value string) bool {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, `{};<>\\"'`) {
		return false
	}
	return true
}
