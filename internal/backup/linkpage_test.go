package backup

import (
	"newpress.local/newpress/internal/linkpage"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupRestoresLinkDirectories(t *testing.T) {
	f := fixtureSite(t, false)
	s, e := linkpage.NewFileStore(filepath.Join(f.cfg.DataDir, "link-pages.json"))
	if e != nil {
		t.Fatal(e)
	}
	cfg := linkpage.Defaults()
	a := linkpage.Actor{ID: 1, Admin: true}
	now := time.Now().UTC()
	if _, e = s.Apply(t.Context(), "friends", a, linkpage.Command{Action: "create", Key: "backup-links-create", ExpectedUserID: 1, Settings: &cfg}, now); e != nil {
		t.Fatal(e)
	}
	c := linkpage.Command{Action: "save_item", Key: "backup-links-item", ExpectedUserID: 1, Version: 1, Item: &linkpage.Item{Name: "Friend", URL: "example.com", Enabled: true}}
	if _, e = s.Apply(t.Context(), "friends", a, c, now); e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(f.parent, "links.zip")
	if _, e = Create(t.Context(), f.cfg, archive); e != nil {
		t.Fatal(e)
	}
	restored, e := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-links"))
	if e != nil || !restored.ApplicationValidated {
		t.Fatal(e)
	}
	s, e = linkpage.NewFileStore(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "link-pages.json"))
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Get(t.Context(), "friends")
	if e != nil || len(b.Items) != 1 || b.Settings.Style != "cards" {
		t.Fatal(b, e)
	}
	v, e := s.Apply(t.Context(), "friends", a, c, now)
	if e != nil || !v.Replay {
		t.Fatal(v, e)
	}
}
