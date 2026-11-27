package httpserver

import (
	"strings"

	"newpress.local/newpress/internal/theme"
)

// Each setting has one owner so full-form saves never receive duplicate names.
type homeModuleSettings struct {
	Fields []theme.SettingField
}

func homeSettingOwner(key string) string {
	if strings.HasSuffix(key, "_visibility") || strings.HasSuffix(key, "_order") {
		return ""
	}
	for _, rule := range []struct{ prefix, module string }{
		{"hero_", "hero"}, {"home_hero_", "hero"},
		{"home_notice_list_", "notice_list"}, {"home_notice_", "notice"},
		{"home_guide_", "reading_guide"}, {"home_carousel_", "carousel"}, {"home_slide_", "carousel"},
		{"home_search_", "search_banner"}, {"home_feed_", "feed"}, {"feed_", "feed"},
		{"home_member_", "welcome"}, {"home_checkin_", "checkin"},
		{"home_updated_", "updated"}, {"home_focus_", "focus_list"},
		{"home_topic_", "topic_banner"}, {"home_entry_", "entry_cards"}, {"home_image_grid_", "image_grid"},
	} {
		if strings.HasPrefix(key, rule.prefix) {
			return rule.module
		}
	}
	switch key {
	case "home_category_count":
		return "categories"
	case "home_series_count":
		return "series"
	case "home_featured_ids":
		return "spotlight"
	case "card_shadow", "cover_ratio", "show_excerpt", "show_content_meta":
		return "feed"
	}
	return ""
}

func homeSettingsFor(groups []theme.SettingGroup) map[string]homeModuleSettings {
	result := map[string]homeModuleSettings{}
	for _, group := range groups {
		for _, field := range group.Fields {
			owner := homeSettingOwner(field.Key)
			if owner == "" {
				continue
			}
			panel := result[owner]
			panel.Fields = append(panel.Fields, field)
			result[owner] = panel
		}
	}
	return result
}

// Leave unrelated theme settings in their original sections; fields rendered
// inside a module must not also appear in the ordinary settings form.
func withoutHomeSettings(groups []theme.SettingGroup) []theme.SettingGroup {
	result := []theme.SettingGroup{}
	for _, group := range groups {
		fields := []theme.SettingField{}
		for _, field := range group.Fields {
			if homeSettingOwner(field.Key) == "" {
				fields = append(fields, field)
			}
		}
		if len(fields) > 0 {
			group.Fields = fields
			result = append(result, group)
		}
	}
	return result
}
