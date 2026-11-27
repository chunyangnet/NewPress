package theme

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/layout"
)

func (schema *settingSchema) defaults() map[string]any {
	values := make(map[string]any, len(schema.Properties))
	for key, property := range schema.Properties {
		value := property.Default
		if value == nil {
			switch property.Type {
			case "boolean":
				value = false
			case "integer":
				value = int64(0)
			case "number":
				value = float64(0)
			default:
				value = ""
			}
		}
		normalized, err := normalizeSettingValue(key, property, value)
		if err == nil {
			values[key] = normalized
		}
	}
	return values
}

func (schema *settingSchema) validate(values map[string]any) (map[string]any, map[string]string) {
	normalized := schema.defaults()
	errorsByField := make(map[string]string)
	for key := range values {
		if _, ok := schema.Properties[key]; !ok {
			errorsByField[key] = "未知设置项"
		}
	}
	for key, property := range schema.Properties {
		value, exists := values[key]
		if !exists {
			continue
		}
		clean, err := normalizeSettingValue(key, property, value)
		if err != nil {
			errorsByField[key] = err.Error()
			continue
		}
		normalized[key] = clean
	}
	if len(errorsByField) > 0 {
		return nil, errorsByField
	}
	if schema.HomeLayout {
		if fields := layout.Validate(normalized); len(fields) > 0 {
			return nil, fields
		}
	}
	return normalized, nil
}

func settingTextLimit(key string, property settingProperty) int {
	if key == "home_custom_blocks" && property.Format == "home-html-blocks" {
		return layout.MaxCustomBlocksBytes
	}
	return 4096
}

func normalizeSettingValue(key string, property settingProperty, value any) (any, error) {
	switch property.Type {
	case "string":
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("必须是文本")
		}
		if property.MinLength != nil && len([]rune(text)) < *property.MinLength {
			return nil, fmt.Errorf("至少需要 %d 个字符", *property.MinLength)
		}
		maxLength := settingTextLimit(key, property)
		if property.MaxLength != nil {
			maxLength = *property.MaxLength
		}
		if len([]rune(text)) > maxLength {
			return nil, fmt.Errorf("不能超过 %d 个字符", maxLength)
		}
		if key == "home_custom_blocks" && property.Format == "home-html-blocks" {
			if _, err := layout.ParseCustomBlocks(text); err != nil {
				return nil, err
			}
		}
		if property.Pattern != "" && !regexp.MustCompile(property.Pattern).MatchString(text) {
			return nil, errors.New("格式不符合要求")
		}
		if property.Format == "uri-reference" && text != "" {
			parsed, err := url.Parse(text)
			if err != nil || strings.ContainsAny(text, "\x00\r\n") || strings.EqualFold(parsed.Scheme, "javascript") || strings.EqualFold(parsed.Scheme, "data") || strings.EqualFold(parsed.Scheme, "vbscript") {
				return nil, errors.New("链接格式不合法")
			}
		}
		if !matchesAllowedValue(property, text) {
			return nil, errors.New("不在允许的选项中")
		}
		return text, nil
	case "boolean":
		boolean, ok := value.(bool)
		if !ok {
			return nil, errors.New("必须是布尔值")
		}
		return boolean, nil
	case "integer":
		number, ok := integerValue(value)
		if !ok {
			return nil, errors.New("必须是整数")
		}
		if err := validateNumberRange(property, float64(number)); err != nil {
			return nil, err
		}
		return number, nil
	case "number":
		number, ok := numericValue(value)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, errors.New("必须是数字")
		}
		if err := validateNumberRange(property, number); err != nil {
			return nil, err
		}
		return number, nil
	default:
		return nil, fmt.Errorf("设置 %s 的类型不受支持", key)
	}
}

func integerValue(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int64:
		return number, true
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < math.MinInt64 || number >= float64(math.MaxInt64) {
			return 0, false
		}
		return int64(number), true
	case json.Number:
		parsed, err := number.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func numericValue(value any) (float64, bool) {
	switch number := value.(type) {
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case float64:
		return number, true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func validateNumberRange(property settingProperty, number float64) error {
	if property.Minimum != nil && number < *property.Minimum {
		return fmt.Errorf("不能小于 %s", formatNumber(*property.Minimum))
	}
	if property.Maximum != nil && number > *property.Maximum {
		return fmt.Errorf("不能大于 %s", formatNumber(*property.Maximum))
	}
	return nil
}

func matchesAllowedValue(property settingProperty, value string) bool {
	if len(property.Enum) == 0 && len(property.OneOf) == 0 {
		return true
	}
	for _, candidate := range property.Enum {
		if candidate == value {
			return true
		}
	}
	for _, candidate := range property.OneOf {
		if candidate.Const == value {
			return true
		}
	}
	return false
}

func parseSettingsForm(schema *settingSchema, form url.Values) (map[string]any, map[string]string) {
	values := make(map[string]any, len(schema.Properties))
	errorsByField := make(map[string]string)
	for key, property := range schema.Properties {
		raw := strings.TrimSpace(form.Get(key))
		switch property.Type {
		case "boolean":
			values[key] = raw == "true" || raw == "1" || raw == "on"
		case "integer":
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				errorsByField[key] = "请输入有效整数"
				continue
			}
			values[key] = parsed
		case "number":
			parsed, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				errorsByField[key] = "请输入有效数字"
				continue
			}
			values[key] = parsed
		default:
			values[key] = raw
		}
	}
	if len(errorsByField) > 0 {
		return nil, errorsByField
	}
	return schema.validate(values)
}

func cloneSettings(values map[string]any) map[string]any {
	copyValues := make(map[string]any, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	return copyValues
}

func formatNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
