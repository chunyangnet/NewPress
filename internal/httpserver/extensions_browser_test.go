package httpserver

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/extensionupdate"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

// Opt-in local browser fixture. All installs, settings and credentials belong to
// temporary directories; the checked-in theme and the user's data are never
// updated. Signature/download tests use the real market in internal/plugin.
func TestExtensionOTABrowserFixture(t *testing.T) {
	artifacts := os.Getenv("NEWPRESS_OTA_BROWSER_FIXTURE")
	if artifacts == "" {
		t.Skip("set NEWPRESS_OTA_BROWSER_FIXTURE to an output directory for browser QA")
	}
	artifacts, err := filepath.Abs(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	themeRoot, settingsRoot := t.TempDir(), t.TempDir()
	source := filepath.Join("..", "..", "themes", "chunnel")
	if err := extension.CopyTree(t.Context(), source, filepath.Join(themeRoot, "chunnel")); err != nil {
		t.Fatal(err)
	}
	s.themes = theme.NewManager(themeRoot, "chunnel", theme.WithSettingsDir(settingsRoot))
	if err := s.themes.Load(); err != nil {
		t.Fatal(err)
	}
	settings := s.themes.SettingsSnapshot().Values
	settings["accent"] = "#2e6d87"
	if _, fields, err := s.themes.SaveSettings(settings); err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
	next := s.themes.Active()
	var major, minor, patch int
	if _, err := fmt.Sscanf(next.Version, "%d.%d.%d", &major, &minor, &patch); err != nil {
		t.Fatal(err)
	}
	next.Version = fmt.Sprintf("%d.%d.%d", major, minor, patch+1)
	manifestBytes, _ := json.Marshal(next)
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	if err := filepath.WalkDir(source, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if relative == "theme.json" {
			data = manifestBytes
		}
		if filepath.ToSlash(relative) == "assets/css/chunnel-extension-updates.css" {
			data = append(data, []byte("\n/* OTA browser fixture "+next.Version+" */\n")...)
		}
		f, err := writer.Create(filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "chunnel.zip")
	if err := os.WriteFile(archive, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buffer.Bytes())
	release := theme.Release{Manifest: next, Path: "packages/themes/chunnel-" + next.Version + ".zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(buffer.Len()), Notes: "主题更新验收版\n保留当前品牌颜色与界面样式，更新完成后立即使用新模板和资源。", Release: &extension.Metadata{Channel: "stable", PublishedAt: time.Now().UTC(), DataCompatible: true}}
	market := &httpExtensionMarket{view: plugin.MarketView{Configured: true, Schema: 2, Sequence: 42, Checked: time.Now().UTC(), Expires: time.Now().Add(time.Hour), Themes: []theme.Release{release}, Sources: []plugin.MarketSourceStatus{{Name: "验收目录", Available: true, Selected: true}}}}
	market.check = func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
			return nil
		}
	}
	market.updateTheme = func(ctx context.Context, tm *theme.Manager, slug, digest, expected, revision string, p extension.Progress) error {
		target, err := tm.CaptureUpdate(slug, expected, revision)
		if err != nil {
			return err
		}
		if digest != release.SHA256 {
			return extension.ErrConflict
		}
		// Read the real validation ZIP in chunks so progress is measured in bytes.
		reader := bytes.NewReader(buffer.Bytes())
		chunk := make([]byte, 128<<10)
		var received int64
		for reader.Len() > 0 {
			n, err := reader.Read(chunk)
			if err != nil {
				return err
			}
			received += int64(n)
			p.Report("downloading", received, release.Size)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(120 * time.Millisecond):
			}
		}
		p.Report("verifying", received, release.Size)
		return tm.UpdatePackage(ctx, archive, release, target, p)
	}
	service, err := extensionupdate.New(filepath.Join(t.TempDir(), "jobs.json"), market, s.plugins, s.themes)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	s.SetExtensionUpdates(service)
	s.config.ThemesDir = themeRoot
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/appearance/updates/", http.StatusSeeOther)
	})
	mux.Handle("/", handler)
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	if err := extension.WriteJSON(filepath.Join(artifacts, "fixture.json"), map[string]string{"url": host.URL + "/fixture/login"}); err != nil {
		t.Fatal(err)
	}
	t.Log("isolated browser fixture ready", host.URL)
	deadline := time.NewTimer(15 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("browser fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(artifacts, "stop")); err == nil {
				if s.themes.SettingsSnapshot().Values["accent"] != "#2e6d87" {
					t.Fatal("browser OTA lost saved accent")
				}
				return
			}
		}
	}
}
