package packagefs

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestJSONUnicodeRejectsLossyStrings(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"invalid value byte", "{\"text\":\"private-\xff\"}"},
		{"invalid key byte", "{\"private-\xff\":1}"},
		{"truncated UTF8", "{\"text\":\"\xe4\xb8\"}"},
		{"overlong UTF8", "{\"text\":\"\xc0\xaf\"}"},
		{"UTF8 surrogate", "{\"text\":\"\xed\xa0\x80\"}"},
		{"UTF8 beyond Unicode", "{\"text\":\"\xf4\x90\x80\x80\"}"},
		{"isolated high surrogate", `{"text":"private-\ud800"}`},
		{"last high surrogate", `{"text":"\uDBFF"}`},
		{"isolated low surrogate", `{"text":"\udc00"}`},
		{"last low surrogate", `{"text":"\uDFFF"}`},
		{"high then high", `{"text":"\ud800\udbff"}`},
		{"high then BMP", `{"text":"\ud800\u0041"}`},
		{"high then literal", `{"text":"\ud800x"}`},
		{"high then escaped slash", `{"text":"\ud800\\udc00"}`},
		{"reversed pair", `{"text":"\udc00\ud800"}`},
		{"extra low surrogate", `{"text":"\ud800\udc00\udfff"}`},
		{"high surrogate key", `{"\ud800":1}`},
		{"low surrogate key", `{"\udc00":1}`},
		{"nested array", `{"raw":{"list":[null,{"text":"\ud800"}]}}`},
		{"pair split across strings", `{"raw":["\ud800","\udc00"]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(test.body)
			before := bytes.Clone(body)
			for _, entry := range []struct {
				name  string
				check func() error
			}{
				{"untyped validation", func() error { return ValidateUniqueJSON(body) }},
				{"typed raw validation", func() error { return ValidateUniqueJSONFor(body, new(map[string]json.RawMessage)) }},
				{"decode before mutation", func() error {
					target := map[string]any{"sentinel": "unchanged"}
					err := DecodeJSONInto(bytes.NewReader(body), 4096, "Unicode fixture", &target)
					if !reflect.DeepEqual(target, map[string]any{"sentinel": "unchanged"}) {
						t.Error("lossy decoding modified the existing destination")
					}
					return err
				}},
			} {
				t.Run(entry.name, func(t *testing.T) {
					if err := entry.check(); err == nil {
						t.Error("accepted JSON requiring Unicode replacement")
					} else if strings.Contains(err.Error(), "private-") {
						t.Error("Unicode error disclosed document contents")
					}
					if !bytes.Equal(body, before) {
						t.Error("validation changed input bytes")
					}
				})
			}
		})
	}
}

func TestJSONUnicodePreservesValidStrings(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"Chinese and emoji", `{"text":"中文😀"}`},
		{"escaped Chinese", `{"text":"\u4e2d\u6587"}`},
		{"surrogate pair", `{"text":"\uD83D\ude00"}`},
		{"first supplementary character", `{"text":"\ud800\udc00"}`},
		{"last supplementary character", `{"text":"\udbff\udfff"}`},
		{"adjacent pairs", `{"text":"\ud800\udfff\udbff\udc00"}`},
		{"BMP boundaries", `{"text":"\ud7ff\ue000\uffff"}`},
		{"intentional replacement character", `{"text":"�\ufffd"}`},
		{"literal surrogate spelling", `{"text":"\\ud800"}`},
		{"literal low spelling", `{"text":"\\udfff"}`},
		{"escaped slash before pair", `{"text":"\\\ud83d\ude00"}`},
		{"escaped quote before pair", `{"text":"\"\ud83d\ude00"}`},
		{"escaped quote after pair", `{"text":"\ud83d\ude00\""}`},
		{"standard escapes", `{"text":"\"\\\/\b\f\n\r\t\u0000"}`},
		{"Unicode object key", `{"\ud83d\ude00":"中"}`},
		{"nested raw and arrays", `{"raw":[{"text":"\ud83d\ude00"},"\\ud800",null]}`},
		{"scalar", `"\ud83d\ude00"`},
		{"non string values", ` [0, true, false, null, {}, []] `},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(test.body)
			var expected any
			if err := json.Unmarshal(body, &expected); err != nil {
				t.Fatal(err)
			}
			if err := ValidateUniqueJSON(body); err != nil {
				t.Fatal(err)
			}
			got, err := DecodeJSON[any](bytes.NewReader(body), 4096, "Unicode fixture")
			if err != nil || !reflect.DeepEqual(got, expected) {
				t.Fatalf("valid Unicode changed: got %#v, want %#v, error %v", got, expected, err)
			}
			var raw json.RawMessage
			if err := DecodeJSONInto(bytes.NewReader(body), 4096, "raw fixture", &raw); err != nil || !bytes.Equal(raw, bytes.TrimSpace(body)) {
				t.Fatalf("raw JSON changed: %v", err)
			}
		})
	}
}

func TestJSONUnicodeKeepsSyntaxValidation(t *testing.T) {
	for _, body := range []string{`{"text":"\u"}`, `{"text":"\u12"}`, `{"text":"\uZZZZ"}`, `{"text":"\q"}`, `{"text":"trailing\`, `{"text":"unterminated}`, `{"text":"\ud800\udc"}`, `{} {}`, `{"text":"ok","text":"again"}`} {
		t.Run(body, func(t *testing.T) {
			if err := ValidateUniqueJSON([]byte(body)); err == nil {
				t.Error("malformed or duplicate JSON accepted")
			}
		})
	}
}
