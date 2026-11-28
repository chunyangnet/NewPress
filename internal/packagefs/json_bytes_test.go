package packagefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

type byteJSONDocument struct {
	ID   int64           `json:"id"`
	Text string          `json:"text"`
	Meta map[string]any  `json:"meta"`
	Map  map[int64]int64 `json:"map"`
	Raw  json.RawMessage `json:"raw"`
}

func TestDecodeJSONBytesMatchesReader(t *testing.T) {
	for _, body := range []string{
		`{"id":9007199254740993,"text":"你好😀","meta":{"integer":9007199254740993,"fraction":0.1234567890123456789,"huge":1e400},"map":{"1":2},"raw":{"value":[1,true,null]}}`,
		`null`, `{}`, ` {"id":1} `, `{"meta":null}`, `{"raw":null}`,
		`{"id":1,"ID":2}`, `{"id":1,"\u0069d":2}`, `{"unknown":1}`,
		`{"map":{"1":1,"01":2}}`, `{"raw":{"value":1,"value":2}}`,
		`{"meta":{"value":1,"value":2}}`, `{"id":9223372036854775808}`, `{"id":"one"}`,
		`{"id":1} {}`, `{"id":}`, `"\ud800"`, "{\"text\":\"\xff\"}", ``, ` `,
		strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001),
	} {
		for _, limit := range []int64{1, int64(len(body)), int64(len(body) + 1)} {
			if limit == 0 {
				limit = 1
			}
			payload := []byte(body)
			before := bytes.Clone(payload)
			want, wantErr := DecodeJSONWithNumbers[byteJSONDocument](bytes.NewReader(payload), limit, "bytes")
			got, gotErr := DecodeJSONBytesWithNumbers[byteJSONDocument](payload, limit, "bytes")
			if !reflect.DeepEqual(got, want) || (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("byte and reader results differ, size=%d limit=%d: %v / %v", len(body), limit, wantErr, gotErr)
			}
			if wantErr != nil && wantErr.Error() != gotErr.Error() {
				t.Fatalf("error changed: %v / %v", wantErr, gotErr)
			}
			var wantSyntax, gotSyntax *json.SyntaxError
			if errors.As(wantErr, &wantSyntax) != errors.As(gotErr, &gotSyntax) {
				t.Fatal("syntax error type changed")
			}
			if !bytes.Equal(payload, before) {
				t.Fatal("decoder changed input bytes")
			}
		}
	}
}

func TestDecodeJSONBytesLimitsAndIsolation(t *testing.T) {
	for _, limit := range []int64{0, -1, math.MaxInt64} {
		if _, err := DecodeJSONBytesWithNumbers[any]([]byte(`null`), limit, "invalid"); err == nil {
			t.Fatal("invalid limit accepted", limit)
		}
	}
	payload := []byte(`{"id":9007199254740993,"text":"original","meta":{"large":9007199254740993},"raw":{"inside":"original"}}`)
	got, err := DecodeJSONBytesWithNumbers[byteJSONDocument](payload, int64(len(payload)), "exact")
	if err != nil || got.ID != 9007199254740993 || got.Meta["large"] != json.Number("9007199254740993") {
		t.Fatal("exact metadata changed", got, err)
	}
	clear(payload)
	if got.Text != "original" || string(got.Raw) != `{"inside":"original"}` || got.Meta["large"] != json.Number("9007199254740993") {
		t.Fatal("result borrowed caller input")
	}
	boundary := []byte(strings.Repeat("[", 10000) + "0" + strings.Repeat("]", 10000))
	decoded, err := DecodeJSONBytesWithNumbers[json.RawMessage](boundary, int64(len(boundary)), "depth")
	if err != nil || !bytes.Equal(decoded, boundary) {
		t.Fatal("valid depth boundary rejected", err)
	}
}

type borrowingJSONBytes []byte

func (b *borrowingJSONBytes) UnmarshalJSON(data []byte) error {
	*b = data // Deliberately retain the decoder buffer to check caller isolation.
	return nil
}

func TestDecodeJSONBytesCustomDecoderDoesNotBorrowCaller(t *testing.T) {
	payload := []byte(`{"value":42}`)
	got, err := DecodeJSONBytesWithNumbers[borrowingJSONBytes](payload, int64(len(payload)), "custom")
	if err != nil {
		t.Fatal(err)
	}
	clear(payload)
	if string(got) != `{"value":42}` {
		t.Fatal("custom decoder borrowed the original input")
	}
}
