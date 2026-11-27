package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestBackupRestoresPluginQuarantinesAndPreservesOriginalEvidence(t *testing.T) {
	f := fixtureSite(t, true)
	path := filepath.Join(f.cfg.DataDir, "plugin-jobs.json")
	b, err := pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: b}
	now := time.Now().UTC().Add(-time.Hour)
	in := pluginjobs.Input{Plugin: "fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "private-isolated-key", ActorID: 1, Payload: json.RawMessage(`{"private":"original quarantine parameters"}`)}
	original, err := q.Enqueue(t.Context(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	body = bytes.Replace(body, []byte(`"status":"pending"`), []byte(`"status":"broken"`), 1)
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	m, _ := pluginjobs.NewFileMaintenance(path)
	page, err := m.InspectRecords(t.Context(), pluginjobs.DiagnosticQuery{})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	isolated, err := m.QuarantineRecord(t.Context(), pluginjobs.QuarantineRequest{Reference: page.Items[0].Reference, Type: pluginjobs.RecordTask, Key: "backup-isolation", Operator: "local-fixture", Reason: "bad status checked"}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	replacement, _ := json.Marshal(original)
	// Preserve an unresolved quarantine across the first restore.
	b, err = pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := b.Read(t.Context())
	archive := filepath.Join(f.parent, "quarantine-site.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "plugin-jobs.json")
	reopened, err := pluginjobs.NewFileBackend(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := reopened.Read(t.Context())
	if after.Schema != 4 || !reflect.DeepEqual(after.Quarantines, before.Quarantines) || after.Quarantines[isolated.ID].Repair != nil {
		t.Fatal("restore discarded or auto-repaired original evidence")
	}
	maintenance, _ := pluginjobs.NewFileMaintenance(restoredPath)
	digest := sha256ForBackupMaintenance(replacement)
	if _, err = maintenance.RepairRecord(t.Context(), pluginjobs.RepairRequest{ID: isolated.ID, Operator: "local-fixture", Reason: "verified matching original", Replacement: replacement, ReplacementDigest: digest}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// A second backup/restore retains the repair audit and its private original.
	restoredConfig := f.cfg
	restoredConfig.DataDir = restored.Environment["NEWPRESS_DATA_DIR"]
	restoredConfig.ThemesDir = restored.Environment["NEWPRESS_THEMES_DIR"]
	restoredConfig.PluginsDir = restored.Environment["NEWPRESS_PLUGINS_DIR"]
	secondArchive := filepath.Join(f.parent, "repaired-quarantine-site.zip")
	if _, err = Create(t.Context(), restoredConfig, secondArchive); err != nil {
		t.Fatal(err)
	}
	second, err := Restore(t.Context(), secondArchive, filepath.Join(f.parent, "restored-repaired-quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	final, err := pluginjobs.NewFileBackend(filepath.Join(second.Environment["NEWPRESS_DATA_DIR"], "plugin-jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	state, _ := final.Read(t.Context())
	if state.Quarantines[isolated.ID].Repair == nil || !reflect.DeepEqual(state.Quarantines[isolated.ID].Original, before.Quarantines[isolated.ID].Original) || state.Jobs[original.ID].Status != "cancelled" {
		t.Fatal("second restore lost the repair audit/original or resumed work")
	}
	source, err := b.Read(t.Context())
	if err != nil || !reflect.DeepEqual(source.Quarantines, before.Quarantines) {
		t.Fatal("restore modified source evidence", err)
	}
}

func sha256ForBackupMaintenance(raw []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
