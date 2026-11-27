package backup

import (
	"os"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/theme"
)

func TestBackupRestoreKeepsSelectedThemeAfterOriginalUninstalled(t *testing.T) {
	fixture := fixtureSite(t, false)
	manager := theme.NewManager(fixture.cfg.ThemesDir, fixture.cfg.ActiveTheme)
	if err := manager.Load(); err != nil {
		t.Fatal(err)
	}
	original := manager.Active()
	selected := original
	selected.Slug = "selected-theme"
	directory := filepath.Join(fixture.cfg.ThemesDir, selected.Slug)
	if err := extension.CopyTree(t.Context(), filepath.Join(fixture.cfg.ThemesDir, original.Slug), directory); err != nil {
		t.Fatal(err)
	}
	if err := extension.WriteJSON(filepath.Join(directory, "theme.json"), selected); err != nil {
		t.Fatal(err)
	}
	state, err := manager.VersionState(selected.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Activate(t.Context(), selected.Slug, selected.Version, state.Revision, original.Slug, nil); err != nil {
		t.Fatal(err)
	}
	state, err = manager.VersionState(original.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(t.Context(), original.Slug, original.Version, state.Revision, selected.Slug, nil); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(fixture.parent, "selected-theme.zip")
	created, err := Create(t.Context(), fixture.cfg, archive)
	if err != nil {
		t.Fatal(err)
	}
	if created.Runtime.ActiveTheme != selected.Slug || !created.ApplicationValidated {
		t.Fatalf("backup retained stale configured theme: %+v", created)
	}
	destination := filepath.Join(fixture.parent, "restored-theme")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, destination)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Environment["NEWPRESS_ACTIVE_THEME"] != selected.Slug || !restored.ApplicationValidated {
		t.Fatalf("restored theme configuration: %+v", restored)
	}
	// Even an unchanged deployment environment must honor the restored selection.
	restarted := theme.NewManager(restored.Environment["NEWPRESS_THEMES_DIR"], original.Slug)
	if err := restarted.Load(); err != nil || restarted.Active().Slug != selected.Slug {
		t.Fatalf("saved selection was not preserved: %v", err)
	}
}
