package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/plugin"
)

func publisherHostPackage(t *testing.T, manifest plugin.Manifest, runtime string) string {
	t.Helper()
	var body bytes.Buffer
	archive := zip.NewWriter(&body)
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := json.Marshal(map[string]string{"runtime": runtime})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"plugin.json": manifestJSON, "builtin.json": descriptor} {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "object-storage.zip")
	if err := os.WriteFile(filename, body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestPublisherSignsAndInstallsRegisteredObjectStorage(t *testing.T) {
	root := t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "test-only.key")
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(private)), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := plugin.ObjectStorageManifest()
	input := publisherHostPackage(t, manifest, manifest.Runtime)
	out := filepath.Join(root, "release")
	if err := publishForTest(t, "-key", key, "-out", out, "-schema", "2", "-sequence", "3", "-expires", time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "-packages", input); err != nil {
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
		t.Fatal("invalid catalog signature")
	}
	var catalog plugin.Catalog
	if err := json.Unmarshal(payload, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Schema != 2 || catalog.Sequence != 3 || len(catalog.Plugins) != 2 {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	release := catalog.Plugins[1]
	if !reflect.DeepEqual(release.Manifest, manifest) || release.Release == nil || release.Release.DataCompatible || !strings.Contains(release.Notes, "主程序") {
		t.Fatalf("incorrect host release: %+v", release)
	}
	filename := filepath.Join(out, filepath.FromSlash(release.Path))
	actual, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(actual)
	if !bytes.Equal(actual, expected) || release.Size != int64(len(actual)) || release.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("published bytes disagree with reviewed package or signed catalog")
	}
	manager := plugin.NewManager(t.TempDir())
	if installed, err := manager.InstallPackage(filename); err != nil || !reflect.DeepEqual(installed, manifest) {
		t.Fatal("host package failed installation", err)
	}
	items, err := manager.List()
	if err != nil || len(items) != 1 || items[0].Enabled {
		t.Fatal("host plugin must install disabled", err)
	}
	if err := manager.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(manifest.Slug); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"test-only.key", "catalog.json", "catalog.runtime-v1.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected published file %s", name)
		}
	}
}

func TestPublisherRejectsUnregisteredOrLegacyHostPackages(t *testing.T) {
	root := t.TempDir()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "test-only.key")
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(private)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"legacy", "altered-manifest", "wrong-descriptor"} {
		t.Run(name, func(t *testing.T) {
			manifest := plugin.ObjectStorageManifest()
			schema, runtime := "2", manifest.Runtime
			switch name {
			case "legacy":
				schema = "1"
			case "altered-manifest":
				manifest.Description = "unregistered host code"
			case "wrong-descriptor":
				runtime = "builtin/unregistered-v1"
			}
			input := publisherHostPackage(t, manifest, runtime)
			out := filepath.Join(t.TempDir(), "release")
			if err := publishForTest(t, "-key", key, "-out", out, "-schema", schema, "-sequence", "3", "-expires", time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "-packages", input); err == nil {
				t.Fatal("unsafe host package was signed")
			}
			for _, filename := range []string{"catalog.json", "catalog.extensions-v2.json", "catalog.extensions-v5.json"} {
				if _, err := os.Stat(filepath.Join(out, filename)); !os.IsNotExist(err) {
					t.Fatal("failed package left a signed catalog")
				}
			}
		})
	}
	for _, manifest := range plugin.BundledManifests() {
		manifest.Description = "unregistered host code"
		catalog := plugin.Catalog{Plugins: []plugin.Release{{Manifest: manifest}}}
		if err := decorateReleases(&catalog, "stable", false, "", ""); err == nil {
			t.Fatal("unregistered manifest bypassed compatibility validation")
		}
	}
}
