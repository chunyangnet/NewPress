package backup

import (
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/feedback"
)

func TestBackupRestoresFeedbackSettingsContentAndReceipts(t *testing.T) {
	f := fixtureSite(t, false)
	s, e := feedback.NewFileStore(filepath.Join(f.cfg.DataDir, "feedback.json"))
	if e != nil {
		t.Fatal(e)
	}
	a := feedback.Actor{ID: 1, Admin: true}
	now := time.Now().UTC()
	settings := feedback.Defaults()
	settings.Moderation = true
	_, e = s.Apply(t.Context(), "ideas", a, feedback.Command{Action: "create", Key: "backup-create-key", Settings: &settings}, now)
	if e != nil {
		t.Fatal(e)
	}
	cmd := feedback.Command{Action: "submit", Key: "backup-submit-key", Version: 1, Title: "备份中的私密反馈", Body: "恢复后仍应保留权限、状态和请求回执。", Kind: "issue", Private: true}
	first, e := s.Apply(t.Context(), "ideas", a, cmd, now)
	if e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(f.parent, "feedback.zip")
	if _, e = Create(t.Context(), f.cfg, archive); e != nil {
		t.Fatal(e)
	}
	restored, e := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-feedback"))
	if e != nil || !restored.ApplicationValidated {
		t.Fatal(restored, e)
	}
	s, e = feedback.NewFileStore(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "feedback.json"))
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Get(t.Context(), "ideas")
	if e != nil || !b.Settings.Moderation || len(b.Items) != 1 || !b.Items[0].Private {
		t.Fatal("feedback restore", e, b)
	}
	replay, e := s.Apply(t.Context(), "ideas", a, cmd, now)
	if e != nil || !replay.Replay || replay.ItemID != first.ItemID {
		t.Fatal("restored receipt", e, replay)
	}
}
