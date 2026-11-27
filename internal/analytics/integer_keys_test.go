package analytics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyticsRejectsDestinationAliases(t *testing.T) {
	base := []byte(`{"schema":1,"days":{"2026-11-21":{"date":"2026-11-21","views":5,"searches":0,"misses":0,"posts":{"1":4},"sources":{"direct":5},"terms":{}}},"writers":{}}`)
	if _, err := Decode(base); err != nil {
		t.Fatalf("invalid base fixture: %v", err)
	}
	for _, test := range []struct{ name, from, to string }{
		{"leading zero post", `"posts":{"1":4}`, `"posts":{"1":4,"01":2}`},
		{"positive sign post", `"posts":{"1":4}`, `"posts":{"1":4,"+1":2}`},
		{"negative zero other bucket", `"posts":{"1":4}`, `"posts":{"0":4,"-0":2}`},
		{"schema field alias", `"schema":1`, `"schema":0,"SCHEMA":1`},
		{"nested views alias", `"views":5`, `"views":0,"VIEWS":5`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := bytes.Replace(base, []byte(test.from), []byte(test.to), 1)
			if bytes.Equal(body, base) {
				t.Fatal("fixture did not inject alias")
			}
			if state, err := Decode(body); err == nil {
				t.Errorf("aliased analytics accepted: %#v", state)
			}
			filename := filepath.Join(t.TempDir(), "analytics.json")
			if err := os.WriteFile(filename, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if store, err := NewFileStore(filename); err == nil || store != nil {
				t.Error("ambiguous analytics file reopened")
			}
			if after, err := os.ReadFile(filename); err != nil || !bytes.Equal(after, body) {
				t.Errorf("rejection changed analytics file: %v", err)
			}
		})
	}
}

func TestAnalyticsPreservesIntegerKeyCompatibility(t *testing.T) {
	for _, post := range []string{`"01":2,"+2":1`, `"0":1,"1":2`, `"9223372036854775807":3`} {
		t.Run(post, func(t *testing.T) {
			body := []byte(`{"schema":1,"days":{"2026-11-21":{"date":"2026-11-21","views":5,"posts":{` + post + `},"sources":{"direct":5},"terms":{}}},"writers":{}}`)
			var want State
			if err := json.Unmarshal(body, &want); err != nil || Validate(want) != nil {
				t.Fatalf("invalid fixture: %v", err)
			}
			got, err := Decode(body)
			if err != nil || len(got.Days["2026-11-21"].Posts) != len(want.Days["2026-11-21"].Posts) {
				t.Fatalf("valid integer keys rejected: %v", err)
			}
			for key, count := range want.Days["2026-11-21"].Posts {
				if got.Days["2026-11-21"].Posts[key] != count {
					t.Errorf("post %d count changed", key)
				}
			}
		})
	}
	t.Run("depth bound", func(t *testing.T) {
		if _, err := Decode([]byte(strings.Repeat(`[`, 10) + `0` + strings.Repeat(`]`, 10))); err == nil {
			t.Error("excessive nesting accepted")
		}
	})
}
