package main

import (
	"encoding/json"
	"newpress.local/newpress/internal/pluginjobs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPluginRestoreCommandCancelsFileQueue(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NEWPRESS_DATA_DIR", root)
	t.Setenv("NEWPRESS_THEMES_DIR", filepath.Join(root, "themes"))
	t.Setenv("NEWPRESS_PLUGINS_DIR", filepath.Join(root, "plugins"))
	t.Setenv("NEWPRESS_DATABASE_URL", "")
	t.Setenv("NEWPRESS_DATABASE_URL_FILE", "")
	path := filepath.Join(root, "plugin-jobs.json")
	backend, err := pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	input := pluginjobs.Input{Plugin: "fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "restore-command", Payload: json.RawMessage(`{}`)}
	if _, err = q.Enqueue(t.Context(), input, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = pluginCommand([]string{"cancel-restored-tasks", "--confirm-all-workers-stopped"}); err != nil {
		t.Fatal(err)
	}
	reopened, err := pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	job, err := (&pluginjobs.Queue{Backend: reopened}).Enqueue(t.Context(), input, time.Now())
	if err != nil || job.Status != "cancelled" {
		t.Fatal("restored task replayed", job.Status, err)
	}
}

func TestPluginRestoreRequiresExplicitOfflineConfirmation(t *testing.T) {
	for _, args := range [][]string{nil, {"--yes"}, {"--confirm-all-workers-stopped", "extra"}} {
		if err := cancelRestoredPluginTasks(args); err == nil {
			t.Fatalf("accepted missing or ambiguous confirmation: %v", args)
		}
	}
}
