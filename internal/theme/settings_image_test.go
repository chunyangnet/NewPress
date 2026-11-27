package theme

import "testing"

func TestImageSettingsRequireExplicitControls(t *testing.T) {
	for _, key := range []string{"home_slide_1_image", "home_hero_cover", "home_search_poster", "logo_image", "contact_qr_image", "product_image", "feature_1_image"} {
		property := settingProperty{Type: "string", Format: "uri-reference", UI: fieldUI{Control: "text"}}
		field := settingFieldView(key, property, "/media/1/image.png")
		if field.Control != "text" || field.StringValue != "/media/1/image.png" {
			t.Fatalf("%s: %+v", key, field)
		}
	}
	for _, key := range []string{"home_slide_1_url", "home_search_video", "hero_link", "contact_map_url"} {
		if field := settingFieldView(key, settingProperty{Type: "string", Format: "uri-reference"}, ""); field.Control != "text" {
			t.Fatalf("%s: %s", key, field.Control)
		}
	}
	if field := settingFieldView("hero_image_alt", settingProperty{Type: "string"}, ""); field.Control != "text" {
		t.Fatal(field)
	}
	if field := settingFieldView("cover_ratio", settingProperty{Type: "string", Enum: []any{"wide", "square"}}, "wide"); field.Control != "select" {
		t.Fatal(field)
	}
}

func TestExplicitImageControlRequiresURLString(t *testing.T) {
	for _, format := range []string{"uri-reference", ""} {
		schema := &settingSchema{Type: "object", Properties: map[string]settingProperty{"artwork": {Type: "string", Format: format, UI: fieldUI{Control: "image"}}}}
		err := validateSettingSchema(schema)
		if (err == nil) != (format == "uri-reference") {
			t.Fatalf("%q: %v", format, err)
		}
	}
}
