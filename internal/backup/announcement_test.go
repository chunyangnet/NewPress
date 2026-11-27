package backup

import (
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/announcement"
)

func TestBackupRestoresIndependentAnnouncements(t *testing.T) {
	f := fixtureSite(t, false)
	s, err := announcement.NewFileStore(filepath.Join(f.cfg.DataDir, "announcements.json"))
	if err != nil {
		t.Fatal(err)
	}
	state, _ := s.Snapshot(t.Context())
	state, err = s.Save(t.Context(), state.Revision, "save", announcement.Item{Title: "恢复后的公告", Body: "公告与主题设置分别保存。", Date: time.Now().UTC().Add(-time.Hour), Published: true, Pinned: true})
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "announcements.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-announcements"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatal(restored, err)
	}
	s, err = announcement.NewFileStore(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "announcements.json"))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := s.Snapshot(t.Context())
	if err != nil || actual.Revision != state.Revision || len(actual.Items) != 1 || actual.Items[0].Title != "恢复后的公告" || !actual.Items[0].Pinned {
		t.Fatal(actual, err)
	}
}
