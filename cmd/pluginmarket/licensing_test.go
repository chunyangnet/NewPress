package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/licensing"
	"newpress.local/newpress/internal/plugin"
)

func TestGeneratedCommentOriginHasCompleteLicensesAndNewVersion(t *testing.T) {
	manifest, payload, err := commentOriginPackage()
	if err != nil || manifest.Version != "1.0.1" {
		t.Fatal("license correction needs a new version", manifest, err)
	}
	_, repeated, err := commentOriginPackage()
	if err != nil || !bytes.Equal(payload, repeated) {
		t.Fatal("generated package bytes are not deterministic", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]string{"LICENSE": licensing.Apache20, "THIRD_PARTY_NOTICES.md": licensing.HostPluginNotices, "flag-icons-MIT.txt": licensing.FlagIconsMIT}
	for _, file := range archive.File {
		want, ok := wanted[file.Name]
		if !ok {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		reader.Close()
		if err != nil || string(got) != want {
			t.Fatalf("incomplete %s: %v", file.Name, err)
		}
		delete(wanted, file.Name)
	}
	if len(wanted) != 0 {
		t.Fatal("missing license notices", wanted)
	}
	filename := filepath.Join(t.TempDir(), "comment-origin-1.0.1.zip")
	if err := os.WriteFile(filename, payload, 0600); err != nil {
		t.Fatal(err)
	}
	manager := plugin.NewManager(t.TempDir())
	if got, err := manager.InstallPackage(filename); err != nil || !reflect.DeepEqual(got, manifest) {
		t.Fatal("corrected archive cannot be installed", err)
	}
}

func licensingPublisherKey(t *testing.T, root string) (string, ed25519.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, "test-only.key")
	if err := os.WriteFile(filename, []byte(base64.StdEncoding.EncodeToString(private)), 0600); err != nil {
		t.Fatal(err)
	}
	return filename, public
}

func readLicensedCatalog(t *testing.T, filename string, public ed25519.PublicKey) plugin.Catalog {
	t.Helper()
	envelope, err := extension.ReadJSON[plugin.SignedCatalog](filename, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil || !ed25519.Verify(public, payload, signature) {
		t.Fatal("license publication has an invalid signature")
	}
	var catalog plugin.Catalog
	if err := json.Unmarshal(payload, &catalog); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestPublisherRejectsRetiredCatalogSchema(t *testing.T) {
	root := t.TempDir()
	key, _ := licensingPublisherKey(t, root)
	if err := publishForTest(t, "-key", key, "-out", filepath.Join(root, "release"), "-schema", "1", "-sequence", "6", "-expires", time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); err == nil {
		t.Fatal("retired catalog schema accepted")
	}
}

func TestPublisherAllowsCDNInNewCatalog(t *testing.T) {
	root := t.TempDir()
	key, public := licensingPublisherKey(t, root)
	cdn := plugin.CDNManifest()
	input := publisherHostPackage(t, cdn, cdn.Runtime)
	out := filepath.Join(root, "release")
	if err := publishForTest(t, "-key", key, "-out", out, "-schema", "2", "-sequence", "6", "-expires", time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "-packages", input); err != nil {
		t.Fatal(err)
	}
	catalog := readLicensedCatalog(t, filepath.Join(out, "catalog.extensions-v5.json"), public)
	if len(catalog.Plugins) != 2 || !reflect.DeepEqual(catalog.Plugins[1].Manifest, cdn) {
		t.Fatal("new catalog must support the current CDN package without changing its version")
	}
}
