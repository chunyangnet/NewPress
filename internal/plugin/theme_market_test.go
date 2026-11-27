package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/theme"
)

func marketTheme(t *testing.T, value string) (theme.Release, []byte) {
	t.Helper()
	manifest := theme.Manifest{Schema: 1, Slug: "market-theme", Name: "Market theme", Version: value, Engine: "go-template/v1", View: "public/v1", NewPress: ">=0.1.0", Templates: map[string]string{"home": "home.html", "single": "single.html"}, SettingsSchema: "settings.schema.json"}
	raw, _ := json.Marshal(manifest)
	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	for name, data := range map[string]string{
		"theme.json":            string(raw),
		"templates/base.html":   `{{define "base"}}{{template "content" .}}{{end}}`,
		"templates/home.html":   `{{define "content"}}` + value + `{{end}}`,
		"templates/single.html": `{{define "content"}}single{{end}}`,
		"settings.schema.json":  `{"type":"object","properties":{"headline":{"type":"string","default":"default"}}}`,
	} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	b := buffer.Bytes()
	sum := sha256.Sum256(b)
	return theme.Release{Manifest: manifest, Path: "packages/themes/market-theme-" + value + ".zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b)), Notes: "Signed theme release", Release: &extension.Metadata{Channel: "stable", PublishedAt: time.Now().UTC(), DataCompatible: true}}, b
}

func TestSignedThemeInstallUpdateRollbackAndCancelledInstall(t *testing.T) {
	first, firstBytes := marketTheme(t, "1.0.0")
	next, nextBytes := marketTheme(t, "1.1.0")
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	market, err := NewMarket(NewManager(t.TempDir()), MarketSource{[]string{githubCatalog}, base64.StdEncoding.EncodeToString(pub)})
	if err != nil {
		t.Fatal(err)
	}
	catalog := signCatalog(t, Catalog{Schema: 2, Sequence: 1, Expires: time.Now().Add(time.Hour), Themes: []theme.Release{first, next}}, key)
	market.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
		data := catalog
		if strings.HasSuffix(r.URL.Path, first.Path) {
			data = firstBytes
		}
		if strings.HasSuffix(r.URL.Path, next.Path) {
			data = nextBytes
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}
	if err := market.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if view := market.View(); len(view.Themes) != 2 || view.Themes[0].Manifest.Version != "1.1.0" {
		t.Fatal(view)
	}
	root := t.TempDir()
	themes := theme.NewManager(root, first.Manifest.Slug, theme.WithSettingsDir(t.TempDir()))
	ctx, cancel := context.WithCancel(t.Context())
	err = market.InstallTheme(ctx, themes, first.Manifest.Slug, first.SHA256, func(phase string, _, _ int64) {
		if phase == "switching" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, first.Manifest.Slug)); !os.IsNotExist(err) {
		t.Fatal("cancelled installation left a theme")
	}
	if err := market.InstallTheme(t.Context(), themes, first.Manifest.Slug, first.SHA256, nil); err != nil {
		t.Fatal(err)
	}
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	if _, fields, err := themes.SaveSettings(map[string]any{"headline": "keep this value"}); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	state, _ := themes.VersionState(first.Manifest.Slug)
	var received int64
	if err := market.UpdateTheme(t.Context(), themes, first.Manifest.Slug, next.SHA256, first.Manifest.Version, state.Revision, func(phase string, n, total int64) {
		if phase == "downloading" {
			received = n
			if total != next.Size {
				t.Error("wrong byte total")
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if themes.Active().Version != "1.1.0" || received != next.Size {
		t.Fatal("theme update did not commit")
	}
	var page bytes.Buffer
	if err := themes.Render(&page, "home", nil); err != nil || page.String() != "1.1.0" {
		t.Fatal(page.String(), err)
	}
	state, _ = themes.VersionState(first.Manifest.Slug)
	if err := themes.Rollback(t.Context(), first.Manifest.Slug, "1.1.0", state.Revision, "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if themes.Active().Version != "1.0.0" {
		t.Fatal("rollback did not restore theme")
	}
	if settings := themes.SettingsSnapshot().Values; settings["headline"] != "keep this value" {
		t.Fatal("theme settings lost", settings)
	}
}

func TestSchemaTwoRejectsAmbiguousVersionsAndInvalidReleaseMetadata(t *testing.T) {
	valid, _ := marketTheme(t, "1.0.0")
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	market, err := NewMarket(NewManager(t.TempDir()), MarketSource{[]string{githubCatalog}, base64.StdEncoding.EncodeToString(pub)})
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"equivalent-version", "missing-metadata", "stable-prerelease", "invalid-range", "future-release", "legacy-themes"} {
		t.Run(failure, func(t *testing.T) {
			b, _ := json.Marshal(valid)
			var release theme.Release
			if err := json.Unmarshal(b, &release); err != nil {
				t.Fatal(err)
			}
			catalog := Catalog{Schema: 2, Sequence: 1, Expires: time.Now().Add(time.Hour), Themes: []theme.Release{release}}
			switch failure {
			case "equivalent-version":
				release.Manifest.Version = "v1.0.0+different"
				catalog.Themes = append(catalog.Themes, release)
			case "missing-metadata":
				catalog.Themes[0].Release = nil
			case "stable-prerelease":
				catalog.Themes[0].Manifest.Version = "1.1.0-rc.1"
			case "invalid-range":
				catalog.Themes[0].Manifest.NewPress = "builtin/anything"
			case "future-release":
				catalog.Themes[0].Release.PublishedAt = time.Now().Add(time.Hour)
			case "legacy-themes":
				catalog.Schema = 1
			}
			if _, err := market.verify(signCatalog(t, catalog, key)); err == nil {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
}
