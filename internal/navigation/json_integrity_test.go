package navigation

import (
	"testing"
)

func TestItemJSONRejectsAliasesAndLossyUnicode(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"case alias", `[{"label":"first","LABEL":"second","url":"/"}]`},
		{"Unicode alias", `[{"label":"safe","presentation":"standard","preſentation":"dropdown","url":"/"}]`},
		{"nested badge alias", `[{"label":"safe","url":"/","badge":{"text":"one","TEXT":"two"}}]`},
		{"invalid UTF8", "[{\"label\":\"private-\xff\",\"url\":\"/\"}]"},
		{"high surrogate", `[{"label":"private-\ud800","url":"/"}]`},
		{"low surrogate", `[{"label":"private-\udfff","url":"/"}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := DecodeItemInputs(test.body); err == nil || got != nil {
				t.Errorf("ambiguous or lossy navigation input decoded: %#v %v", got, err)
			}
		})
	}
}

func TestItemJSONKeepsUnicodeAndLegacyAliases(t *testing.T) {
	for _, test := range []struct{ name, body, want string }{
		{"paired emoji", `[{"label":"导航\ud83d\ude00","url":"/"}]`, "导航😀"},
		{"single field alias", `[{"LABEL":"导航","url":"/"}]`, "导航"},
		{"literal surrogate text", `[{"label":"\\ud800","url":"/"}]`, `\ud800`},
		{"replacement character", `[{"label":"�\ufffd","url":"/"}]`, "��"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := DecodeItemInputs(test.body)
			if err != nil || len(got) != 1 || got[0].Label != test.want {
				t.Fatalf("valid navigation input changed: %#v %v", got, err)
			}
		})
	}
}
