package backup

import (
	"os"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/theme"
)

func TestChildThemeBackupRestore(t *testing.T) {
	fixture := fixtureSite(t, false)
	manager := theme.NewManager(fixture.cfg.ThemesDir, fixture.cfg.ActiveTheme)
	if err := manager.Load(); err != nil {
		t.Fatal(err)
	}
	parent := manager.Active()
	child := theme.Manifest{Schema: 2, Slug: "backup-child", Name: "Backup child", Version: "1.0.0", Engine: parent.Engine, View: parent.View, Parent: &theme.ParentTheme{Slug: parent.Slug, Version: "*"}}
	directory := filepath.Join(fixture.cfg.ThemesDir, child.Slug)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := extension.WriteJSON(filepath.Join(directory, "theme.json"), child); err != nil {
		t.Fatal(err)
	}
	state, err := manager.VersionState(child.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.Activate(t.Context(), child.Slug, child.Version, state.Revision, parent.Slug, nil); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(fixture.parent, "child-backup.zip")
	if _, err = Create(t.Context(), fixture.cfg, archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(fixture.parent, "child-restored")
	if err = os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, destination)
	if err != nil {
		t.Fatal(err)
	}
	root := restored.Environment["NEWPRESS_THEMES_DIR"]
	restarted := theme.NewManager(root, parent.Slug)
	if err = restarted.Load(); err != nil || restarted.Active().Slug != child.Slug {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(root, parent.Slug), filepath.Join(root, ".removed-parent")); err != nil {
		t.Fatal(err)
	}
	if err = theme.NewManager(root, child.Slug).Load(); err == nil {
		t.Fatal("restored child ran without parent")
	}
}
