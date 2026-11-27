package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/app"
	"newpress.local/newpress/internal/config"
)

func TestBackupCommandsProduceRestorableSiteWithoutConfigOnInspectOrRestore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NEWPRESS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("NEWPRESS_THEMES_DIR", filepath.Join(root, "themes"))
	t.Setenv("NEWPRESS_PLUGINS_DIR", filepath.Join(root, "plugins"))
	t.Setenv("NEWPRESS_ACTIVE_THEME", "backup-test")
	t.Setenv("NEWPRESS_DATABASE_URL", "")
	t.Setenv("NEWPRESS_ADMIN_PASSWORD_HASH", "")
	t.Setenv("NEWPRESS_SEED_DEMO", "false")
	for name, body := range map[string]string{
		"theme.json":            `{"schema":1,"slug":"backup-test","name":"Backup test","version":"1.0.0","engine":"go-template/v1","view":"public/v1","templates":{"home":"home.html","single":"single.html"}}`,
		"templates/base.html":   `{{define "base"}}{{template "content" .}}{{end}}`,
		"templates/home.html":   `{{define "content"}}home{{end}}`,
		"templates/single.html": `{{define "content"}}single{{end}}`,
	} {
		path := filepath.Join(root, "themes", "backup-test", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	application.Close()
	archive := filepath.Join(root, "site.zip")
	created, err := backupCommand([]string{"create", archive})
	if err != nil || !created.Verified || !created.ApplicationValidated {
		t.Fatalf("create = %+v, %v", created, err)
	}
	unrelated := filepath.Join(root, "must-remain-absent")
	t.Setenv("NEWPRESS_DATA_DIR", unrelated)
	t.Setenv("NEWPRESS_ADDR", "deliberately-invalid")
	inspected, err := backupCommand([]string{"inspect", archive})
	if err != nil || inspected.ArchiveSHA256 != created.ArchiveSHA256 {
		t.Fatalf("inspect read unrelated environment: %+v, %v", inspected, err)
	}
	restored, err := backupCommand([]string{"restore", archive, "--destination", filepath.Join(root, "restored")})
	if err != nil || !restored.ApplicationValidated || restored.Environment["NEWPRESS_ACTIVE_THEME"] != "backup-test" {
		t.Fatalf("restore = %+v, %v", restored, err)
	}
	if _, err := os.Stat(unrelated); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspect or restore touched the current data directory")
	}
	for _, args := range [][]string{nil, {"create"}, {"restore", archive}, {"restore", archive, "--force", "target"}, {"inspect", archive, "extra"}} {
		if _, err := backupCommand(args); err == nil {
			t.Fatalf("invalid command accepted: %v", args)
		}
	}
}
