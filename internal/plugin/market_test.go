package plugin

import (
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
	"newpress.local/newpress/internal/extension"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type marketTransport func(*http.Request) (*http.Response, error)

func (f marketTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func signCatalog(t *testing.T, c Catalog, key ed25519.PrivateKey) []byte {
	t.Helper()
	p, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(SignedCatalog{base64.StdEncoding.EncodeToString(p), base64.StdEncoding.EncodeToString(ed25519.Sign(key, p))})
	return b
}

func TestSignedMarketInstallTrustIntegrityAndDisabledDefault(t *testing.T) {
	public, key, _ := ed25519.GenerateKey(rand.Reader)
	m := NewManager(t.TempDir())
	market, err := NewMarket(m, MarketSource{[]string{"https://raw.githubusercontent.com/official/plugins/main/catalog.json"}, base64.StdEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Schema: 1, Slug: "official-test", Name: "Official test", Version: "1.0.0", Runtime: "wasm/core-v1", NewPress: ">=0.1.0", Entry: "plugin.wasm", Category: "tools", Tags: []string{"维护", "工具"}}
	archive := createPluginArchive(t, manifest, abiV1Module())
	b, _ := os.ReadFile(archive)
	digest := sha256.Sum256(b)
	c := Catalog{Schema: 2, Sequence: 1, Expires: time.Now().Add(time.Hour), Plugins: []Release{{Release: &extension.Metadata{Channel: "stable", PublishedAt: time.Now().UTC(), DataCompatible: true}, Manifest: manifest, Path: "packages/test.zip", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(b))}}}
	signed := signCatalog(t, c, key)
	market.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
		body := signed
		if strings.HasSuffix(r.URL.Path, ".zip") {
			body = b
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	if err = market.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = market.Install(t.Context(), manifest.Slug, "wrong digest"); err == nil {
		t.Fatal("stale catalog selection accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	err = market.InstallWithProgress(ctx, manifest.Slug, c.Plugins[0].SHA256, func(phase string, _, _ int64) {
		if phase == "switching" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled install committed", err)
	}
	if _, err := os.Stat(filepath.Join(m.root, manifest.Slug)); !os.IsNotExist(err) {
		t.Fatal("cancelled package left installed")
	}
	if err = market.Install(t.Context(), manifest.Slug, c.Plugins[0].SHA256); err != nil {
		t.Fatal(err)
	}
	items, err := m.List()
	if err != nil || len(items) != 1 || items[0].Enabled || !items[0].Official {
		t.Fatalf("installed=%+v err=%v", items, err)
	}
	if !reflectManifest(items[0].Manifest, manifest) {
		t.Fatalf("signed discovery metadata changed during installation: %+v", items[0].Manifest)
	}
	if err = m.Uninstall(manifest.Slug); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 1
	if err = market.Install(t.Context(), manifest.Slug, c.Plugins[0].SHA256); err == nil {
		t.Fatal("modified archive accepted")
	}
	if _, err = os.Stat(filepath.Join(m.root, manifest.Slug)); !os.IsNotExist(err) {
		t.Fatal("corrupt package left installed")
	}
	c.Sequence = 0
	signed = signCatalog(t, c, key)
	if err = market.Refresh(t.Context()); err == nil {
		t.Fatal("rollback accepted")
	}
}

func TestMarketRejectsUntrustedSourcesSignaturesAndExpiry(t *testing.T) {
	public, key, _ := ed25519.GenerateKey(rand.Reader)
	source := MarketSource{[]string{"https://gitee.com/official/plugins/raw/main/catalog.json"}, base64.StdEncoding.EncodeToString(public)}
	market, err := NewMarket(NewManager(t.TempDir()), source)
	if err != nil {
		t.Fatal(err)
	}
	c := Catalog{Schema: 2, Sequence: 1, Expires: time.Now().Add(time.Hour)}
	if _, err = market.verify(signCatalog(t, c, key)); err != nil {
		t.Fatal(err)
	}
	c.Schema = 1
	if _, err = market.verify(signCatalog(t, c, key)); err == nil {
		t.Fatal("retired catalog schema accepted")
	}
	c.Schema = 2
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if _, err = market.verify(signCatalog(t, c, other)); err == nil {
		t.Fatal("wrong signature accepted")
	}
	c.Expires = time.Now().Add(-time.Minute)
	if _, err = market.verify(signCatalog(t, c, key)); err == nil {
		t.Fatal("expired catalog accepted")
	}
	for _, raw := range []string{"http://gitee.com/a/b/raw/main/catalog.json", "https://evil.test/a/b/main/catalog.json", "https://raw.githubusercontent.com/a/b/main/../catalog.json", "https://gitee.com/a/b/raw/main/catalog.json?token=secret"} {
		if _, err = repositoryURL(raw); err == nil {
			t.Fatalf("accepted source %s", raw)
		}
	}
}

func TestBuiltinLifecycleCleanUninstallAndRecovery(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	if err := m.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	list, _ := m.List()
	if len(list) != 1 || list[0].Enabled || !list[0].Official || list[0].Error != "" {
		t.Fatalf("builtin=%+v", list)
	}
	if err := m.SetEnabled(CommentOrigin, true); err != nil {
		t.Fatal(err)
	}
	var generation uint64
	if err := m.WithData(CommentOrigin, true, 0, func(dir string, g uint64) error {
		generation = g
		return WritePrivate(filepath.Join(dir, "secret.json"), map[string]string{"key": "sensitive"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(CommentOrigin); err != nil {
		t.Fatal(err)
	}
	if err := m.WithData(CommentOrigin, true, generation, func(string, uint64) error { t.Fatal("uninstalled callback invoked"); return nil }); err == nil {
		t.Fatal("deleted namespace accessible")
	}
	if _, err := os.Stat(filepath.Join(root, CommentOrigin)); !os.IsNotExist(err) {
		t.Fatal("plugin directory remains")
	}
	tomb := filepath.Join(root, ".removed-old")
	os.Mkdir(tomb, 0o700)
	os.WriteFile(filepath.Join(tomb, "private"), []byte("secret"), 0o600)
	if err := m.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tomb); !os.IsNotExist(err) {
		t.Fatal("interrupted cleanup remains")
	}
	m = NewManager(root)
	if list, _ = m.List(); len(list) != 0 {
		t.Fatal("startup reinstalled builtin")
	}
}
