package httpserver

import (
	"net/http/httptest"
	"newpress.local/newpress/internal/content"
	"testing"
	"time"
)

func TestHomeSpotlightDevices(t *testing.T) {
	for _, tc := range []struct {
		visibility, visual string
		desktop, mobile    int
	}{
		{"both", "editorial", 2, 2}, {"hidden", "editorial", 4, 4},
		{"desktop", "editorial", 2, 4}, {"mobile", "editorial", 4, 2},
		{"both", "minimal", 4, 4},
	} {
		t.Run(tc.visibility+tc.visual, func(t *testing.T) {
			posts := make([]content.Post, 4)
			d, m := homeSpotlight(map[string]any{"home_hero_visibility": tc.visibility, "hero_visual": tc.visual}, posts)
			if len(d) != tc.desktop || len(m) != tc.mobile {
				t.Fatalf("got %d/%d", len(d), len(m))
			}
		})
	}
	for n := 0; n <= 2; n++ {
		d, m := homeSpotlight(nil, make([]content.Post, n))
		if len(d)+len(m) != 0 {
			t.Fatal("consumed picks left an empty module")
		}
	}
	d, m := homeSpotlight(map[string]any{"home_aside_order": "hero", "sidebar_enabled": false}, make([]content.Post, 2))
	if len(d) != 2 || len(m) != 2 {
		t.Fatal("disabled sidebar consumed picks")
	}
}

func TestHomeReadingModules(t *testing.T) {
	s, _, _, repository, _, owner := newPersistentAdminTestServer(t)
	now := time.Now().Add(-time.Hour)
	for _, status := range []string{content.StatusPublish, content.StatusDraft, content.StatusPrivate} {
		_, err := repository.Create(t.Context(), content.PostInput{Title: "reading-" + status, Slug: "reading-" + status, Status: status, Type: content.TypePost, PublishedAt: now, AuthorID: owner.UserID, AuthorName: owner.Label()})
		if err != nil {
			t.Fatal(err)
		}
	}
	values := s.themes.SettingsSnapshot().Values
	values["home_updated_visibility"] = "both"
	values["home_reading_guide_visibility"] = "both"
	values["home_guide_1_title"] = "阅读"
	values["home_guide_1_url"] = "/explore/"
	values["home_guide_2_url"] = "//example.test/"
	values["home_guide_3_url"] = "javascript:alert(1)"
	view, err := s.homePage(httptest.NewRequest("GET", "https://example.test/", nil), values)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Home.Updated) != 1 || view.Home.Updated[0].Slug != "reading-publish" {
		t.Fatalf("unexpected public updates: %#v", view.Home.Updated)
	}
	if len(view.Home.GuideLinks) != 1 || view.Home.GuideLinks[0].URL != "/explore/" {
		t.Fatalf("invalid guide links: %#v", view.Home.GuideLinks)
	}
	for _, raw := range []string{"https://example.test/", "/%2fexample.test/", "/\\example.test/", "", "relative"} {
		values["home_guide_1_url"] = raw
		if len(s.homeGuideLinks(values)) != 0 {
			t.Fatalf("accepted %q", raw)
		}
	}
}
