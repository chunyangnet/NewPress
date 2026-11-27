package audit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckpointExactAndExtendedVerification(t *testing.T) {
	directory := t.TempDir()
	ledger, paths := checkpointFixture(t, directory)
	before := ledger.Records()
	generatedAt := time.Date(2026, 8, 22, 2, 10, 0, 0, time.UTC)
	checkpoint, err := ledger.CreateCheckpoint(paths, CheckpointOptions{
		GeneratedAt: generatedAt, SoftwareVersion: "0.1.0-dev", SoftwareCommit: "test-commit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Schema != 1 || checkpoint.Kind != checkpointKind || checkpoint.GeneratedAt != generatedAt ||
		checkpoint.Ledger.File != FileName || checkpoint.Ledger.Sequence != uint64(len(before)) ||
		checkpoint.Ledger.RecordCount != uint64(len(before)) || checkpoint.ManifestSHA256 == "" || len(checkpoint.Facts) != 3 {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	if after := ledger.Records(); len(after) != len(before) {
		t.Fatalf("checkpoint mutated ledger: %d -> %d", len(before), len(after))
	}
	exact, err := ledger.VerifyCheckpoint(checkpoint, paths)
	if err != nil {
		t.Fatal(err)
	}
	if !exact.Valid || exact.Relation != CheckpointRelationExact || !exact.AnchoredFactsVerified || !exact.CurrentFactsVerified ||
		exact.CheckpointSequence != checkpoint.Ledger.Sequence || exact.CurrentSequence != checkpoint.Ledger.Sequence {
		t.Fatalf("exact verification = %#v", exact)
	}

	navigationPayload := []byte(`{"schema":1,"menus":[]}`)
	if _, err := ledger.ApplyFile(FactNavigation, Event{
		Action: "navigation.registry.replace", ObjectType: "navigation_registry", ObjectID: "all", Actor: "operator",
	}, navigationPayload, paths[FactNavigation], func() error {
		return os.WriteFile(paths[FactNavigation], navigationPayload, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	extended, err := ledger.VerifyCheckpoint(checkpoint, paths)
	if err != nil {
		t.Fatal(err)
	}
	if !extended.Valid || extended.Relation != CheckpointRelationExtended || !extended.AnchoredFactsVerified || extended.CurrentFactsVerified ||
		extended.CurrentSequence <= extended.CheckpointSequence {
		t.Fatalf("extended verification = %#v", extended)
	}
}

func TestCheckpointReadRejectsTamperingUnknownFieldsAndSymlink(t *testing.T) {
	directory := t.TempDir()
	ledger, paths := checkpointFixture(t, directory)
	checkpoint, err := ledger.CreateCheckpoint(paths, CheckpointOptions{
		GeneratedAt:     time.Date(2026, 8, 22, 2, 20, 0, 0, time.UTC),
		SoftwareVersion: "0.1.0-dev", SoftwareCommit: "test-commit",
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "checkpoint.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := ReadCheckpoint(path)
	if err != nil || read.ManifestSHA256 != checkpoint.ManifestSHA256 {
		t.Fatalf("read checkpoint = %#v, %v", read, err)
	}

	tampered := checkpoint
	tampered.Facts[0].SHA256 = strings.Repeat("0", 64)
	tamperedBytes, _ := json.Marshal(tampered)
	if err := os.WriteFile(path, tamperedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCheckpoint(path); err == nil || !strings.Contains(err.Error(), "manifest digest") {
		t.Fatalf("tampered checkpoint error = %v", err)
	}

	unknown := append([]byte(nil), encoded[:len(encoded)-2]...)
	unknown = append(unknown, []byte(",\n  \"unexpected\": true\n}")...)
	if err := os.WriteFile(path, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCheckpoint(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field checkpoint error = %v", err)
	}

	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "checkpoint-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ReadCheckpoint(link); err == nil {
		t.Fatal("symlink checkpoint was accepted")
	}
}

func TestCheckpointRejectsNonCanonicalUTCLocation(t *testing.T) {
	directory := t.TempDir()
	ledger, paths := checkpointFixture(t, directory)
	checkpoint, err := ledger.CreateCheckpoint(paths, CheckpointOptions{
		GeneratedAt:     time.Date(2026, 8, 22, 2, 25, 0, 0, time.UTC),
		SoftwareVersion: "0.1.0-dev", SoftwareCommit: "test-commit",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.GeneratedAt = time.Date(2026, 8, 22, 2, 25, 0, 0, time.FixedZone("zero", 0))
	checkpoint.ManifestSHA256 = checkpointManifestDigest(checkpoint)
	if err := ValidateCheckpoint(checkpoint); err == nil || !strings.Contains(err.Error(), "timestamps must be UTC") {
		t.Fatalf("non-canonical UTC checkpoint error = %v", err)
	}
}

func TestCheckpointRejectsReplacedLedger(t *testing.T) {
	directory := t.TempDir()
	ledger, paths := checkpointFixture(t, directory)
	checkpoint, err := ledger.CreateCheckpoint(paths, CheckpointOptions{
		SoftwareVersion: "0.1.0-dev", SoftwareCommit: "test-commit",
	})
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(directory, FileName)
	temporary := filepath.Join(directory, "audit-replaced.jsonl")
	contents, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, ledgerPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.VerifyCheckpoint(checkpoint, paths); err == nil || !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("replaced ledger verification error = %v", err)
	}
}

func TestCheckpointRejectsFactDriftAndBrokenAnchor(t *testing.T) {
	directory := t.TempDir()
	ledger, paths := checkpointFixture(t, directory)
	checkpoint, err := ledger.CreateCheckpoint(paths, CheckpointOptions{
		SoftwareVersion: "0.1.0-dev", SoftwareCommit: "test-commit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[FactUsers], []byte(`{"schema":1,"tampered":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.CreateCheckpoint(paths, CheckpointOptions{SoftwareVersion: "0.1.0-dev", SoftwareCommit: "test-commit"}); err == nil || !errors.Is(err, ErrFactMismatch) {
		t.Fatalf("drift checkpoint error = %v", err)
	}
	if _, err := ledger.VerifyCheckpoint(checkpoint, paths); err == nil || !errors.Is(err, ErrFactMismatch) {
		t.Fatalf("drift verification error = %v", err)
	}

	if err := os.WriteFile(paths[FactUsers], []byte(`{"schema":1,"users":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := checkpoint
	broken.Ledger.LastRecordHash = strings.Repeat("f", 64)
	broken.ManifestSHA256 = checkpointManifestDigest(broken)
	if _, err := ledger.VerifyCheckpoint(broken, paths); err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("broken anchor error = %v", err)
	}
}

func checkpointFixture(t *testing.T, directory string) (*Ledger, map[string]string) {
	t.Helper()
	ledger, err := Open(filepath.Join(directory, FileName))
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		FactUsers: filepath.Join(directory, "users.json"), FactComments: filepath.Join(directory, "comments.json"),
		FactNavigation: filepath.Join(directory, "navigation.json"),
	}
	payloads := map[string][]byte{
		FactUsers: []byte(`{"schema":1,"users":[]}`), FactComments: []byte(`{"schema":1,"comments":[]}`),
		FactNavigation: []byte(`{"schema":1,"menus":[]}`),
	}
	for _, key := range []string{FactUsers, FactComments, FactNavigation} {
		if err := os.WriteFile(paths[key], payloads[key], 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ledger.EnsureBaselinePath(key, paths[key]); err != nil {
			t.Fatal(err)
		}
	}
	return ledger, paths
}
