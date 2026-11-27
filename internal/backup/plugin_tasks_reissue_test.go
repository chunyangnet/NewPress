package backup

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestBackupRestoresReviewedPluginReissueProvenance(t *testing.T) {
	f := fixtureSite(t, true)
	backend, err := pluginjobs.NewFileBackend(filepath.Join(f.cfg.DataDir, "plugin-jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	now := time.Now().UTC().Add(-time.Hour)
	in := pluginjobs.Input{Plugin: "fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "review-source", ActorID: 1, Payload: json.RawMessage(`{"old":"private parameters"}`)}
	source, err := q.Enqueue(t.Context(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.CancelRestored(t.Context(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	details, err := q.GetReview(t.Context(), in.Plugin, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	in.Key = "new-reviewed-work"
	in.Payload = json.RawMessage(`{"reviewed":"new business parameters"}`)
	request := pluginjobs.ReissueRequest{SourceID: source.ID, SourceVersion: details.Task.Version, Input: in, Reason: "Checked restored work before explicit reissue", AllowNew: true}
	child, err := q.Reissue(t.Context(), request, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.Archive(t.Context(), pluginjobs.ArchiveQuery{Tasks: true, Before: now.Add(time.Second)}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "reviewed-plugin-tasks.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-reviews"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := pluginjobs.NewFileBackend(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "plugin-jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	recovered := &pluginjobs.Queue{Backend: reopened}
	request.Input.Epoch = strings.Repeat("b", 64)
	request.AllowNew = false
	receipt, err := recovered.Reissue(t.Context(), request, time.Now().UTC())
	if err != nil || receipt.ID != child.ID || receipt.Status != "cancelled" || receipt.Reissue == nil || receipt.Reissue.SourceID != source.ID {
		t.Fatal("restore lost reviewed receipt or resumed it", receipt, err)
	}
	details, err = recovered.GetReview(t.Context(), in.Plugin, child.ID)
	if err != nil || details.Provenance == nil || details.Provenance.ReviewerID != 1 || details.Provenance.Reason != request.Reason {
		t.Fatal("backup lost reviewer decision", details, err)
	}
	if _, err = recovered.Archive(t.Context(), pluginjobs.ArchiveQuery{Tasks: true, Before: time.Now().UTC()}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	details, err = recovered.GetReview(t.Context(), in.Plugin, child.ID)
	if err != nil || details.Task.ArchivedAt.IsZero() || details.Provenance.Reason != request.Reason {
		t.Fatal("post-restore archive lost review", details, err)
	}
	state, err := backend.Read(t.Context())
	if err != nil || state.Jobs[child.ID].Status != "pending" {
		t.Fatal("restoring changed source site", err)
	}
}
