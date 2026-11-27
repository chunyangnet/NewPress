package theme

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallThemeParentDirectoryConsistency(t *testing.T) {
	t.Parallel()
	for _, alias := range []bool{false, true} {
		name, secondParent := "consistent", "Icons"
		if alias {
			name, secondParent = "aliased", "icons"
		}
		t.Run(name, func(t *testing.T) {
			manifest := Manifest{
				Schema: 1, Slug: "directory-theme", Name: "Directory test", Version: "1.0.0", Engine: "go-template/v1", View: "public/v1",
				Templates: map[string]string{"home": "home.html", "single": "single.html"},
			}
			payload, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(t.TempDir(), "theme.zip")
			archiveFile, err := os.Create(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			defer archiveFile.Close()
			writer := zip.NewWriter(archiveFile)
			for _, file := range []struct{ name, body string }{
				{"theme.json", string(payload)},
				{"templates/base.html", `{{define "base"}}{{template "content" .}}{{end}}`},
				{"templates/home.html", `{{define "content"}}home{{end}}`},
				{"templates/single.html", `{{define "content"}}single{{end}}`},
				{"assets/Icons/one.css", "one"},
				{"assets/" + secondParent + "/two.css", "two"},
			} {
				entry, err := writer.Create(file.name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write([]byte(file.body)); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := archiveFile.Close(); err != nil {
				t.Fatal(err)
			}

			root := t.TempDir()
			_, err = NewManager(root, manifest.Slug).InstallPackage(archivePath)
			if alias {
				if err == nil || !strings.Contains(err.Error(), "collide") {
					t.Errorf("aliased parent installation error = %v", err)
				}
				if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
					t.Errorf("rejected theme left installation artifacts: %v, %v", entries, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(root, manifest.Slug, "assets", "Icons", "two.css"))
			if err != nil || string(body) != "two" {
				t.Fatalf("consistent parent installation contents = %q, %v", body, err)
			}
		})
	}
}
