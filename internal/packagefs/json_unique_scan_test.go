package packagefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// Preserve the pre-optimization token walk as an independent traversal oracle.
// Field resolution and Unicode policy are unchanged by the scanner optimization.
func referenceUniqueJSON(payload []byte, target any) error {
	if err := validateJSONUnicode(payload); err != nil {
		return err
	}
	if !json.Valid(payload) {
		var raw json.RawMessage
		return json.Unmarshal(payload, &raw)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := referenceUniqueJSONValue(decoder, reflect.TypeOf(target)); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing JSON token %v", token)
	}
	return nil
}

func referenceUniqueJSONValue(decoder *json.Decoder, target reflect.Type) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	target = jsonDestinationType(target)
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		fields := make(map[string]string)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key must be a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			child, field := jsonObjectField(target, key)
			if field != "" {
				if previous, exists := fields[field]; exists {
					return fmt.Errorf("duplicate JSON field %q (alias of %q)", key, previous)
				}
				fields[field] = key
			}
			if err := referenceUniqueJSONValue(decoder, child); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		index := 0
		for decoder.More() {
			var child reflect.Type
			if target != nil && (target.Kind() == reflect.Slice || target.Kind() == reflect.Array && index < target.Len()) {
				child = target.Elem()
			}
			if err := referenceUniqueJSONValue(decoder, child); err != nil {
				return err
			}
			index++
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

type scanDocument struct {
	ID    int64                  `json:"id"`
	Key   string                 `json:"key"`
	Items []scanDocument         `json:"items"`
	Fixed [1]*scanDocument       `json:"fixed"`
	Child *scanDocument          `json:"child"`
	Map   map[int64]scanDocument `json:"map"`
	Meta  any                    `json:"meta"`
	Raw   json.RawMessage        `json:"raw"`
}

func checkUniqueJSONScan(t *testing.T, payload []byte) {
	t.Helper()
	before := bytes.Clone(payload)
	for _, target := range []any{nil, new(scanDocument), new(map[int64]any), new([1]scanDocument), new(json.RawMessage)} {
		want := referenceUniqueJSON(payload, target)
		got := ValidateUniqueJSONFor(payload, target)
		if (want == nil) != (got == nil) {
			t.Fatalf("validation changed for %T: reference=%v, actual=%v, payload=%q", target, want, got, payload)
		}
		var wantSyntax, gotSyntax *json.SyntaxError
		if errors.As(want, &wantSyntax) != errors.As(got, &gotSyntax) {
			t.Fatalf("syntax error type changed: reference=%v, actual=%v", want, got)
		}
	}
	if !bytes.Equal(before, payload) {
		t.Fatal("validation mutated the input")
	}
}

func TestUniqueJSONScanReference(t *testing.T) {
	for _, body := range []string{
		`null`, `true`, `false`, `1e400`, `-0.123e-45`, `"text with \\\" quotes"`, `"你好😀"`,
		`{"id":1,"ID":2}`, `{"key":"k","Key":"alias"}`, `{"id":1,"\u0069d":2}`,
		`{"meta":{"a":1,"a":2}}`, `{"raw":{"a":1,"\u0061":2}}`,
		`{"items":[{"id":1},{"ID":2}]}`, `{"items":[{"id":1,"ID":2}]}`,
		`{"map":{"1":{"id":1},"01":{"id":2}}}`, `{"1":1,"+1":2}`,
		`{"fixed":[{"id":1},{"id":2,"ID":3}]}`, `[{"id":1},{"id":2,"ID":3}]`,
		`{"key":"[\\\"}]:,","meta":[null,true,false,123,-1.2e+30,"\\uD800"]}`,
		`{"\u0000":1,"\u0000":2}`, `{"\ud83d\ude00":1,"😀":2}`,
		`{"id":1} {"id":2}`, `{"id":}`, `[1,]`, `"\ud800"`, `"\udc00"`, `"\u12xz"`,
		"{\"key\":\"\xff\"}", "\t\r\n { \"id\" : 42 , \"meta\" : [ 1 , { \"x\" : true } ] } \r\n",
	} {
		t.Run(fmt.Sprint(len(body), "/", body), func(t *testing.T) { checkUniqueJSONScan(t, []byte(body)) })
	}
	for _, duplicate := range []bool{false, true} {
		var body strings.Builder
		body.WriteByte('{')
		for i := range 300 {
			if i > 0 {
				body.WriteByte(',')
			}
			fmt.Fprintf(&body, `"key-%d":{"n":%d,"text":"escaped\\\"value"}`, i, i)
		}
		if duplicate {
			body.WriteString(`,"key-299":null`)
		}
		body.WriteByte('}')
		checkUniqueJSONScan(t, []byte(body.String()))
	}
}

func FuzzUniqueJSONScan(f *testing.F) {
	for _, body := range []string{
		`{"id":1,"ID":2}`, `{"map":{"1":{"id":1},"01":{"ID":2}}}`,
		`{"items":[{"key":"escaped\\\""},{"key":"你好😀"}],"meta":[1e400,null,true]}`,
		`{"raw":{"\ud83d\ude00":1,"😀":2}}`, `{"fixed":[{"id":1},{"id":2,"ID":3}]}`,
		`[]`, `"\ud800"`, `"\u0000"`, `[{}]`, `null`, `{"":""}`,
	} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 65536 {
			t.Skip("bounded differential input")
		}
		checkUniqueJSONScan(t, payload)
	})
}
