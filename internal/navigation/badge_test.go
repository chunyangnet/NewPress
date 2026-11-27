package navigation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNavigationBadgeValidationAndCSS(t *testing.T) {
	for _, style := range []string{"solid", "pill", "outline", "soft", "gradient", "ribbon", "bubble"} {
		badge, fields := NormalizeBadge(Badge{Text: " 最新 ", Style: style, Background: "#aabbcc", OffsetX: -12, OffsetY: 12})
		if len(fields) != 0 || badge.Text != "最新" || badge.Background != "#AABBCC" || !strings.Contains(badge.CSSVariables(), "--nav-badge-x:-12px") {
			t.Fatalf("%s: %#v %#v", style, badge, fields)
		}
	}
	for _, badge := range []Badge{
		{Text: strings.Repeat("新", 13)}, {Text: "新\n闻"}, {Text: "新", Style: "url(evil)"},
		{Text: "新", Placement: "absolute"}, {Text: "新", Background: "red;background:url(evil)"},
		{Text: "新", Color: "#fff"}, {Text: "新", BorderColor: "</style>"}, {Text: "新", GradientTo: "transparent"},
		{Text: "新", FontSize: 99}, {Text: "新", Radius: -1}, {Text: "新", OffsetX: 13}, {Text: "新", OffsetY: -13},
	} {
		if _, fields := NormalizeBadge(badge); len(fields) == 0 || badge.CSSVariables() != "" {
			t.Fatalf("accepted unsafe badge: %#v", badge)
		}
	}
	items, fields := NormalizeItemInputs([]ItemInput{{Label: "首页", URL: "/", Badge: Badge{Text: "新", OffsetY: 99}}})
	if len(items) != 1 || fields["items.0.badge.offset_y"] == "" {
		t.Fatal("missing item-scoped error", fields)
	}
	for _, payload := range []string{`[{"badge":{"text":"新","text":"旧"}}]`, `[{"badge":{"text":"新","css":"evil"}}]`} {
		if _, err := DecodeItemInputs(payload); err == nil {
			t.Fatal("accepted unknown/duplicate badge field")
		}
	}
}

func TestNavigationBadgePersistenceAndLegacyOmission(t *testing.T) {
	old, _ := json.Marshal(Item{ID: 1, Label: "旧导航"})
	if strings.Contains(string(old), `"badge"`) {
		t.Fatal("zero badge changed legacy JSON and audit digests", string(old))
	}
	path := filepath.Join(t.TempDir(), "navigation.json")
	store, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	menu, err := store.Create(t.Context(), MenuInput{Name: "角标", Slug: "badges", Locations: []string{LocationUtility, LocationMobileAction}}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	badge, _ := NormalizeBadge(Badge{Text: "DIY", Style: "gradient", Radius: 7, OffsetY: -3, HideOnMobile: true})
	menu, err = store.ReplaceItems(t.Context(), menu.ID, Mutation{ExpectedVersion: menu.Version, Actor: "owner"}, []ItemInput{{ID: -1, Label: "星球", URL: "/community/", Badge: badge}})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Get(t.Context(), menu.ID)
	if err != nil || got.Items[0].Badge != badge {
		t.Fatal("badge lost on reload", got, err)
	}
	if action, err := reloaded.GetLocation(t.Context(), LocationMobileAction); err != nil || action.ID != menu.ID {
		t.Fatal("central action assignment lost on reload", action, err)
	}
	snapshot, _ := reloaded.Snapshot(t.Context())
	imported, err := reloaded.ReplaceAll(t.Context(), RegistryMutation{ExpectedRevision: SnapshotRevision(snapshot), Actor: "owner"}, []RegistryMenuInput{{Menu: MenuInput{Name: "导入", Slug: "imported"}, Items: []ItemInput{{ID: -1, Label: "问答", URL: "/", Badge: badge}}}})
	if err != nil || imported.Menus[0].Items[0].Badge != badge {
		t.Fatal("registry lost badge", err)
	}
	current := imported.Menus[0]
	_, err = reloaded.ReplaceItems(t.Context(), current.ID, Mutation{ExpectedVersion: current.Version, Actor: "owner"}, []ItemInput{{ID: current.Items[0].ID, Label: "问答", URL: "/"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"badge"`) {
		t.Fatal("cleared badge persisted", string(raw))
	}
}

func TestNavigationBadgeMobileVisibilityCompatibility(t *testing.T) {
	legacy := `{"text":"旧","style":"bubble","placement":"corner","background":"#D14343","color":"#FFFFFF","border_color":"#D14343","gradient_to":"#EF7C3E","font_size":11,"radius":4,"offset_x":0,"offset_y":0}`
	var badge Badge
	if err := json.Unmarshal([]byte(legacy), &badge); err != nil || badge.HideOnMobile {
		t.Fatal("legacy badge visibility changed", err)
	}
	raw, _ := json.Marshal(badge)
	if string(raw) != legacy {
		t.Fatal("legacy badge audit bytes changed", string(raw))
	}
	badge.Text, badge.HideOnMobile = "", true
	cleared, fields := NormalizeBadge(badge)
	if len(fields) != 0 || cleared != (Badge{}) {
		t.Fatal("cleared badge retained visibility metadata", cleared, fields)
	}
	if _, err := DecodeItemInputs(`[{"label":"首页","url":"/","badge":{"text":"新","hide_on_mobile":"false"}}]`); err == nil {
		t.Fatal("accepted nonboolean visibility")
	}
}
