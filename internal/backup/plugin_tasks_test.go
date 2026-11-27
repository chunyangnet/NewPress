package backup

import (
	"encoding/json"
	"newpress.local/newpress/internal/pluginjobs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupRestoresPluginTasksWithoutResuming(t *testing.T) {
	f := fixtureSite(t, true)
	backend, err := pluginjobs.NewFileBackend(filepath.Join(f.cfg.DataDir, "plugin-jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	queue := &pluginjobs.Queue{Backend: backend}
	in := pluginjobs.Input{Plugin: "fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "restore-test", ActorID: 1, Payload: json.RawMessage(`{}`)}
	original, err := queue.Enqueue(t.Context(), in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "plugin-tasks.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-tasks"))
	if err != nil {
		t.Fatal(err)
	}
	restoredBackend, err := pluginjobs.NewFileBackend(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "plugin-jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: restoredBackend}
	replay, err := q.Enqueue(t.Context(), in, time.Now())
	if err != nil || replay.ID != original.ID || replay.Status != "cancelled" {
		t.Fatalf("restore replay: %+v %v", replay, err)
	}
	if _, ok, err := q.Claim(t.Context(), time.Now().Add(time.Hour)); err != nil || ok {
		t.Fatalf("restored work resumed: %v %v", ok, err)
	}
	source, err := backend.Read(t.Context())
	if err != nil || source.Jobs[original.ID].Status != "pending" {
		t.Fatal("restore mutated source", err)
	}
}
