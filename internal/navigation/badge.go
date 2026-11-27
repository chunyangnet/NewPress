package navigation

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Badge is optional, presentation-safe navigation metadata.
type Badge struct {
	Text         string `json:"text"`
	Style        string `json:"style"`
	Placement    string `json:"placement"`
	Background   string `json:"background"`
	Color        string `json:"color"`
	BorderColor  string `json:"border_color"`
	GradientTo   string `json:"gradient_to"`
	FontSize     int    `json:"font_size"`
	Radius       int    `json:"radius"`
	OffsetX      int    `json:"offset_x"`
	OffsetY      int    `json:"offset_y"`
	HideOnMobile bool   `json:"hide_on_mobile,omitempty"`
}

func NormalizeBadge(badge Badge) (Badge, map[string]string) {
	fields := make(map[string]string)
	badge.Text = strings.TrimSpace(badge.Text)
	if badge.Text == "" {
		return Badge{}, fields
	}
	if !utf8.ValidString(badge.Text) || utf8.RuneCountInString(badge.Text) > 12 || containsControl(badge.Text) {
		fields["text"] = "角标文字最多 12 个字符，不能包含控制字符"
	}
	badge.Style = strings.ToLower(strings.TrimSpace(badge.Style))
	if badge.Style == "" {
		badge.Style = "bubble"
	}
	switch badge.Style {
	case "solid", "pill", "outline", "soft", "gradient", "ribbon", "bubble":
	default:
		fields["style"] = "角标样式不受支持"
	}
	badge.Placement = strings.ToLower(strings.TrimSpace(badge.Placement))
	if badge.Placement == "" {
		badge.Placement = "corner"
	}
	if badge.Placement != "corner" && badge.Placement != "inline" {
		fields["placement"] = "角标位置不受支持"
	}
	textColor := "#FFFFFF"
	if badge.Style == "outline" || badge.Style == "soft" {
		textColor = "#D14343"
	}
	for _, field := range []struct {
		key      string
		value    *string
		fallback string
	}{
		{"background", &badge.Background, "#D14343"},
		{"color", &badge.Color, textColor},
		{"border_color", &badge.BorderColor, "#D14343"},
		{"gradient_to", &badge.GradientTo, "#EF7C3E"},
	} {
		*field.value = strings.ToUpper(strings.TrimSpace(*field.value))
		if *field.value == "" {
			*field.value = field.fallback
		}
		if !colorPattern.MatchString(*field.value) {
			fields[field.key] = "颜色必须是六位十六进制颜色"
		}
	}
	if badge.FontSize == 0 {
		badge.FontSize = 11
	}
	for _, field := range []struct {
		key             string
		value, min, max int
	}{
		{"font_size", badge.FontSize, 10, 16}, {"radius", badge.Radius, 0, 20},
		{"offset_x", badge.OffsetX, -12, 12}, {"offset_y", badge.OffsetY, -12, 12},
	} {
		if field.value < field.min || field.value > field.max {
			fields[field.key] = fmt.Sprintf("数值必须在 %d 到 %d 之间", field.min, field.max)
		}
	}
	return badge, fields
}

// CSSVariables emits only normalized color tokens and bounded integers.
func (badge Badge) CSSVariables() string {
	value, fields := NormalizeBadge(badge)
	if value.Text == "" || len(fields) > 0 {
		return ""
	}
	return fmt.Sprintf("--nav-badge-bg:%s;--nav-badge-color:%s;--nav-badge-border:%s;--nav-badge-end:%s;--nav-badge-size:%dpx;--nav-badge-radius:%dpx;--nav-badge-x:%dpx;--nav-badge-y:%dpx;",
		value.Background, value.Color, value.BorderColor, value.GradientTo, value.FontSize, value.Radius, value.OffsetX, value.OffsetY)
}
