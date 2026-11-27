package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditRejectsFieldAliasesWithValidDigests(t *testing.T) {
	t.Run("ledger", func(t *testing.T) {
		filename := filepath.Join(t.TempDir(), "audit.jsonl")
		ledger, err := Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		if err := ledger.Apply("navigation", testEvent(), []byte("fact"), func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.Replace(body, []byte(`"sequence":1`), []byte(`"sequence":0,"SEQUENCE":1`), 1)
		if !bytes.Contains(body, []byte(`"SEQUENCE"`)) {
			t.Fatal("fixture did not inject alias")
		}
		if err := os.WriteFile(filename, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(filename); !errors.Is(err, ErrCorrupt) {
			t.Errorf("ambiguous hash-chain record accepted: %v", err)
		}
		if after, err := os.ReadFile(filename); err != nil || !bytes.Equal(after, body) {
			t.Errorf("loading changed ledger: %v", err)
		}
	})
	t.Run("checkpoint", func(t *testing.T) {
		directory := t.TempDir()
		ledger, paths := checkpointFixture(t, directory)
		checkpoint, err := ledger.CreateCheckpoint(paths, CheckpointOptions{GeneratedAt: time.Date(2026, 8, 22, 2, 20, 0, 0, time.UTC), SoftwareVersion: "0.1.0-dev", SoftwareCommit: "test"})
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.Replace(body, []byte(`"schema":1`), []byte(`"schema":0,"SCHEMA":1`), 1)
		if !bytes.Contains(body, []byte(`"SCHEMA"`)) {
			t.Fatal("fixture did not inject alias")
		}
		filename := filepath.Join(directory, "checkpoint.json")
		if err := os.WriteFile(filename, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCheckpoint(filename); err == nil {
			t.Error("ambiguous checkpoint with unchanged canonical digest accepted")
		}
		if after, err := os.ReadFile(filename); err != nil || !bytes.Equal(after, body) {
			t.Errorf("loading changed checkpoint: %v", err)
		}
	})
}
