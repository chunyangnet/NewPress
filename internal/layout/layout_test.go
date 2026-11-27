package layout

import "testing"

func TestCrossRegionPlacementAndVisibility(t *testing.T) {
	v := map[string]any{"home_top_order": "notice,recent", "home_main_order": "feed,hero", "home_aside_order": "", "home_hero_visibility": "mobile", "sidebar_enabled": false}
	if fields := Validate(v); len(fields) > 0 {
		t.Fatal(fields)
	}
	r := Resolve(v)
	if r[0].Modules[1].Key != "recent" || r[1].Modules[1].Key != "hero" {
		t.Fatal("explicit placement lost")
	}
	seen := map[string]bool{}
	for _, region := range r {
		for _, b := range region.Modules {
			if seen[b.Key] {
				t.Fatalf("duplicate %s", b.Key)
			}
			seen[b.Key] = true
		}
	}
	if len(seen) != 19 || !Requested(v, "recent") || Requested(v, "tags") {
		t.Fatal("default completion or sidebar visibility")
	}
	v["home_main_order"] = "feed,recent"
	if len(Validate(v)) == 0 {
		t.Fatal("cross-region duplicate accepted")
	}
	v["home_main_order"] = "unregistered"
	if len(Validate(v)) == 0 {
		t.Fatal("unknown block accepted")
	}
}

func TestNewCreativeBlocksAreOptIn(t *testing.T) {
	for _, key := range []string{"reading_guide", "updated", "carousel", "search_banner"} {
		if Requested(nil, key) {
			t.Fatalf("new block %s unexpectedly enabled for old settings", key)
		}
		if !Requested(map[string]any{"home_" + key + "_visibility": "mobile"}, key) {
			t.Fatalf("block %s cannot be enabled", key)
		}
	}
}
