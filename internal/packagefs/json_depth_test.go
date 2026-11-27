package packagefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestUniqueJSONRejectsNestingBeyondDecoderLimit(t *testing.T) {
	for _, shape := range []struct{ name, open, close string }{
		{"arrays", "[", "]"},
		{"objects", `{"next":`, "}"},
		{"mixed", `[{"next":`, "}]"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			body := []byte(strings.Repeat(shape.open, 10001) + "0" + strings.Repeat(shape.close, 10001))
			if json.Valid(body) {
				t.Fatal("fixture does not exceed the standard decoder limit")
			}
			for _, validate := range []struct {
				name string
				run  func() error
			}{
				{"untyped", func() error { return ValidateUniqueJSON(body) }},
				{"raw destination", func() error { return ValidateUniqueJSONFor(body, new(json.RawMessage)) }},
				{"generic destination", func() error { return ValidateUniqueJSONFor(body, new(any)) }},
			} {
				t.Run(validate.name, func(t *testing.T) {
					err := validate.run()
					var syntax *json.SyntaxError
					if !errors.As(err, &syntax) || !strings.Contains(err.Error(), "exceeded max depth") {
						t.Fatalf("over-nested document did not produce the decoder's syntax error: %v", err)
					}
				})
			}
		})
	}
}

func TestUniqueJSONAcceptsDecoderNestingBoundary(t *testing.T) {
	for _, shape := range []struct{ name, open, close string }{
		{"arrays", "[", "]"},
		{"objects", `{"next":`, "}"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			body := []byte(strings.Repeat(shape.open, 10000) + "0" + strings.Repeat(shape.close, 10000))
			if !json.Valid(body) {
				t.Fatal("fixture is not a readable boundary document")
			}
			if err := ValidateUniqueJSON(body); err != nil {
				t.Fatal("valid boundary was rejected", err)
			}
			decoded, err := DecodeJSON[json.RawMessage](bytes.NewReader(body), int64(len(body)), "boundary")
			if err != nil || !bytes.Equal(decoded, body) {
				t.Fatal("boundary raw document did not decode exactly", err)
			}
		})
	}

	// Syntax preflight must not replace the stricter duplicate/Unicode checks.
	for _, value := range []string{`{"same":1,"same":2}`, `"\ud800"`} {
		body := []byte(strings.Repeat("[", 9999) + value + strings.Repeat("]", 9999))
		if !json.Valid(body) {
			t.Fatal("fixture must be syntactically valid")
		}
		if err := ValidateUniqueJSON(body); err == nil {
			t.Fatal("deep duplicate key or malformed Unicode was accepted")
		}
	}
}
