package theme

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"newpress.local/newpress/internal/packagefs"
)

func loadSettingSchema(directory string, manifest Manifest) (*settingSchema, error) {
	if manifest.SettingsSchema == "" {
		return &settingSchema{Type: "object", Properties: map[string]settingProperty{}}, nil
	}
	if err := packagefs.ValidateRelativePath(manifest.SettingsSchema); err != nil {
		return nil, fmt.Errorf("unsafe settings schema path %q", manifest.SettingsSchema)
	}
	cleanPath := filepath.FromSlash(manifest.SettingsSchema)
	schemaPath := filepath.Join(directory, cleanPath)
	if info, err := packagefs.LstatNoSymlink(schemaPath); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("theme settings schema is missing, linked, or not a regular file")
	}
	file, _, err := packagefs.OpenRegularNoSymlink(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("open theme settings schema: %w", err)
	}
	defer file.Close()
	var schema settingSchema
	if err := decodeJSONDocument(file, 1<<20, &schema, false); err != nil {
		return nil, fmt.Errorf("decode theme settings schema: %w", err)
	}
	if err := validateSettingSchema(&schema); err != nil {
		return nil, err
	}
	schema.HomeLayout = manifest.Supports(HomeLayoutV1)
	return &schema, nil
}

// Allow complete public themes and home-layout controls while bounding schemas.
const maxThemeSettingFields = 256

func validateSettingSchema(schema *settingSchema) error {
	if schema.Type != "object" {
		return errors.New("theme settings schema root type must be object")
	}
	if schema.AdditionalProperties {
		return errors.New("theme settings schema must set additionalProperties to false")
	}
	if schema.UI.Version != 0 && schema.UI.Version != 1 {
		return fmt.Errorf("unsupported theme settings UI schema version %d", schema.UI.Version)
	}
	if len(schema.Properties) > maxThemeSettingFields {
		return fmt.Errorf("theme settings schema cannot contain more than %d fields", maxThemeSettingFields)
	}
	if len(schema.UI.Groups) > 64 {
		return errors.New("theme settings schema cannot contain more than 64 groups")
	}
	groups := make(map[string]struct{}, len(schema.UI.Groups))
	for _, group := range schema.UI.Groups {
		if !settingKeyPattern.MatchString(group.ID) {
			return fmt.Errorf("invalid settings group %q", group.ID)
		}
		if _, exists := groups[group.ID]; exists {
			return fmt.Errorf("duplicate settings group %q", group.ID)
		}
		groups[group.ID] = struct{}{}
		if group.Icon != "" && !cssClassPattern.MatchString(group.Icon) {
			return fmt.Errorf("invalid settings group icon %q", group.Icon)
		}
	}
	required := make(map[string]struct{}, len(schema.Required))
	for _, key := range schema.Required {
		if _, exists := required[key]; exists {
			return fmt.Errorf("duplicate required theme setting %q", key)
		}
		if _, exists := schema.Properties[key]; !exists {
			return fmt.Errorf("required theme setting %q is not defined", key)
		}
		required[key] = struct{}{}
	}
	validControls := map[string]struct{}{
		"": {}, "text": {}, "textarea": {}, "color": {}, "toggle": {}, "range": {},
		"segmented": {}, "radio-card": {}, "select": {}, "number": {}, "image": {},
	}
	for key, property := range schema.Properties {
		if !settingKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid theme setting key %q", key)
		}
		if property.Type != "string" && property.Type != "boolean" && property.Type != "integer" && property.Type != "number" {
			return fmt.Errorf("theme setting %s uses unsupported type %q", key, property.Type)
		}
		if property.UI.Group != "" {
			if _, ok := groups[property.UI.Group]; !ok {
				return fmt.Errorf("theme setting %s references unknown group %q", key, property.UI.Group)
			}
		}
		if property.Pattern != "" {
			if _, err := regexp.Compile(property.Pattern); err != nil {
				return fmt.Errorf("theme setting %s has invalid pattern: %w", key, err)
			}
		}
		if property.Format != "" && property.Format != "uri-reference" && !(key == "home_custom_blocks" && property.Type == "string" && property.Format == "home-html-blocks") {
			return fmt.Errorf("theme setting %s uses unsupported format %q", key, property.Format)
		}
		if _, ok := validControls[property.UI.Control]; !ok {
			return fmt.Errorf("theme setting %s uses unsupported control %q", key, property.UI.Control)
		}
		if property.UI.Control == "image" && (property.Type != "string" || property.Format != "uri-reference") {
			return fmt.Errorf("theme image setting %s must be a uri-reference string", key)
		}
		if !safeCSSUnit(property.UI.CSSUnit) {
			return fmt.Errorf("theme setting %s uses unsafe CSS unit %q", key, property.UI.CSSUnit)
		}
		for choice, rendered := range property.UI.CSSMap {
			if choice == "" || !safeCSSValue(rendered) {
				return fmt.Errorf("theme setting %s has unsafe CSS mapping for %q", key, choice)
			}
		}
		if property.UI.CSSVariable != "" && !cssVariablePattern.MatchString(property.UI.CSSVariable) {
			return fmt.Errorf("theme setting %s has unsafe CSS variable %q", key, property.UI.CSSVariable)
		}
		for _, className := range []string{property.UI.ClassWhenTrue, property.UI.ClassWhenFalse} {
			if className != "" && !cssClassPattern.MatchString(className) {
				return fmt.Errorf("theme setting %s has unsafe CSS class %q", key, className)
			}
		}
		if property.UI.ClassPrefix != "" && !cssClassPattern.MatchString(strings.TrimSuffix(property.UI.ClassPrefix, "-")) {
			return fmt.Errorf("theme setting %s has unsafe CSS class prefix %q", key, property.UI.ClassPrefix)
		}
		if property.Minimum != nil && property.Maximum != nil && *property.Minimum > *property.Maximum {
			return fmt.Errorf("theme setting %s minimum exceeds maximum", key)
		}
		if property.MinLength != nil && *property.MinLength < 0 || property.MaxLength != nil && *property.MaxLength < 0 {
			return fmt.Errorf("theme setting %s has a negative length constraint", key)
		}
		if property.MinLength != nil && property.MaxLength != nil && *property.MinLength > *property.MaxLength {
			return fmt.Errorf("theme setting %s minLength exceeds maxLength", key)
		}
		limit := settingTextLimit(key, property)
		if property.MinLength != nil && *property.MinLength > limit || property.MaxLength != nil && *property.MaxLength > limit {
			return fmt.Errorf("theme setting %s length constraints cannot exceed %d", key, limit)
		}
		if len(property.Enum) > 256 || len(property.OneOf) > 256 {
			return fmt.Errorf("theme setting %s cannot contain more than 256 choices", key)
		}
		if len(property.UI.CSSMap) > 256 {
			return fmt.Errorf("theme setting %s cannot contain more than 256 CSS mappings", key)
		}
		if property.UI.Step < 0 {
			return fmt.Errorf("theme setting %s has a negative UI step", key)
		}
		if _, err := normalizeSettingValue(key, property, property.Default); err != nil {
			if _, isRequired := required[key]; isRequired || property.Default != nil {
				return fmt.Errorf("invalid default for theme setting %s: %w", key, err)
			}
		}
	}
	return nil
}
