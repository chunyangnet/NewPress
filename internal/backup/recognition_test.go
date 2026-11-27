package backup

import (
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/recognition"
)

func TestBackupRestoresCommunityIdentity(t *testing.T) {
	f := fixtureSite(t, true)
	b, err := recognition.NewFileBackend(filepath.Join(f.cfg.DataDir, "recognition.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := &recognition.Service{Backend: b, Users: progressionBackupUser{}}
	if _, err = service.UpdateIdentity(t.Context(), 1, true, recognition.IdentityInput{UserID: 1, Version: 1, Action: "grant", Key: "original", Reason: "原创贡献"}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "recognition.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-recognition"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatal(restored, err)
	}
	b, err = recognition.NewFileBackend(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "recognition.json"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := b.Read(t.Context())
	if err != nil || len(state.PublicBadges(1, time.Now())) != 1 {
		t.Fatal(state, err)
	}
}
