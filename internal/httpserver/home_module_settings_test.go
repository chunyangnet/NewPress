package httpserver

import (
	"strings"
	"testing"

	"newpress.local/newpress/internal/layout"
)

func TestHomeModuleSettingsRenderedOnce(t *testing.T) {
	s, handler, _, cookie, _ := newAuthenticatedContentServer(t)
	snapshot := s.themes.SettingsSnapshot()
	for _, path := range []string{"/admin/appearance/", "/admin/appearance/layout/"} {
		t.Run(path, func(t *testing.T) {
			response := serveContentAPI(handler, cookie, "GET", "https://example.test"+path, "", nil)
			if response.Code != 200 {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			body := response.Body.String()
			for _, region := range layout.Resolve(snapshot.Values) {
				for _, block := range region.Modules {
					if block.Custom != nil {
						continue
					}
					if !strings.Contains(body, `data-home-config="`+block.Key+`"`) {
						t.Errorf("missing disclosure for %s", block.Key)
					}
				}
			}
			for _, panel := range homeSettingsFor(snapshot.Groups) {
				for _, field := range panel.Fields {
					if n := strings.Count(body, `class="home-module-field" data-setting-field="`+field.Key+`"`); n != 1 {
						t.Errorf("%s rendered %d times", field.Key, n)
					}
				}
			}
			form := renderedAppearanceForm(t, body)
			if _, errors := s.themes.ParseSettingsForm(form); len(errors) != 0 {
				t.Fatalf("invalid rendered form: %v", errors)
			}
		})
	}
}

func TestHomeSettingOwners(t *testing.T) {
	for key, want := range map[string]string{
		"home_notice_list_title": "notice_list", "home_notice_title": "notice", "home_carousel_visibility": "",
		"home_top_order": "", "home_featured_ids": "spotlight", "home_member_palette": "welcome",
		"home_checkin_angle": "checkin", "home_slide_4_image": "carousel", "home_guide_2_url": "reading_guide",
		"home_module_gap": "", "sidebar_enabled": "", "home_custom_blocks": "",
	} {
		if got := homeSettingOwner(key); got != want {
			t.Errorf("%s: got %q want %q", key, got, want)
		}
	}
}
