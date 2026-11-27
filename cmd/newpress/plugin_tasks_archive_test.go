package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestPluginArchiveCommandReclaimsFileQueue(t *testing.T) {
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
	in := pluginjobs.Input{Plugin: "fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "archive-command", Payload: json.RawMessage(`{}`)}
	now := time.Now().UTC().Add(-time.Hour)
	j, err := q.Enqueue(t.Context(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Change(t.Context(), j.Plugin, j.ID, j.Version, "cancel", now); err != nil {
		t.Fatal(err)
	}
	event := pluginjobs.Event{Key: "archive-command-event", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	if _, err = q.FreezeFanout(t.Context(), event, nil); err != nil {
		t.Fatal(err)
	}
	if err = q.CompleteFanout(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if err = pluginCommand([]string{"archive-tasks", "--scope", "all", "--limit", "1", "--before", time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	reopened, err := pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := (&pluginjobs.Queue{Backend: reopened}).Enqueue(t.Context(), in, now)
	if err != nil || replay.Status != "cancelled" || replay.ArchivedAt.IsZero() {
		t.Fatal("CLI dropped task history", replay, err)
	}
	state, err := reopened.Read(t.Context())
	if err != nil || len(state.Jobs) != 0 || len(state.Fanouts) != 0 || len(state.ArchivedJobs) != 1 || len(state.ArchivedFanouts) != 1 {
		t.Fatal("CLI did not archive both classes", err)
	}
}

func TestPluginArchiveCommandRejectsAmbiguousScopeAndCutoff(t *testing.T) {
	before := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	for _, args := range [][]string{
		nil, {"--before", "bad"}, {"--before", before, "--scope", "everything"},
		{"--before", before, "--scope", "all", "--plugin", "reader"},
		{"--before", before, "--limit", "1001"}, {"--before", before, "--limit", "-1"},
		{"--before", before, "--limit", "0"},
		{"--before", before, "extra"}, {"--before", "2100-01-01T00:00:00Z"},
	} {
		if err := archivePluginTasks(args); err == nil {
			t.Fatal("ambiguous archive command accepted", args)
		}
	}
}
