package theme

import "encoding/json"

func settingDefaultHint(key string, property settingProperty) string {
	field := settingFieldView(key, property, property.Default)
	text := field.StringValue
	if property.Type == "boolean" {
		if field.BoolValue {
			text = "开启"
		} else {
			text = "关闭"
		}
	} else if property.Default == nil {
		text = "未设置"
	} else if text == "" {
		if _, ok := property.Default.(string); ok {
			text = "空字符串"
		} else {
			data, _ := json.Marshal(property.Default)
			text = string(data)
		}
	}
	for _, option := range field.Options {
		if option.Value == field.StringValue {
			text = option.Label
			break
		}
	}
	runes := []rune(text)
	if len(runes) > 120 {
		text = string(runes[:120]) + "…"
	}
	return "有效默认值：" + text
}
