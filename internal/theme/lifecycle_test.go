package theme

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/extension"
)

func TestThemeSwitchPersistsAndUninstallKeepsSettings(t *testing.T) {
	m, first := themeOTAFixture(t)
	second := first
	second.Slug, second.Name = "another-theme", "Another"
	_, archive := themeOTAArchive(t, second, otaSettings, `{{define "content"}}another{{end}}`)
	if _, err := m.InstallPackage(archive); err != nil {
		t.Fatal(err)
	}
	state, _ := m.VersionState(second.Slug)
	if err := m.Activate(t.Context(), second.Slug, second.Version, state.Revision, "stale-theme", nil); !errors.Is(err, extension.ErrConflict) {
		t.Fatal("stale selection accepted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.Activate(ctx, second.Slug, second.Version, state.Revision, first.Slug, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := m.Activate(t.Context(), second.Slug, second.Version, state.Revision, first.Slug, nil); err != nil {
		t.Fatal(err)
	}
	if m.Active().Slug != second.Slug || m.SettingsSnapshot().Values["headline"] != "default" {
		t.Fatal("selection or settings leaked")
	}
	var out bytes.Buffer
	if err := m.Render(&out, "home", nil); err != nil || out.String() != "another" {
		t.Fatal(out.String(), err)
	}
	restarted := NewManager(m.root, first.Slug, WithSettingsDir(m.settingsDir))
	if err := restarted.Load(); err != nil || restarted.Active().Slug != second.Slug {
		t.Fatal("saved theme not restored", err)
	}
	if err := restarted.Uninstall(t.Context(), second.Slug, second.Version, state.Revision, second.Slug, nil); err == nil {
		t.Fatal("active theme removed")
	}
	firstState, _ := m.VersionState(first.Slug)
	if err := m.Activate(t.Context(), first.Slug, first.Version, firstState.Revision, second.Slug, nil); err != nil {
		t.Fatal(err)
	}
	if m.SettingsSnapshot().Values["headline"] != "自定义外观" {
		t.Fatal("previous theme settings lost")
	}
	if err := m.Activate(t.Context(), second.Slug, second.Version, state.Revision, first.Slug, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(t.Context(), first.Slug, first.Version, firstState.Revision, second.Slug, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.root, first.Slug)); !os.IsNotExist(err) {
		t.Fatal("theme still installed", err)
	}
	if _, err := os.Stat(settingsFilePath(m.settingsDir, first.Slug)); err != nil {
		t.Fatal("uninstall deleted site settings", err)
	}
	if selected, err := SelectedSlug(m.root, first.Slug); err != nil || selected != second.Slug {
		t.Fatal(selected, err)
	}
}

func TestManagerLoadExplicitIgnoresSavedSelection(t *testing.T) {
	manager, manifest := themeOTAFixture(t)
	const savedSlug = "another-installed-theme"
	if err := extension.WriteJSON(filepath.Join(manager.root, SelectionFile), selection{Schema: 1, Slug: savedSlug}); err != nil {
		t.Fatal(err)
	}

	explicit := NewManager(manager.root, savedSlug, WithSettingsDir(manager.settingsDir))
	if err := explicit.LoadExplicit(manifest.Slug); err != nil {
		t.Fatal(err)
	}
	if explicit.Active().Slug != manifest.Slug {
		t.Fatalf("explicit theme = %q, want %q", explicit.Active().Slug, manifest.Slug)
	}
	if selected, err := SelectedSlug(manager.root, manifest.Slug); err != nil || selected != savedSlug {
		t.Fatalf("saved selection = %q, %v; want %q", selected, err, savedSlug)
	}
}

func TestThemeSwitchFailureLeavesSelectionAndRuntime(t *testing.T) {
	for _, fault := range []string{"template", "settings", "persistence", "incompatible"} {
		t.Run(fault, func(t *testing.T) {
			m, first := themeOTAFixture(t)
			next := first
			next.Slug = "candidate"
			if fault == "incompatible" {
				next.NewPress = ">=99.0.0"
			}
			_, archive := themeOTAArchive(t, next, otaSettings, `{{define "content"}}candidate{{end}}`)
			if _, err := m.InstallPackage(archive); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "template":
				if err := os.WriteFile(filepath.Join(m.root, next.Slug, "templates/home.html"), []byte("{{broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "settings":
				if err := os.WriteFile(settingsFilePath(m.settingsDir, next.Slug), []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "persistence":
				if err := os.Mkdir(filepath.Join(m.root, SelectionFile), 0700); err != nil {
					t.Fatal(err)
				}
			}
			state, err := m.VersionState(next.Slug)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Activate(t.Context(), next.Slug, next.Version, state.Revision, first.Slug, nil); err == nil {
				t.Fatal("invalid selection accepted")
			}
			if m.Active().Slug != first.Slug || m.SettingsSnapshot().Values["headline"] != "自定义外观" {
				t.Fatal("failed switch changed running theme")
			}
			var out bytes.Buffer
			if err := m.Render(&out, "home", nil); err != nil || out.String() != "old" {
				t.Fatal(out.String(), err)
			}
		})
	}
}

func TestThemeCoverBoundsAndPaths(t *testing.T) {
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 960, 600))); err != nil {
		t.Fatal(err)
	}
	if mime, err := ValidateCover(pngBytes.Bytes()); err != nil || mime != "image/png" {
		t.Fatal(mime, err)
	}
	for _, data := range [][]byte{[]byte(`<svg onload="alert(1)"></svg>`), []byte("<html>bad</html>"), make([]byte, MaxCoverBytes+1)} {
		if _, err := ValidateCover(data); err == nil {
			t.Fatal("unsafe cover accepted")
		}
	}
	for _, name := range []string{"../cover.png", "/assets/cover.png", "https://evil.test/cover.png", "assets/cover.svg", "assets/../cover.png"} {
		if validCoverPath(name) {
			t.Fatal("unsafe cover path", name)
		}
	}
}

func TestSettingsRevisionCannotCrossThemeSwitch(t *testing.T) {
	m, first := themeOTAFixture(t)
	if _, _, err := m.SaveSettings(map[string]any{"headline": "default"}); err != nil {
		t.Fatal(err)
	}
	old := m.SettingsSnapshot()
	second := first
	second.Slug = "same-settings"
	_, archive := themeOTAArchive(t, second, otaSettings, `{{define "content"}}same{{end}}`)
	if _, err := m.InstallPackage(archive); err != nil {
		t.Fatal(err)
	}
	state, _ := m.VersionState(second.Slug)
	if err := m.Activate(t.Context(), second.Slug, second.Version, state.Revision, first.Slug, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.SaveSettingsRevision(old.Revision, map[string]any{"headline": "wrong theme"}); !errors.Is(err, ErrSettingsRevisionConflict) {
		t.Fatal("stale form crossed theme boundary", err)
	}
	if _, err := m.ResetSettingsRevision(old.Revision); !errors.Is(err, ErrSettingsRevisionConflict) {
		t.Fatal("stale reset accepted", err)
	}
}
