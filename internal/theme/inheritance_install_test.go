package theme

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestChildManifestOnlyInstall(t *testing.T) {
	m, parent := themeOTAFixture(t)
	child := Manifest{Schema: 2, Slug: "installed-child", Name: "Child", Version: "1.0.0", Engine: parent.Engine, View: parent.View, Parent: &ParentTheme{Slug: parent.Slug, Version: "^1.0.0"}}
	archive := filepath.Join(t.TempDir(), "child.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	entry, err := z.Create("theme.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(entry).Encode(child); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InstallPackage(archive); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadExplicit(child.Slug); err != nil {
		t.Fatal(err)
	}
	missing := NewManager(t.TempDir(), child.Slug)
	if _, err := missing.InstallPackage(archive); err == nil {
		t.Fatal("installed without parent")
	}
	entries, err := os.ReadDir(missing.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed install left files: %v", entries)
	}
}
