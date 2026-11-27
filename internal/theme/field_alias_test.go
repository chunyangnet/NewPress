package theme

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestThemeJSONRejectsStructAliasesPreservingSettingKeys(t *testing.T) {
	for _, strict := range []bool{true, false} {
		name := "known fields"
		if !strict {
			name = "unknown fields allowed"
		}
		t.Run(name, func(t *testing.T) {
			var target struct {
				Schema int `json:"schema"`
			}
			if err := decodeJSONDocument(strings.NewReader(`{"schema":0,"SCHEMA":1}`), 4096, &target, strict); err == nil {
				t.Error("ambiguous theme document accepted")
			}
		})
	}
	t.Run("case-sensitive settings", func(t *testing.T) {
		var target struct {
			Values map[string]any `json:"values"`
		}
		if err := decodeJSONDocument(strings.NewReader(`{"values":{"Header":1,"header":2}}`), 4096, &target, true); err != nil {
			t.Fatal(err)
		}
		if target.Values["Header"] != json.Number("1") || target.Values["header"] != json.Number("2") {
			t.Fatalf("distinct setting keys or number precision changed: %#v", target.Values)
		}
	})
}
