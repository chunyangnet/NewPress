package theme

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
)

// Exercise complete release archives against disposable installations. This
// optional audit never changes an installed workspace or production theme.
func TestFrontendThemeReleaseArchivesRollback(t *testing.T) {
	directory := os.Getenv("NEWPRESS_THEME_RELEASE_ARCHIVES")
	if directory == "" {
		t.Skip("opt-in audit of packaged frontend theme releases")
	}
	if !filepath.IsAbs(directory) {
		t.Fatal("absolute release evidence directory required")
	}
	readRelease := func(t *testing.T, path string) Release {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		file, err := archive.Open("theme.json")
		if err != nil {
			t.Fatal(err)
		}
		var manifest Manifest
		err = json.NewDecoder(file).Decode(&manifest)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		return Release{Manifest: manifest, Path: "packages/" + filepath.Base(path), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)),
			Release: &extension.Metadata{Channel: "stable", PublishedAt: time.Now().UTC(), DataCompatible: true}}
	}
	verifyFiles := func(t *testing.T, manager *Manager, archivePath string) {
		t.Helper()
		archive, err := zip.OpenReader(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		defer archive.Close()
		for _, entry := range archive.File {
			if entry.FileInfo().IsDir() {
				continue
			}
			reader, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			want, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(manager.root, manager.Active().Slug, filepath.FromSlash(entry.Name)))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("installed %s differs from release archive: %v", entry.Name, err)
			}
		}
	}
	type releaseCase struct {
		Slug     string `json:"slug"`
		Previous string `json:"previous"`
		Current  string `json:"current"`
		Asset    string `json:"asset"`
	}
	cases := []releaseCase{
		{"chunnel", "0.48.18", "0.48.19", "css/chunnel-home.css"},
		{"docshub", "2.0.3", "2.0.4", "js/docshub.js"},
		{"nexora", "1.0.4", "1.0.4", "js/nexora.js"},
	}
	// A release audit records its exact versions beside the archives. Keep the
	// original cases as the fallback so historical evidence remains replayable.
	if data, err := os.ReadFile(filepath.Join(directory, "cases.json")); err == nil {
		if err := json.Unmarshal(data, &cases); err != nil || len(cases) == 0 {
			t.Fatalf("invalid release archive cases: %v", err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var results []map[string]any
	for _, item := range cases {
		t.Run(item.Slug, func(t *testing.T) {
			if item.Slug == "" || item.Previous == "" || item.Current == "" || item.Asset == "" {
				t.Fatal("release case requires slug, previous/current versions and an asset")
			}
			previous := filepath.Join(directory, "theme-packages-clean", item.Slug+"-"+item.Previous+"-1.zip")
			current := filepath.Join(directory, "theme-final", "theme-packages-clean", item.Slug+"-"+item.Current+"-1.zip")
			manager := NewManager(t.TempDir(), item.Slug, WithSettingsDir(t.TempDir()))
			if _, err := manager.InstallPackage(previous); err != nil {
				t.Fatal(err)
			}
			if err := manager.LoadExplicit(item.Slug); err != nil {
				t.Fatal(err)
			}
			verifyFiles(t, manager, previous)
			oldURL := manager.AssetURL(item.Asset)
			if item.Previous != item.Current {
				settings := manager.SettingsSnapshot().Values
				settings["sidebar_enabled"] = false
				if _, fields, err := manager.SaveSettings(settings); err != nil || len(fields) > 0 {
					t.Fatal(fields, err)
				}
				target, err := manager.CaptureUpdate(item.Slug, "", "")
				if err != nil {
					t.Fatal(err)
				}
				if err := manager.UpdatePackage(t.Context(), current, readRelease(t, current), target, nil); err != nil {
					t.Fatal(err)
				}
				verifyFiles(t, manager, current)
				if manager.Active().Version != item.Current || manager.SettingsSnapshot().Values["sidebar_enabled"] != false {
					t.Fatal("upgrade did not preserve the active theme and customized setting")
				}
				settings = manager.SettingsSnapshot().Values
				settings["sidebar_enabled"] = true
				if _, fields, err := manager.SaveSettings(settings); err != nil || len(fields) > 0 {
					t.Fatal(fields, err)
				}
				state, err := manager.VersionState(item.Slug)
				if err != nil {
					t.Fatal(err)
				}
				if err := manager.Rollback(t.Context(), item.Slug, item.Current, state.Revision, item.Previous, nil); err != nil {
					t.Fatal(err)
				}
				if manager.SettingsSnapshot().Values["sidebar_enabled"] != true {
					t.Fatal("rollback lost the setting edited after upgrade")
				}
			}
			verifyFiles(t, manager, previous)
			if manager.Active().Version != item.Previous || manager.AssetURL(item.Asset) != oldURL {
				t.Fatal("rollback did not restore the release and asset URL")
			}
			reloaded := NewManager(manager.root, item.Slug, WithSettingsDir(manager.settingsDir))
			if err := reloaded.Recover(); err != nil {
				t.Fatal(err)
			}
			if err := reloaded.Load(); err != nil || reloaded.Active().Version != item.Previous || reloaded.AssetURL(item.Asset) != oldURL {
				t.Fatal("restart did not preserve the installed release", err)
			}
			verifyFiles(t, reloaded, previous)
			results = append(results, map[string]any{"theme": item.Slug, "previous": item.Previous, "current": item.Current,
				"all_archive_files_match": true, "installed_and_reloaded": true, "upgrade_and_rollback": item.Previous != item.Current})
		})
	}
	if t.Failed() {
		return
	}
	encoded, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "theme-release-rollback.json"), append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
