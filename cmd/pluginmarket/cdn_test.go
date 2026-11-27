package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/plugin"
)

func TestPublisherCDNAcceptsSafeCatalogName(t *testing.T) {
	root := t.TempDir()
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	key := filepath.Join(root, "key")
	if e = os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(priv)), 0600); e != nil {
		t.Fatal(e)
	}
	manifest := plugin.CDNManifest()
	input := publisherHostPackage(t, manifest, manifest.Runtime)
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if e = publishForTest(t, "-key", key, "-out", filepath.Join(root, "bad"), "-schema", "2", "-no-bundled", "-catalog-name", "catalog.extensions-v2.json", "-sequence", "4", "-expires", expiry, "-packages", input); e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(root, "good")
	if e = publishForTest(t, "-key", key, "-out", out, "-schema", "2", "-no-bundled", "-catalog-name", "catalog.extensions-v4.json", "-sequence", "5", "-expires", expiry, "-packages", input); e != nil {
		t.Fatal(e)
	}
	envelope, e := extension.ReadJSON[plugin.SignedCatalog](filepath.Join(out, "catalog.extensions-v4.json"), 2<<20)
	if e != nil {
		t.Fatal(e)
	}
	payload, _ := base64.StdEncoding.DecodeString(envelope.Payload)
	sig, _ := base64.StdEncoding.DecodeString(envelope.Signature)
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatal("catalog signature invalid")
	}
	m := plugin.NewManager(t.TempDir())
	path := filepath.Join(out, "packages", "cdn", manifest.Version, "cdn-"+manifest.Version+".zip")
	if _, e = m.InstallPackage(path); e != nil {
		t.Fatal(e)
	}
	if e = m.SetEnabled(plugin.CDN, true); e != nil {
		t.Fatal(e)
	}
	if e = m.Uninstall(plugin.CDN); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(out, "catalog.extensions-v2.json")); !os.IsNotExist(e) {
		t.Fatal("legacy catalog was modified")
	}
}
