package plugin

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPluginParentDirectoryConsistency(t *testing.T) {
	t.Parallel()
	for _, alias := range []bool{false, true} {
		name, secondParent := "consistent", "Icons"
		if alias {
			name, secondParent = "aliased", "icons"
		}
		t.Run(name, func(t *testing.T) {
			manifest := Manifest{
				Schema: 1, Slug: "directory-plugin", Name: "Directory test", Version: "1.0.0",
				Runtime: "wasm/core-v1", Entry: "plugin.wasm",
			}
			payload, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(t.TempDir(), "plugin.zip")
			archiveFile, err := os.Create(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			defer archiveFile.Close()
			writer := zip.NewWriter(archiveFile)
			for _, file := range []struct {
				name string
				body []byte
			}{
				{"plugin.json", payload},
				{"plugin.wasm", abiV1Module()},
				{"assets/Icons/one.txt", []byte("one")},
				{"assets/" + secondParent + "/two.txt", []byte("two")},
			} {
				entry, err := writer.Create(file.name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write(file.body); err != nil {
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
			_, err = NewManager(root).InstallPackage(archivePath)
			if alias {
				if err == nil || !strings.Contains(err.Error(), "collide") {
					t.Errorf("aliased parent installation error = %v", err)
				}
				if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
					t.Errorf("rejected plugin left installation artifacts: %v, %v", entries, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(root, manifest.Slug, "assets", "Icons", "two.txt"))
			if err != nil || string(body) != "two" {
				t.Fatalf("consistent parent installation contents = %q, %v", body, err)
			}
		})
	}
}
