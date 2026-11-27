package packagefs

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type aliasRecord struct {
	Schema int                    `json:"schema"`
	Key    string                 `json:"key"`
	Child  *aliasRecord           `json:"child"`
	Items  []aliasRecord          `json:"items"`
	Fixed  [1]*aliasRecord        `json:"fixed"`
	Lookup map[string]aliasRecord `json:"lookup"`
}

func TestDecodeJSONRejectsFieldAliases(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"ASCII case", `{"schema":1,"SCHEMA":2}`},
		{"reverse order", `{"SCHEMA":1,"schema":2}`},
		{"escaped case", `{"schema":1,"\u0053chema":2}`},
		{"Unicode long s", `{"schema":1,"ſchema":2}`},
		{"Unicode Kelvin", `{"key":"a","Key":"b"}`},
		{"nested pointer", `{"child":{"schema":1,"Schema":2}}`},
		{"slice member", `{"items":[{"schema":1,"Schema":2}]}`},
		{"array member", `{"fixed":[{"schema":1,"Schema":2}]}`},
		{"map value", `{"lookup":{"first":{"schema":1,"Schema":2}}}`},
		{"merge objects", `{"child":{"schema":1},"CHILD":{"key":"merged"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := DecodeJSON[aliasRecord](strings.NewReader(test.body), 4096, "alias test"); err == nil {
				t.Fatalf("ambiguous fields accepted: %#v", got)
			}
		})
	}
}

func assertCompatibleJSON[T any](t *testing.T, input string) {
	t.Helper()
	var want T
	if err := json.Unmarshal([]byte(input), &want); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeJSON[T](strings.NewReader(input), 4096, "compatibility test")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("valid JSON changed: got %#v, want %#v, error %v", got, want, err)
	}
}

type aliasEmbedded struct {
	Schema int `json:"schema"`
}
type aliasEmbeddedLeft struct{ aliasEmbedded }
type aliasEmbeddedRight struct{ aliasEmbedded }
type aliasCustom map[string]int

func (value *aliasCustom) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, (*map[string]int)(value))
}

func TestDecodeJSONPreservesDistinctFieldsAndKeys(t *testing.T) {
	for _, input := range []string{
		`{"SCHEMA":1,"Key":"value"}`,
		`{"items":[{"schema":1},{"SCHEMA":2}]}`,
		`{"lookup":{"first":{"schema":1},"FIRST":{"SCHEMA":2}}}`,
		`{"fixed":[{"schema":1},{"schema":2,"SCHEMA":3}]}`,
		`{"child":{"child":{"schema":3}}}`,
	} {
		t.Run(input, func(t *testing.T) { assertCompatibleJSON[aliasRecord](t, input) })
	}
	t.Run("string map keys", func(t *testing.T) {
		assertCompatibleJSON[map[string]int](t, `{"schema":1,"SCHEMA":2,"ſchema":3}`)
	})
	t.Run("interface object", func(t *testing.T) {
		assertCompatibleJSON[any](t, `{"schema":1,"SCHEMA":2}`)
	})
	t.Run("raw message", func(t *testing.T) {
		assertCompatibleJSON[json.RawMessage](t, `{"schema":1,"SCHEMA":2}`)
	})
	t.Run("custom decoder", func(t *testing.T) {
		assertCompatibleJSON[aliasCustom](t, `{"schema":1,"SCHEMA":2}`)
	})
	t.Run("distinct exact tags", func(t *testing.T) {
		assertCompatibleJSON[struct {
			Lower int `json:"schema"`
			Upper int `json:"SCHEMA"`
		}](t, `{"schema":1,"SCHEMA":2}`)
	})
	t.Run("first folded match", func(t *testing.T) {
		assertCompatibleJSON[struct {
			Lower int `json:"schema"`
			Upper int `json:"SCHEMA"`
		}](t, `{"Schema":1,"SCHEMA":2}`)
	})
	t.Run("embedding shadow", func(t *testing.T) {
		assertCompatibleJSON[struct {
			aliasEmbedded
			Schema string `json:"schema"`
		}](t, `{"Schema":"outer"}`)
	})
	t.Run("tagged embedding", func(t *testing.T) {
		assertCompatibleJSON[struct {
			aliasEmbedded `json:"embedded"`
			Schema        int `json:"schema"`
		}](t, `{"schema":1,"embedded":{"Schema":2}}`)
	})
	t.Run("tag overrides untagged", func(t *testing.T) {
		assertCompatibleJSON[struct {
			aliasEmbedded
			Other int `json:"Schema"`
		}](t, `{"schema":1,"Schema":2}`)
	})
	t.Run("duplicate embedding hidden", func(t *testing.T) {
		assertCompatibleJSON[struct {
			aliasEmbeddedLeft
			aliasEmbeddedRight
			Key string `json:"key"`
		}](t, `{"key":"ok"}`)
	})
}

func TestDecodeJSONRejectsEmbeddedFieldAliases(t *testing.T) {
	for _, input := range []string{`{"schema":1,"Schema":2}`, `{"schema":1,"ſchema":2}`} {
		if _, err := DecodeJSON[struct{ aliasEmbedded }](strings.NewReader(input), 4096, "embedded"); err == nil {
			t.Errorf("accepted embedded field aliases: %s", input)
		}
	}
	if _, err := DecodeJSON[struct {
		Lower int `json:"schema"`
		Upper int `json:"SCHEMA"`
	}](strings.NewReader(`{"schema":1,"Schema":2}`), 4096, "exact and folded"); err == nil {
		t.Error("accepted two names resolving to the first exact field")
	}
}

type aliasDeepLeft struct{ aliasEmbeddedLeft }
type aliasDeepRight struct{ aliasEmbeddedLeft }
type aliasPlain struct{ Schema int }
type aliasTagged struct {
	Value int `json:"Schema"`
}
type aliasRecursive struct {
	*aliasRecursive
	Schema int `json:"schema"`
}

// Determine whether names alias by observing encoding/json's actual writes,
// independently of the field lookup used by the strict decoder.
func checkJSONFieldSelection[T any](t *testing.T) {
	t.Helper()
	probe := func(body string) []byte {
		var target T
		if err := json.Unmarshal([]byte(body), &target); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(target)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	zero := probe(`{}`)
	keys := []string{"schema", "Schema", "SCHEMA", "ſchema"}
	for left := range keys {
		for right := left + 1; right < len(keys); right++ {
			a, b := keys[left], keys[right]
			first, second := probe(`{"`+a+`":1}`), probe(`{"`+b+`":1}`)
			if bytes.Equal(first, zero) || bytes.Equal(second, zero) {
				continue // Neither decoder maps an unknown key to a field.
			}
			t.Run(a+"+"+b, func(t *testing.T) {
				t.Parallel()
				body := `{"` + a + `":1,"` + b + `":2}`
				got, err := DecodeJSON[T](strings.NewReader(body), 4096, "field selection")
				if bytes.Equal(first, second) {
					if err == nil {
						t.Errorf("names writing the same destination were accepted: %s", body)
					}
					return
				}
				encoded, marshalErr := json.Marshal(got)
				if err != nil || marshalErr != nil || !bytes.Equal(encoded, probe(body)) {
					t.Errorf("distinct fields changed: %s, %v, %v", body, err, marshalErr)
				}
			})
		}
	}
}

func TestDecodeJSONMatchesStandardFieldSelection(t *testing.T) {
	t.Parallel()
	t.Run("nested shared embedding", func(t *testing.T) {
		checkJSONFieldSelection[struct {
			aliasDeepLeft
			aliasDeepRight
		}](t)
	})
	t.Run("tagged wins", func(t *testing.T) {
		checkJSONFieldSelection[struct {
			aliasPlain
			aliasTagged
		}](t)
	})
	t.Run("distinct exact tags", func(t *testing.T) {
		checkJSONFieldSelection[struct {
			First int `json:"schema"`
			Last  int `json:"SCHEMA"`
		}](t)
	})
	t.Run("recursive embedding", func(t *testing.T) {
		checkJSONFieldSelection[aliasRecursive](t)
	})
	t.Run("ignored tag and invalid tag", func(t *testing.T) {
		checkJSONFieldSelection[struct {
			Ignored int `json:"-"`
			Schema  int `json:"bad\\tag"`
		}](t)
	})
}
