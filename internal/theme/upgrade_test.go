package theme

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
)

const otaSettings = `{"type":"object","properties":{"headline":{"type":"string","default":"default"}}}`

func themeOTAArchive(t *testing.T, manifest Manifest, schema, home string) (Release, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	b, _ := json.Marshal(manifest)
	entries := map[string]string{
		"theme.json":            string(b),
		"templates/base.html":   `{{define "base"}}{{template "content" .}}{{end}}`,
		"templates/home.html":   home,
		"templates/single.html": `{{define "content"}}single{{end}}`,
		"templates/page.html":   `{{define "content"}}page{{end}}`,
		"settings.schema.json":  schema,
		"assets/style.css":      "/* version " + manifest.Version + " */",
	}
	for name, text := range entries {
		f, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, text); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "theme.zip")
	if err := os.WriteFile(filename, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buffer.Bytes())
	return Release{Manifest: manifest, Path: "packages/theme.zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(buffer.Len()), Release: &extension.Metadata{Channel: "stable", PublishedAt: time.Now().UTC(), DataCompatible: true}}, filename
}

func themeOTAFixture(t *testing.T) (*Manager, Manifest) {
	t.Helper()
	manifest := Manifest{Schema: 1, Slug: "sample-theme", Name: "Sample", Version: "1.0.0", Engine: "go-template/v1", View: "public/v1", NewPress: ">=0.1.0", Templates: map[string]string{"home": "home.html", "single": "single.html", "page": "page.html"}, SettingsSchema: "settings.schema.json"}
	m := NewManager(t.TempDir(), manifest.Slug, WithSettingsDir(t.TempDir()))
	_, archive := themeOTAArchive(t, manifest, otaSettings, `{{define "content"}}old{{end}}`)
	if _, err := m.InstallPackage(archive); err != nil {
		t.Fatal(err)
	}
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if _, fields, err := m.SaveSettings(map[string]any{"headline": "自定义外观"}); err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
	return m, manifest
}

func TestThemeOTAHotReloadAndRollbackPreserveCurrentSettings(t *testing.T) {
	m, manifest := themeOTAFixture(t)
	oldAsset := m.AssetURL("style.css")
	oldRevision := m.assetRevision
	target, err := m.CaptureUpdate(manifest.Slug, "", "")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(settingsFilePath(m.settingsDir, manifest.Slug))
	manifest.Version = "1.1.0"
	release, archive := themeOTAArchive(t, manifest, otaSettings, `{{define "content"}}updated{{end}}`)
	if err := m.UpdatePackage(t.Context(), archive, release, target, nil); err != nil {
		t.Fatal(err)
	}
	if oldAsset == m.AssetURL("style.css") {
		t.Fatal("OTA did not change asset identity")
	}
	if _, _, err := m.ReadVersionedAsset(manifest.Slug, oldRevision, "style.css"); err == nil {
		t.Fatal("old asset identity served new OTA bytes")
	}
	var rendered bytes.Buffer
	if err := m.Render(&rendered, "home", nil); err != nil || rendered.String() != "updated" || m.Active().Version != "1.1.0" {
		t.Fatal("active templates did not reload", rendered.String(), err)
	}
	file, _, assetVersion, err := m.OpenAsset("style.css")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	file.Close()
	if err != nil || assetVersion != "1.1.0" || !strings.Contains(string(data), "1.1.0") {
		t.Fatal("asset version mismatch", assetVersion, err)
	}
	after, _ := os.ReadFile(settingsFilePath(m.settingsDir, manifest.Slug))
	if !bytes.Equal(before, after) || m.SettingsSnapshot().Values["headline"] != "自定义外观" {
		t.Fatal("update rewrote settings")
	}
	if _, fields, err := m.SaveSettings(map[string]any{"headline": "更新后的新设置"}); err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
	state, _ := m.VersionState(manifest.Slug)
	if err := m.Rollback(t.Context(), manifest.Slug, "1.1.0", state.Revision, "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	rendered.Reset()
	if err := m.Render(&rendered, "home", nil); err != nil || rendered.String() != "old" {
		t.Fatal("rollback did not reload old templates", err)
	}
	if m.SettingsSnapshot().Values["headline"] != "更新后的新设置" {
		t.Fatal("rollback lost live settings")
	}
	if m.AssetURL("style.css") != oldAsset {
		t.Fatal("rollback did not restore original asset identity")
	}
	if data, _, err := m.ReadVersionedAsset(manifest.Slug, oldRevision, "style.css"); err != nil || !strings.Contains(string(data), "1.0.0") {
		t.Fatal("rollback asset snapshot", err)
	}
	reloaded := NewManager(m.root, manifest.Slug, WithSettingsDir(m.settingsDir))
	if err := reloaded.Recover(); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Load(); err != nil || reloaded.Active().Version != "1.0.0" {
		t.Fatal("restart lost rollback", err)
	}
	state, _ = reloaded.VersionState(manifest.Slug)
	if len(state.History) != 2 || state.Previous != "1.1.0" {
		t.Fatal(state)
	}
}

func TestThemeOTAFailuresDoNotChangeRunningTheme(t *testing.T) {
	for _, failure := range []string{"template", "settings", "missing-page", "cancel-at-switch", "incompatible-core", "not-data-compatible", "manifest-mismatch"} {
		t.Run(failure, func(t *testing.T) {
			m, manifest := themeOTAFixture(t)
			target, _ := m.CaptureUpdate(manifest.Slug, "", "")
			manifest.Version = "1.1.0"
			schema, home := otaSettings, `{{define "content"}}new{{end}}`
			switch failure {
			case "template":
				home = "{{"
			case "settings":
				schema = `{"type":"object","properties":{"headline":{"type":"integer","default":1}}}`
			case "missing-page":
				delete(manifest.Templates, "page")
			case "incompatible-core":
				manifest.NewPress = ">=99.0.0"
			}
			release, archive := themeOTAArchive(t, manifest, schema, home)
			if failure == "not-data-compatible" {
				release.Release.DataCompatible = false
			}
			if failure == "manifest-mismatch" {
				release.Manifest.Name = "Different"
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := m.UpdatePackage(ctx, archive, release, target, func(phase string, _, _ int64) {
				if failure == "cancel-at-switch" && phase == "switching" {
					cancel()
				}
			})
			if err == nil {
				t.Fatal("unsafe theme update accepted")
			}
			var rendered bytes.Buffer
			if err := m.Render(&rendered, "home", nil); err != nil || rendered.String() != "old" || m.Active().Version != "1.0.0" {
				t.Fatal("failed update changed active theme", err)
			}
			state, _ := m.VersionState(manifest.Slug)
			if state.Revision != target.state.Revision || state.Previous != "" || m.SettingsSnapshot().Values["headline"] != "自定义外观" {
				t.Fatal("failed update changed version/settings", state)
			}
		})
	}
}

func TestThemeOTAAssetsCanBeOpenedDuringSwitch(t *testing.T) {
	m, manifest := themeOTAFixture(t)
	target, _ := m.CaptureUpdate(manifest.Slug, "", "")
	manifest.Version = "1.1.0"
	release, archive := themeOTAArchive(t, manifest, otaSettings, `{{define "content"}}new{{end}}`)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	failures := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			f, _, _, err := m.OpenAsset("style.css")
			if err != nil {
				select {
				case failures <- err:
				default:
				}
				return
			}
			f.Close()
		}
	}()
	err := m.UpdatePackage(t.Context(), archive, release, target, nil)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-failures:
		t.Fatal("asset failed during switch", err)
	default:
	}
}
