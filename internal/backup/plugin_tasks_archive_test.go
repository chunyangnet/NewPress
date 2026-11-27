package backup

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestBackupRestoresPluginArchivesAndCancelsOutstandingFanouts(t *testing.T) {
	f := fixtureSite(t, true)
	path := filepath.Join(f.cfg.DataDir, "plugin-jobs.json")
	backend, err := pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	now := time.Now().UTC().Add(-time.Hour)
	in := pluginjobs.Input{Plugin: "fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "archived-success", ActorID: 1, Payload: json.RawMessage(`{"private":"remove from archive"}`)}
	original, err := q.Enqueue(t.Context(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	claim, ok, err := q.Claim(t.Context(), now)
	if err != nil || !ok {
		t.Fatal("claim", ok, err)
	}
	if err = q.Finish(t.Context(), claim, "", now); err != nil {
		t.Fatal(err)
	}
	subs := []pluginjobs.Subscription{{Plugin: in.Plugin, Epoch: in.Epoch, Since: now, ActorID: 1}}
	event := pluginjobs.Event{Key: "archived-event", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	if _, err = q.FreezeFanout(t.Context(), event, subs); err != nil {
		t.Fatal(err)
	}
	if err = q.CompleteFanout(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if result, err := q.Archive(t.Context(), pluginjobs.ArchiveQuery{Tasks: true, Fanouts: true, Before: now}, now.Add(time.Second)); err != nil || result.Tasks != 1 || result.Fanouts != 1 {
		t.Fatal("seed archive", result, err)
	}
	pending := in
	pending.Key = "pending-restore"
	if _, err = q.Enqueue(t.Context(), pending, now); err != nil {
		t.Fatal(err)
	}
	incomplete := event
	incomplete.Key = "incomplete-event"
	if _, err = q.FreezeFanout(t.Context(), incomplete, subs); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "plugin-history.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-history"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := pluginjobs.NewFileBackend(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "plugin-jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	recovered := &pluginjobs.Queue{Backend: reopened}
	if replay, err := recovered.Enqueue(t.Context(), in, time.Now()); err != nil || replay.ID != original.ID || replay.Status != "succeeded" || replay.ArchivedAt.IsZero() {
		t.Fatal("backup dropped success history", replay, err)
	}
	if replay, err := recovered.Enqueue(t.Context(), pending, time.Now()); err != nil || replay.Status != "cancelled" {
		t.Fatal("restored pending work resumed", replay, err)
	}
	for _, item := range []pluginjobs.Event{event, incomplete} {
		frozen, found, err := recovered.FindFanout(t.Context(), item)
		if err != nil || !found || !frozen.Cancelled || !reflect.DeepEqual(frozen.Subscriptions, subs) {
			t.Fatal("restore lost archived or outstanding cohorts", frozen, err)
		}
	}
	source, err := backend.Read(t.Context())
	if err != nil || source.ArchivedFanouts[event.Key].Receipt.Cancelled || len(source.Jobs) != 1 {
		t.Fatal("restoring changed source history", err)
	}
}
