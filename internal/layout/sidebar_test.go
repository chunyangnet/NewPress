package layout

import "testing"

func TestSidebarModulesRequireThemeSupport(t *testing.T) {
	values := map[string]any{}
	for _, key := range []string{"focus_list", "topic_banner", "entry_cards", "image_grid", "notice_list"} {
		if Requested(values, key) {
			t.Fatalf("unsupported module %s visible", key)
		}
		values["home_"+key+"_visibility"] = "hidden"
	}
	regions := Resolve(values)
	if len(regions[0].Modules)+len(regions[1].Modules)+len(regions[2].Modules) != 24 {
		t.Fatal("supported modules omitted")
	}
	values["home_focus_list_visibility"] = "both"
	values["home_main_order"] = "focus_list,feed"
	if fields := Validate(values); len(fields) > 0 {
		t.Fatal(fields)
	}
	if !Requested(values, "focus_list") || Resolve(values)[1].Modules[0].Key != "focus_list" {
		t.Fatal("main placement or visibility lost")
	}
}
