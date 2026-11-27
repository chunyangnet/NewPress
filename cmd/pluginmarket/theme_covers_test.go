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
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

func TestPublisherThemeOnlyExportsSignedCover(t *testing.T) {
	root := t.TempDir()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	key := filepath.Join(root, "test.key")
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(private)), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := theme.Manifest{Schema: 1, Slug: "covered", Name: "Covered", Version: "1.0.0", Engine: "go-template/v1", View: "public/v1", Templates: map[string]string{"home": "home.html", "single": "single.html"}, Cover: "assets/preview.png"}
	raw, _ := json.Marshal(manifest)
	var cover, archive bytes.Buffer
	if err := png.Encode(&cover, image.NewRGBA(image.Rect(0, 0, 96, 60))); err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(&archive)
	for name, data := range map[string][]byte{"theme.json": raw, "templates/base.html": []byte(`{{define "base"}}{{template "content" .}}{{end}}`), "templates/home.html": []byte(`{{define "content"}}home{{end}}`), "templates/single.html": []byte(`{{define "content"}}single{{end}}`), manifest.Cover: cover.Bytes()} {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, "theme.zip")
	if err := os.WriteFile(filename, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "release")
	if err := publishForTest(t, "-key", key, "-out", out, "-schema", "2", "-themes-only", "-catalog-name", "catalog.themes-v1.json", "-sequence", "1", "-expires", time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "-themes", filename, "-data-compatible"); err != nil {
		t.Fatal(err)
	}
	catalog, err := extension.ReadJSON[plugin.Catalog](filepath.Join(out, "catalog.themes-v1.readable.json"), 1<<20)
	if err != nil || len(catalog.Plugins) != 0 || len(catalog.Themes) != 1 {
		t.Fatal(catalog, err)
	}
	meta := catalog.Themes[0].Cover
	if meta == nil {
		t.Fatal("missing signed cover")
	}
	data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(meta.Path)))
	sum := sha256.Sum256(data)
	if err != nil || !bytes.Equal(data, cover.Bytes()) || meta.SHA256 != hex.EncodeToString(sum[:]) || meta.Size != int64(len(data)) {
		t.Fatal(meta, err)
	}
}
