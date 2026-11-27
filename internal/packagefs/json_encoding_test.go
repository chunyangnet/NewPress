package packagefs

import (
	"encoding/json"
	"math"
	"testing"
)

type encodingAlias string
type encodingPointerText struct{ Text string }

func (value *encodingPointerText) MarshalText() ([]byte, error) { return []byte(value.Text), nil }

type encodingPointerJSON struct{ Ignored string }

func (*encodingPointerJSON) MarshalJSON() ([]byte, error) { return []byte(`"合法😀"`), nil }

type encodingZeroText string

func (encodingZeroText) IsZero() bool { return true }

type encodingKey int

func (encodingKey) MarshalText() ([]byte, error) { return []byte("same"), nil }

type encodingEmbedded struct{ Text string }
type encodingOuter struct {
	encodingEmbedded
	Text string
}

func TestJSONEncodingValidatesOnlySerializedText(t *testing.T) {
	valid := &encodingPointerText{Text: "合法�😀"}
	invalid := &encodingPointerText{Text: "\xff"}
	for _, test := range []struct {
		name  string
		value any
		valid bool
	}{
		{"named invalid string", encodingAlias("\xff"), false},
		{"nil", nil, true},
		{"nil pointer", (*encodingPointerText)(nil), true},
		{"binary bytes", []byte{0xff, 0x80}, true},
		{"integer array", [2]byte{0xff, 0x80}, true},
		{"pointer text valid", valid, true},
		{"pointer text invalid", invalid, false},
		{"addressable slice text", []encodingPointerText{{"\xff"}}, false},
		{"unaddressable map text", map[string]encodingPointerText{"a": {"\xff"}}, false},
		{"custom pointer JSON", &encodingPointerJSON{"\xff"}, true},
		{"addressable slice JSON", []encodingPointerJSON{{"\xff"}}, true},
		{"unaddressable map JSON", map[string]encodingPointerJSON{"a": {"\xff"}}, false},
		{"omitzero invalid hidden", struct {
			Text encodingZeroText `json:"text,omitzero"`
		}{"\xff"}, true},
		{"shadowed invalid hidden", encodingOuter{encodingEmbedded{"\xff"}, "合法"}, true},
		{"nested map integer key", map[int]any{1: map[string]any{"text": "\xff"}}, false},
		{"custom keys collide", map[encodingKey]string{1: "a", 2: "b"}, false},
		{"raw invalid UTF8", json.RawMessage("\"\xff\""), false},
		{"raw unpaired surrogate", json.RawMessage(`"\ud800"`), false},
		{"raw paired surrogate", json.RawMessage(`"\ud83d\ude00"`), true},
		{"raw large number", json.RawMessage(`9007199254740993`), true},
		{"literal replacement", "�", true},
		{"nan", math.NaN(), false},
		{"unsupported channel", make(chan int), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateJSONEncoding(test.value); (err == nil) != test.valid {
				t.Fatalf("encoding validity: %v, want valid=%v", err, test.valid)
			}
		})
	}
}

func TestJSONEncodingRejectsCyclesAndAllowsSharedValues(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		cycle := map[string]any{}
		cycle["self"] = cycle
		if ValidateJSONEncoding(cycle) == nil {
			t.Fatal("cyclic value accepted")
		}
	})
	t.Run("shared", func(t *testing.T) {
		shared := map[string]any{"text": "合法😀"}
		if err := ValidateJSONEncoding([]any{shared, shared}); err != nil {
			t.Fatal(err)
		}
	})
}
