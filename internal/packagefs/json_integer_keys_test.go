package packagefs

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type integerAliasID int64
type integerTextID int

func (id *integerTextID) UnmarshalText(text []byte) error {
	*id = integerTextID(len(text))
	return nil
}

func TestDecodeJSONRejectsIntegerMapKeyAliases(t *testing.T) {
	for _, test := range []struct {
		name, left, right string
		target            any
	}{
		{"int leading zero", "1", "01", new(map[int]int)},
		{"int8 plus", "1", "+1", new(map[int8]int)},
		{"int16 negative", "-1", "-01", new(map[int16]int)},
		{"int32 negative zero", "0", "-0", new(map[int32]int)},
		{"int64 minimum", "-9223372036854775808", "-09223372036854775808", new(map[int64]int)},
		{"int64 maximum", "9223372036854775807", "+9223372036854775807", new(map[int64]int)},
		{"uint leading zero", "1", "01", new(map[uint]int)},
		{"uint8 maximum", "255", "0255", new(map[uint8]int)},
		{"uint16 maximum", "65535", "065535", new(map[uint16]int)},
		{"uint32 maximum", "4294967295", "04294967295", new(map[uint32]int)},
		{"uint64 maximum", "18446744073709551615", "018446744073709551615", new(map[uint64]int)},
		{"uintptr zero", "0", "00", new(map[uintptr]int)},
		{"named integer", "7", "007", new(map[integerAliasID]int)},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{%q:1,%q:2}`, test.left, test.right)
			// Confirm this input aliases under the decoder actually in use.
			probe := reflect.New(reflect.TypeOf(test.target).Elem())
			if err := json.Unmarshal([]byte(body), probe.Interface()); err != nil || probe.Elem().Len() != 1 {
				t.Fatalf("fixture does not alias: %s, %v", body, err)
			}
			if err := DecodeJSONInto(strings.NewReader(body), 4096, "integer map", test.target); err == nil {
				t.Error("ambiguous integer keys accepted")
			}
			if !reflect.ValueOf(test.target).Elem().IsNil() {
				t.Error("rejected document modified destination")
			}
		})
	}
}

type integerKeyDocument struct {
	Label string                   `json:"label"`
	Items []map[int64]int          `json:"items"`
	Fixed [1]map[int64]int         `json:"fixed"`
	Map   map[string]map[int64]int `json:"map"`
	Ptr   *map[int64]int           `json:"ptr"`
}

func TestDecodeJSONRejectsNestedIntegerKeyAliasesBeforeMutation(t *testing.T) {
	for _, body := range []string{
		`{"label":"changed","items":[{"1":1,"01":2}]}`,
		`{"label":"changed","fixed":[{"1":1,"+1":2}]}`,
		`{"label":"changed","map":{"bucket":{"0":1,"-0":2}}}`,
		`{"label":"changed","ptr":{"-1":1,"-01":2}}`,
	} {
		t.Run(body, func(t *testing.T) {
			target := integerKeyDocument{Label: "original"}
			if err := DecodeJSONInto(strings.NewReader(body), 4096, "nested integer map", &target); err == nil {
				t.Error("nested integer aliases accepted")
			}
			if !reflect.DeepEqual(target, integerKeyDocument{Label: "original"}) {
				t.Errorf("invalid document changed existing destination: %#v", target)
			}
		})
	}
}

func TestDecodeJSONIntegerKeyCompatibility(t *testing.T) {
	t.Run("signed distinct values", func(t *testing.T) {
		assertCompatibleJSON[map[int64]int](t, `{"01":1,"+2":2,"-03":3,"-0":4}`)
	})
	t.Run("unsigned distinct values", func(t *testing.T) {
		assertCompatibleJSON[map[uint64]int](t, `{"01":1,"02":2,"018446744073709551615":3}`)
	})
	t.Run("integer limits", func(t *testing.T) {
		assertCompatibleJSON[map[int64]int](t, `{"-9223372036854775808":1,"9223372036854775807":2}`)
	})
	t.Run("string keys", func(t *testing.T) {
		assertCompatibleJSON[map[string]int](t, `{"1":1,"01":2,"+1":3,"-0":4,"0":5}`)
	})
	t.Run("custom text keys", func(t *testing.T) {
		assertCompatibleJSON[map[integerTextID]int](t, `{"1":1,"01":2}`)
	})
	t.Run("dynamic object", func(t *testing.T) {
		assertCompatibleJSON[any](t, `{"1":1,"01":2}`)
	})
	t.Run("raw document", func(t *testing.T) {
		assertCompatibleJSON[json.RawMessage](t, `{"1":1,"01":2}`)
	})
	t.Run("separate map scopes", func(t *testing.T) {
		assertCompatibleJSON[integerKeyDocument](t, `{"items":[{"1":1},{"01":2}]}`)
	})
	t.Run("discarded array member", func(t *testing.T) {
		assertCompatibleJSON[integerKeyDocument](t, `{"fixed":[{"1":1},{"1":1,"01":2}]}`)
	})
	for _, test := range []struct {
		name, body string
		target     any
	}{
		{"int8 overflow", `{"128":1}`, new(map[int8]int)},
		{"uint8 overflow", `{"256":1}`, new(map[uint8]int)},
		{"int64 overflow", `{"9223372036854775808":1}`, new(map[int64]int)},
		{"uint64 overflow", `{"18446744073709551616":1}`, new(map[uint64]int)},
		{"unsigned negative", `{"-1":1}`, new(map[uint64]int)},
		{"decimal point", `{"1.0":1}`, new(map[int64]int)},
		{"exponent", `{"1e0":1}`, new(map[int64]int)},
		{"hexadecimal", `{"0x1":1}`, new(map[int64]int)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := DecodeJSONInto(strings.NewReader(test.body), 4096, "invalid integer", test.target); err == nil {
				t.Error("invalid integer key accepted")
			}
		})
	}
}
