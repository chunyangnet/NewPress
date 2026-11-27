package packagefs_test

import (
	"archive/zip"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

func TestExtensionInstallValidatesDirectoryEntries(t *testing.T) {
	module, err := hex.DecodeString("0061736d010000000105016000017e030201000712010e6e705f6170695f76657273696f6e00000a0601040042010b")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"theme", "plugin"} {
		t.Run(kind, func(t *testing.T) {
			for _, corrupt := range []bool{false, true} {
				name := "valid"
				if corrupt {
					name = "corrupt"
				}
				t.Run(name, func(t *testing.T) {
					files := map[string]string{
						"theme.json":            `{"schema":1,"slug":"directory-theme","name":"Directory","version":"1.0.0","engine":"go-template/v1","view":"public/v1","templates":{"home":"home.html","single":"single.html"}}`,
						"templates/base.html":   `{{define "base"}}{{template "content" .}}{{end}}`,
						"templates/home.html":   `{{define "content"}}home{{end}}`,
						"templates/single.html": `{{define "content"}}single{{end}}`,
					}
					slug := "directory-theme"
					if kind == "plugin" {
						files = map[string]string{
							"plugin.json": `{"schema":1,"slug":"directory-plugin","name":"Directory","version":"1.0.0","runtime":"wasm/core-v1","entry":"plugin.wasm"}`,
							"plugin.wasm": string(module),
						}
						slug = "directory-plugin"
					}
					archivePath := filepath.Join(t.TempDir(), "package.zip")
					file, err := os.Create(archivePath)
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					writer := zip.NewWriter(file)
					for path, body := range files {
						entry, err := writer.Create(path)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := entry.Write([]byte(body)); err != nil {
							t.Fatal(err)
						}
					}
					header := &zip.FileHeader{Name: "assets/", Method: zip.Store}
					header.SetMode(os.ModeDir | 0o750)
					if corrupt {
						header.CRC32 = 1
					}
					if _, err := writer.CreateHeader(header); err != nil {
						t.Fatal(err)
					}
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
					root := t.TempDir()
					if kind == "theme" {
						_, err = theme.NewManager(root, slug).InstallPackage(archivePath)
					} else {
						_, err = plugin.NewManager(root).InstallPackage(archivePath)
					}
					if corrupt {
						if err == nil {
							t.Error("extension with a corrupt directory was installed")
						}
						if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
							t.Errorf("failed install left artifacts: %v, %v", entries, err)
						}
					} else {
						if err != nil {
							t.Fatal(err)
						}
						if info, err := os.Stat(filepath.Join(root, slug, "assets")); err != nil || !info.IsDir() {
							t.Errorf("valid directory missing: %v, %v", info, err)
						}
					}
				})
			}
		})
	}
}
