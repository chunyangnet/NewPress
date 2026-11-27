package packagefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type snapshotEncodingOnce struct{ calls int }

func (v *snapshotEncodingOnce) MarshalJSON() ([]byte, error) {
	v.calls++
	if v.calls != 1 {
		return nil, errors.New("encoding invoked more than once")
	}
	return []byte(`{"number":9007199254740993}`), nil
}

func TestMarshalUniqueJSONReturnsTheCheckedEncoding(t *testing.T) {
	value := &snapshotEncodingOnce{}
	payload, err := MarshalUniqueJSON(value)
	if err != nil || string(payload) != `{"number":9007199254740993}` || value.calls != 1 {
		t.Fatalf("checked bytes were not reused: %s, calls=%d, err=%v", payload, value.calls, err)
	}
	for _, value := range []any{"\xff", json.RawMessage(`{"same":1,"same":2}`), json.RawMessage(`"\ud800"`)} {
		if payload, err := MarshalUniqueJSON(value); err == nil || payload != nil {
			t.Fatalf("invalid encoding was returned to the caller: %q, %v", payload, err)
		}
	}
}

func TestDecodeJSONWithNumbersIsExactAndOptIn(t *testing.T) {
	type document struct {
		ID   int64          `json:"id"`
		Meta map[string]any `json:"meta"`
	}
	const payload = `{"id":9007199254740993,"meta":{"integer":9007199254740993,"fraction":0.1234567890123456789,"nested":[1e400,1e-400]}}`
	got, err := DecodeJSONWithNumbers[document](strings.NewReader(payload), 1024, "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	want := document{ID: 9007199254740993, Meta: map[string]any{"integer": json.Number("9007199254740993"), "fraction": json.Number("0.1234567890123456789"), "nested": []any{json.Number("1e400"), json.Number("1e-400")}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("precision changed: %#v", got)
	}
	ordinary, err := DecodeJSON[map[string]any](strings.NewReader(`{"number":42}`), 128, "ordinary")
	if err != nil || ordinary["number"] != float64(42) {
		t.Fatal("the existing decoder changed default number semantics", ordinary, err)
	}
}

func TestDecodeJSONWithNumbersKeepsStrictDocumentChecks(t *testing.T) {
	type document struct {
		ID   int64          `json:"id"`
		Meta map[string]any `json:"meta"`
	}
	for _, test := range []struct{ name, payload string }{
		{"duplicate", `{"id":1,"id":2}`},
		{"field alias", `{"id":1,"ID":2}`},
		{"unknown", `{"missing":1}`},
		{"trailing", `{"id":1} {"id":2}`},
		{"bad UTF8", "{\"meta\":{\"text\":\"\xff\"}}"},
		{"surrogate", `{"meta":{"text":"\ud800"}}`},
		{"nested duplicate", `{"meta":{"number":1,"number":2}}`},
		{"typed overflow", `{"id":9223372036854775808}`},
		{"empty", ``},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeJSONWithNumbers[document](strings.NewReader(test.payload), 1024, "snapshot"); err == nil {
				t.Fatal("invalid JSON document accepted")
			}
		})
	}
	t.Run("size", func(t *testing.T) {
		if _, err := DecodeJSONWithNumbers[document](bytes.NewReader([]byte(`{"id":1}`)), 2, "snapshot"); err == nil {
			t.Fatal("document size guard was bypassed")
		}
	})
}
