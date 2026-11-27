package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/audit"
)

func TestAuditCheckpointCommandsUseExistingFileLedger(t *testing.T) {
	directory := t.TempDir()
	ledger, err := audit.Open(filepath.Join(directory, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	facts := map[string]string{
		audit.FactUsers:      filepath.Join(directory, "users.json"),
		audit.FactComments:   filepath.Join(directory, "comments.json"),
		audit.FactNavigation: filepath.Join(directory, "navigation.json"),
	}
	for key, path := range facts {
		if err := os.WriteFile(path, []byte(`{"schema":1,"items":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ledger.EnsureBaselinePath(key, path); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NEWPRESS_DATA_DIR", directory)
	t.Setenv("NEWPRESS_THEMES_DIR", filepath.Join(directory, "themes"))
	t.Setenv("NEWPRESS_PLUGINS_DIR", filepath.Join(directory, "plugins"))
	t.Setenv("NEWPRESS_DATABASE_URL", "")

	value, err := auditCommand([]string{"checkpoint"})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, ok := value.(audit.Checkpoint)
	if !ok || checkpoint.Kind != "newpress.audit_checkpoint" || checkpoint.ManifestSHA256 == "" {
		t.Fatalf("checkpoint command = %#v", value)
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	checkpointPath := filepath.Join(directory, "checkpoint.json")
	if err := os.WriteFile(checkpointPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	verified, err := auditCommand([]string{"verify-checkpoint", checkpointPath})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := verified.(audit.CheckpointVerification)
	if !ok || !result.Valid || result.Relation != audit.CheckpointRelationExact || !result.CurrentFactsVerified {
		t.Fatalf("verify command = %#v", verified)
	}
}

func TestAuditCheckpointCommandRejectsDatabaseModeAndMissingLedger(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("NEWPRESS_DATA_DIR", directory)
	t.Setenv("NEWPRESS_THEMES_DIR", filepath.Join(directory, "themes"))
	t.Setenv("NEWPRESS_PLUGINS_DIR", filepath.Join(directory, "plugins"))
	t.Setenv("NEWPRESS_DATABASE_URL", "postgres://example.invalid/newpress")
	if _, err := auditCommand([]string{"checkpoint"}); err == nil {
		t.Fatal("database checkpoint command succeeded")
	}
	t.Setenv("NEWPRESS_DATABASE_URL", "")
	if _, err := auditCommand([]string{"checkpoint"}); err == nil {
		t.Fatal("missing-ledger checkpoint command succeeded")
	}
	if _, err := auditCommand([]string{"verify-checkpoint"}); err == nil {
		t.Fatal("invalid checkpoint command shape succeeded")
	}
}
