package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/pluginjobs"
)

func maintenanceCommandFileFixture(t *testing.T) (string, pluginjobs.Job, []byte) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("NEWPRESS_DATA_DIR", root)
	t.Setenv("NEWPRESS_THEMES_DIR", filepath.Join(root, "themes"))
	t.Setenv("NEWPRESS_PLUGINS_DIR", filepath.Join(root, "plugins"))
	t.Setenv("NEWPRESS_DATABASE_URL", "")
	t.Setenv("NEWPRESS_DATABASE_URL_FILE", "")
	path := filepath.Join(root, "plugin-jobs.json")
	b, err := pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	j, err := (&pluginjobs.Queue{Backend: b}).Enqueue(t.Context(), pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "private-command-key", ActorID: 7, Payload: json.RawMessage(`{"private":"private original parameters"}`)}, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(j)
	body, _ := os.ReadFile(path)
	body = bytes.Replace(body, []byte(`"status":"pending"`), []byte(`"status":"broken"`), 1)
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path, j, original
}

func checksumForMaintenance(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func TestPluginMaintenanceCommandFileRoundTripAndLock(t *testing.T) {
	path, j, original := maintenanceCommandFileFixture(t)
	m, _ := pluginjobs.NewFileMaintenance(path)
	page, err := m.InspectRecords(t.Context(), pluginjobs.DiagnosticQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Fault != "invalid_data" {
		t.Fatal(page, err)
	}
	if err = pluginCommand([]string{"maintain-records", "inspect", "--type", "task"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(path)
	locks, err := dirlock.Acquire(root, filepath.Join(root, "themes"), filepath.Join(root, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if err = maintainPluginRecords([]string{"inspect"}); err == nil {
		locks.Close()
		t.Fatal("CLI inspection bypassed the running site's directory lock")
	}
	locks.Close()
	args := []string{"quarantine", "--type", "task", "--reference", page.Items[0].Reference, "--request-key", "file-command", "--operator", "local-person", "--reason", "bad status checked", "--confirm-all-workers-stopped", "--snapshot-sha256", page.SnapshotDigest}
	if err = maintainPluginRecords(args); err != nil {
		t.Fatal(err)
	}
	quarantines, err := m.ListQuarantines(t.Context(), pluginjobs.DiagnosticQuery{})
	if err != nil || quarantines.Total != 1 {
		t.Fatal(quarantines, err)
	}
	body, _ := os.ReadFile(path)
	if err = maintainPluginRecords(args); err != nil {
		t.Fatal("same full command could not confirm isolation", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(body, after) {
		t.Fatal("command retry changed isolated evidence")
	}
	page, _ = m.InspectRecords(t.Context(), pluginjobs.DiagnosticQuery{})
	replacement := filepath.Join(root, "verified-original-record.json")
	if err = os.WriteFile(replacement, original, 0600); err != nil {
		t.Fatal(err)
	}
	repair := []string{"repair", "--id", quarantines.Items[0].ID, "--operator", "local-person", "--reason", "verified original checksum", "--confirm-all-workers-stopped", "--snapshot-sha256", page.SnapshotDigest, "--replacement", replacement, "--replacement-sha256", checksumForMaintenance(original)}
	if err = maintainPluginRecords(repair); err != nil {
		t.Fatal(err)
	}
	if err = maintainPluginRecords(repair); err != nil {
		t.Fatal("repair confirmation rejected old snapshot digest", err)
	}
	if err = maintainPluginRecords([]string{"quarantines", "--limit", "1"}); err != nil {
		t.Fatal(err)
	}
	b, err := pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := b.Read(t.Context())
	repaired := state.Jobs[j.ID]
	if state.Schema != 4 || repaired.Status != "cancelled" || repaired.Version != j.Version+1 || !bytes.Equal(repaired.Payload, j.Payload) || len(state.Quarantines) != 1 {
		t.Fatal("CLI repair lost identity, parameters or cancellation")
	}
	for _, q := range state.Quarantines {
		if q.Operator != "local-person" || q.Repair == nil || q.Repair.Operator != "local-person" {
			t.Fatal("CLI invented or lost the operator declaration")
		}
	}
}

func TestPluginMaintenanceCommandRejectsIncompleteOrMisappliedFlags(t *testing.T) {
	path, _, _ := maintenanceCommandFileFixture(t)
	body, _ := os.ReadFile(path)
	for _, args := range [][]string{
		nil, {"unknown"}, {"inspect", "--reason", "not a write"}, {"inspect", "--type", "unknown"}, {"inspect", "--limit", "0"}, {"inspect", "--limit", "1001"}, {"inspect", "--offset", "-1"},
		{"quarantine"}, {"quarantine", "--type", "task", "--reference", strings.Repeat("a", 64), "--operator", "operator", "--reason", "checked", "--confirm-all-workers-stopped"},
		{"repair", "--id", strings.Repeat("a", 64), "--operator", "operator", "--reason", "checked"},
		{"repair", "--id", strings.Repeat("a", 64), "--operator", "operator", "--reason", "checked", "--confirm-all-workers-stopped", "--replacement", "missing-checksum.json"},
		{"repair", "--id", strings.Repeat("a", 64), "--operator", "operator", "--reason", "checked", "--confirm-all-workers-stopped", "--keep-reference", strings.Repeat("b", 64), "--replacement", "file.json", "--replacement-sha256", strings.Repeat("c", 64)},
		{"quarantine", "--type", "task", "--reference", strings.Repeat("a", 64), "--request-key", "key", "--operator", "operator", "--reason", "checked", "--confirm-all-workers-stopped"}, // no file snapshot checksum
	} {
		t.Run(fmtMaintenanceArguments(args), func(t *testing.T) {
			if err := maintainPluginRecords(args); err == nil {
				t.Fatal("incomplete maintenance command accepted", args)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(body, after) {
				t.Fatal("rejected command changed original storage")
			}
		})
	}
}

func fmtMaintenanceArguments(args []string) string {
	return checksumForMaintenance([]byte(strings.Join(args, " ")))[:8]
}

func TestPluginMaintenanceReplacementChecksumAndRegularFileBounds(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "record.json")
	original := []byte(`{"private":"verified record"}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := readPluginRepairReplacement(path, checksumForMaintenance(original)); err != nil || !reflect.DeepEqual(result, original) {
		t.Fatal(result, err)
	}
	if _, err := readPluginRepairReplacement(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("replacement with the wrong SHA256 accepted")
	}
	if _, err := readPluginRepairReplacement(root, checksumForMaintenance(original)); err == nil {
		t.Fatal("directory used as record replacement")
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), (512<<10)+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPluginRepairReplacement(path, checksumForMaintenance(original)); err == nil {
		t.Fatal("oversized replacement accepted")
	}
}
