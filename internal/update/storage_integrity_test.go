package update

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadUpdateRecordRejectsAmbiguousOrOversizedJSON(t *testing.T) {
	const limit = 256 << 10
	base := `{"phase":"idle"}`
	for _, test := range []struct {
		name, body string
	}{
		{"duplicate phase", `{"phase":"staging","phase":"idle"}`},
		{"escaped duplicate", `{"phase":"staging","\u0070hase":"idle"}`},
		{"duplicate log field", `{"phase":"idle","logs":[{"message":"first","message":"second"}]}`},
		{"oversized whitespace", base + strings.Repeat(" ", limit-len(base)+1)},
		{"trailing document beyond limit", base + strings.Repeat(" ", limit-len(base)) + `{"phase":"queued"}`},
		{"trailing garbage beyond limit", base + strings.Repeat(" ", limit-len(base)) + `broken`},
		{"unknown field", `{"phase":"idle","unknown":true}`},
		{"trailing document within limit", base + `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(filename, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			var state State
			if err := readJSON(filename, &state); err == nil {
				t.Errorf("invalid update record accepted: phase=%q logs=%v", state.Phase, state.Logs)
			}
			if body, err := os.ReadFile(filename); err != nil || string(body) != test.body {
				t.Errorf("reading changed record: %v", err)
			}
		})
	}
}

func TestReadUpdateRecordRetainsDefaultsAndExactLimit(t *testing.T) {
	const limit = 256 << 10
	for _, size := range []int{len(`{"owner":"team","repo":"site"}`), limit} {
		name := "normal"
		if size == limit {
			name = "exact limit"
		}
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "source.json")
			body := `{"owner":"team","repo":"site"}`
			body += strings.Repeat(" ", size-len(body))
			if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			source := Source{Ref: "master", Manifest: "updates/stable.json"}
			if err := readJSON(filename, &source); err != nil || source != (Source{Owner: "team", Repo: "site", Ref: "master", Manifest: "updates/stable.json"}) {
				t.Fatalf("valid record lost defaults or failed: %+v, %v", source, err)
			}
		})
	}
	filename := filepath.Join(t.TempDir(), "version.json")
	if err := writeJSON(filename, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := readJSON(filename, &value); err != nil || value != "0.2.0" {
		t.Fatalf("scalar record no longer works: %q, %v", value, err)
	}
}

func TestUpdateStartupRejectsInvalidStateWithoutChangingRecords(t *testing.T) {
	for _, kind := range []string{"duplicate phase", "hidden trailer"} {
		t.Run(kind, func(t *testing.T) {
			manager := managerFixture(t)
			manager.Close()
			statePath := filepath.Join(manager.root, "state.json")
			body := []byte(`{"id":"interrupted","phase":"staging","phase":"idle"}`)
			if kind == "hidden trailer" {
				body = []byte(`{"id":"interrupted","phase":"staging"}`)
				body = append(body, bytes.Repeat([]byte(" "), (256<<10)-len(body))...)
				body = append(body, []byte(`{"phase":"idle"}`)...)
			}
			if err := os.WriteFile(statePath, body, 0o600); err != nil {
				t.Fatal(err)
			}
			opened, err := New(manager.root, manager.current, manager.themes, false)
			if opened != nil {
				opened.Close()
			}
			if err == nil {
				t.Error("startup accepted damaged task state")
			}
			if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, body) {
				t.Errorf("startup rewrote unvalidated state: %v", err)
			}
		})
	}
}

func TestUpdateProgressRejectsAmbiguousTicket(t *testing.T) {
	manager := managerFixture(t)
	state := preparationState("idle")
	if err := manager.save(state); err != nil {
		t.Fatal(err)
	}
	ticket := strings.Repeat("a", 64)
	value := progressTicket{Hash: digest([]byte(ticket)), Job: state.ID, Expires: time.Now().Add(time.Hour)}
	filename := filepath.Join(manager.root, "ticket.json")
	if err := writeJSON(filename, value); err != nil {
		t.Fatal(err)
	}
	if got, ok := manager.Progress(ticket); !ok || !reflect.DeepEqual(got, state) {
		t.Fatal("valid progress ticket rejected", got, ok)
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	body = append([]byte(`{"job":"another-task",`), body[1:]...)
	if err := os.WriteFile(filename, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Progress(ticket); ok {
		t.Error("ambiguous ticket authorizes progress")
	}
	if after, err := os.ReadFile(filename); err != nil || !bytes.Equal(after, body) {
		t.Errorf("ticket verification changed input: %v", err)
	}
}

func TestWriteUpdateRecordEnforcesReadableSizeBeforePublication(t *testing.T) {
	const limit = 256 << 10
	for _, existing := range []bool{false, true} {
		name := "new record"
		if existing {
			name = "existing record"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			filename := filepath.Join(root, "state.json")
			original := []byte("{\"phase\":\"idle\"}\n")
			if existing {
				if err := os.WriteFile(filename, original, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// Quotes and the encoder's newline make this exactly one byte too large.
			if err := writeJSON(filename, strings.Repeat("x", limit-2)); err == nil {
				t.Error("oversized record was published")
			}
			if existing {
				if after, err := os.ReadFile(filename); err != nil || !bytes.Equal(after, original) {
					t.Errorf("oversized record replaced existing state: %v", err)
				}
			} else if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("oversized record created target: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != "state.json" {
					t.Errorf("write left a temporary file: %s", entry.Name())
				}
			}
		})
	}
	filename := filepath.Join(t.TempDir(), "exact.json")
	want := strings.Repeat("x", limit-3)
	if err := writeJSON(filename, want); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := readJSON(filename, &got); err != nil || got != want {
		t.Fatalf("exact-limit record did not round trip: size=%d, %v", len(got), err)
	}
}
