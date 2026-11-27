package articlewidgets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWidgetStorageRejectsAmbiguousOrLossyJSON(t *testing.T) {
	dir := t.TempDir()
	c := example("poll")
	s := NewStore(dir)
	if _, err := s.Vote(t.Context(), 1, 1, c, "u:2", 1); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, key(1, 1, c)+".json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, old, replacement string }{
		{"null document", string(original), " \nnull\t"},
		{"empty document", string(original), "{}"},
		{"missing votes", `"votes":{"u:2":1},`, ""},
		{"duplicate version", `"version":1`, `"version":2,"version":1`},
		{"aliased version", `"version":1`, `"VERSION":2,"version":1`},
		{"duplicate vote", `"u:2":1`, `"u:2":0,"u:2":1`},
		{"invalid UTF8 key", `"u:2":1`, "\"private-\xff\":1"},
		{"unpaired surrogate key", `"u:2":1`, `"private-\ud800":1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := bytes.Replace(original, []byte(test.old), []byte(test.replacement), 1)
			if bytes.Equal(payload, original) {
				t.Fatal("fixture was not changed")
			}
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.State(t.Context(), 1, 1, c, "u:2"); err == nil {
				t.Error("invalid voting record was counted")
			}
			if _, err := s.Vote(t.Context(), 1, 1, c, "u:3", 2); err == nil {
				t.Error("invalid voting record was overwritten")
			}
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, payload) {
				t.Fatal("rejected operation modified the record", err)
			}
		})
	}
}
