package main

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

func publisherTheme(t *testing.T, value string) string {
	t.Helper()
	manifest := theme.Manifest{Schema: 1, Slug: "publish-theme", Name: "Theme", Version: value, Engine: "go-template/v1", View: "public/v1", NewPress: ">=0.1.0", Templates: map[string]string{"home": "home.html", "single": "single.html"}}
	b, _ := json.Marshal(manifest)
	name := filepath.Join(t.TempDir(), "theme.zip")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, text := range map[string]string{"theme.json": string(b), "templates/base.html": `{{define "base"}}{{template "content" .}}{{end}}`, "templates/home.html": `{{define "content"}}home{{end}}`, "templates/single.html": `{{define "content"}}single{{end}}`} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, text); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

func publishForTest(t *testing.T, args ...string) error {
	t.Helper()
	previousFlags, previousArgs := flag.CommandLine, os.Args
	defer func() { flag.CommandLine, os.Args = previousFlags, previousArgs }()
	flag.CommandLine = flag.NewFlagSet("pluginmarket-test", flag.ContinueOnError)
	os.Args = append([]string{"pluginmarket-test"}, args...)
	return run()
}

func TestPublisherBuildsSignedMultiversionThemeCatalog(t *testing.T) {
	root := t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "test-only.key")
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(private)), 0600); err != nil {
		t.Fatal(err)
	}
	first, next := publisherTheme(t, "1.0.0"), publisherTheme(t, "1.1.0-rc.1")
	metadata := filepath.Join(root, "metadata.json")
	if err := extension.WriteJSON(metadata, map[string]releaseDetails{
		"theme/publish-theme@1.1.0-rc.1": {Release: extension.Metadata{Channel: "preview", PublishedAt: time.Now().UTC(), DataCompatible: true}, Notes: "预览版发行说明"},
	}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "release")
	if err := publishForTest(t, "-key", key, "-out", out, "-schema", "2", "-sequence", "12", "-expires", time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "-themes", first+","+next, "-data-compatible", "-metadata", metadata); err != nil {
		t.Fatal(err)
	}
	envelope, err := extension.ReadJSON[plugin.SignedCatalog](filepath.Join(out, "catalog.extensions-v5.json"), 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil || !ed25519.Verify(public, payload, signature) {
		t.Fatal("publisher signature cannot be verified")
	}
	var catalog plugin.Catalog
	if err := json.Unmarshal(payload, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Schema != 2 || catalog.Sequence != 12 || len(catalog.Themes) != 2 || len(catalog.Plugins) != 1 {
		t.Fatal(catalog)
	}
	for _, release := range catalog.Themes {
		if err := release.Release.Validate(release.Manifest.Version); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(release.Path)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if release.Size != int64(len(data)) || release.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("release bytes disagree with signed catalog")
		}
		if !strings.HasPrefix(release.Path, "packages/themes/") {
			t.Fatal("theme and plugin package namespaces overlap")
		}
		if release.Manifest.Version == "1.1.0-rc.1" && (release.Release.Channel != "preview" || release.Notes != "预览版发行说明") {
			t.Fatal("metadata override lost")
		}
		manager := theme.NewManager(t.TempDir(), release.Manifest.Slug)
		if _, err := manager.InstallPackage(filepath.Join(out, filepath.FromSlash(release.Path))); err != nil {
			t.Fatal(err)
		}
		if err := manager.Load(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "test-only.key")); !os.IsNotExist(err) {
		t.Fatal("private key was copied to output")
	}
	if err := publishForTest(t, "-schema", "2", "-catalog-name", "catalog.runtime-v1.json", "-key", key); err == nil {
		t.Fatal("legacy catalog filename accepted")
	}
}

func TestPublisherRejectsAmbiguousOrUnmatchedReleaseMetadata(t *testing.T) {
	one := theme.Release{Manifest: theme.Manifest{Slug: "example", Version: "1.0.0", NewPress: ">=0.1.0"}}
	duplicate := one
	duplicate.Manifest.Version = "1.0.0+rebuild"
	catalog := plugin.Catalog{Themes: []theme.Release{one, duplicate}}
	if err := decorateReleases(&catalog, "stable", true, "", ""); err == nil {
		t.Fatal("equivalent versions accepted")
	}
	one.Manifest.NewPress = "builtin/skip-validation"
	catalog.Themes = []theme.Release{one}
	if err := decorateReleases(&catalog, "stable", true, "", ""); err == nil {
		t.Fatal("theme bypassed compatibility range validation")
	}
	metadata := filepath.Join(t.TempDir(), "unmatched.json")
	if err := extension.WriteJSON(metadata, map[string]releaseDetails{"theme/missing@1.0.0": {}}); err != nil {
		t.Fatal(err)
	}
	catalog = plugin.Catalog{}
	if err := decorateReleases(&catalog, "stable", true, "", metadata); err == nil {
		t.Fatal("unmatched metadata accepted")
	}
}
