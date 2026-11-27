package content

import (
	"encoding/json"
	"testing"
)

type snapshotTextMarshaler string

func (value snapshotTextMarshaler) MarshalText() ([]byte, error) { return []byte(value), nil }

type snapshotJSONMarshaler string

func (value snapshotJSONMarshaler) MarshalJSON() ([]byte, error) { return []byte(value), nil }

type snapshotEmbedded struct{ Value string }
type snapshotStruct struct{ snapshotEmbedded }
type snapshotDash struct {
	Value string `json:"-,omitempty"`
}
type snapshotHidden struct {
	Value   string `json:"-"`
	Visible string
}
type snapshotShadowed struct {
	snapshotEmbedded
	Value string
}
type snapshotTextKey int

func (snapshotTextKey) MarshalText() ([]byte, error) { return []byte("\xff"), nil }

func TestSnapshotMetadataUsesActualJSONEncoding(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		valid bool
	}{
		{"text marshaler valid", snapshotTextMarshaler("合法😀�"), true},
		{"text marshaler invalid", snapshotTextMarshaler("\xff"), false},
		{"JSON marshaler valid", snapshotJSONMarshaler(`{"title":"合法😀"}`), true},
		{"JSON marshaler surrogate", snapshotJSONMarshaler(`"\ud800"`), false},
		{"text map key invalid", map[snapshotTextKey]string{1: "value"}, false},
		{"literal string map key invalid", map[snapshotTextMarshaler]string{"\xff": "value"}, false},
		{"unexported embedded exported text", snapshotStruct{snapshotEmbedded{"\xff"}}, false},
		{"literal dash field name", snapshotDash{"\xff"}, false},
		{"ignored field", snapshotHidden{"\xff", "合法"}, true},
		{"shadowed field", snapshotShadowed{snapshotEmbedded{"\xff"}, "合法"}, true},
		{"binary bytes", []byte{0xff, 0x80}, true},
		{"named string", snapshotTextAlias("\xff"), false},
		{"nil custom pointer", (*snapshotJSONMarshaler)(nil), true},
		{"raw valid JSON", json.RawMessage(`{"value":42}`), true},
		{"raw duplicate key", json.RawMessage(`{"value":1,"value":2}`), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validSnapshotJSON(Post{Meta: map[string]any{"plugin": test.value}}); got != test.valid {
				t.Fatalf("snapshot validity = %v, want %v", got, test.valid)
			}
		})
	}
}
