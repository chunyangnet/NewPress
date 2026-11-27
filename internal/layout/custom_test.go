package layout

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCustomInstancesPlacement(t *testing.T) {
	values := map[string]any{}
	values["home_custom_blocks"] = `[{"key":"custom_a","title":"甲","html":"<p>A</p>","surface":true,"visibility":"both"},{"key":"custom_b","title":"乙","html":"<p>B</p>","surface":false,"visibility":"mobile"}]`
	values["home_main_order"], values["home_aside_order"] = "custom_b,feed", "custom_a"
	if fields := Validate(values); len(fields) != 0 {
		t.Fatal(fields)
	}
	regions := Resolve(values)
	if regions[1].Modules[0].Custom.HTML != "<p>B</p>" || regions[2].Modules[0].Custom.HTML != "<p>A</p>" || Requested(values, "custom") {
		t.Fatal("instances share content or resurrect legacy")
	}
	values["home_custom_blocks"], values["home_main_order"], values["home_aside_order"] = "[]", "feed", ""
	for _, region := range Resolve(values) {
		for _, block := range region.Modules {
			if block.Custom != nil {
				t.Fatal("deleted instances came back")
			}
		}
	}
}

func TestCustomInstancesRequireExplicitCollection(t *testing.T) {
	values := map[string]any{"home_custom_title": "Old", "home_custom_html": "<p>Old</p>", "home_custom_visibility": "mobile"}
	if got := EditorCustomJSON(values); got != "[]" {
		t.Fatal("retired singleton restored", got)
	}
	values["home_top_order"] = "hero,custom"
	if len(Validate(values)) == 0 {
		t.Fatal("missing custom instance accepted")
	}
}

func TestCustomInstancesRejectMalformedAndUnboundedInput(t *testing.T) {
	valid := CustomBlock{Key: "custom_a", Title: "模块", HTML: "<p>内容</p>", Surface: true, Visibility: "both"}
	encode := func(blocks []CustomBlock) string { raw, _ := json.Marshal(blocks); return string(raw) }
	long := valid
	long.HTML = strings.Repeat("文", 4097)
	many := make([]CustomBlock, MaxCustomBlocks+1)
	for _, raw := range []string{"null", "{}", "[{}]", "[] []", `[{"key":"feed","visibility":"both"}]`, `[{"key":"custom_a","visibility":"all"}]`, `[{"key":"custom_a","visibility":"both","script":"bad"}]`, encode([]CustomBlock{valid, valid}), encode([]CustomBlock{long}), encode(many), strings.Repeat(" ", MaxCustomBlocksBytes+1)} {
		if _, err := ParseCustomBlocks(raw); err == nil {
			t.Errorf("accepted invalid input %.100s", raw)
		}
	}
	values := map[string]any{"home_custom_blocks": encode([]CustomBlock{valid}), "home_top_order": "custom_a", "home_main_order": "custom_a"}
	if len(Validate(values)) == 0 {
		t.Fatal("duplicate placement accepted")
	}
	values["home_main_order"] = "custom_missing"
	if len(Validate(values)) == 0 {
		t.Fatal("missing instance accepted")
	}
}
