package backup

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
)

func TestBackupExcludesSessionsAndTheirUncommittedSnapshots(t *testing.T) {
	fixture := fixtureSite(t, true)
	path := filepath.Join(fixture.cfg.DataDir, auth.SessionDirectory)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sessions.json", "sessions.json.bak", ".newpress-sessions-orphan.tmp"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte("private authentication state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(fixture.parent, "without-sessions.zip")
	if _, err := Create(t.Context(), fixture.cfg, archive); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if strings.Contains(file.Name, auth.SessionDirectory) {
			_ = reader.Close()
			t.Fatal("backup retained live or stale authentication state")
		}
	}
	_ = reader.Close()
	dest := filepath.Join(fixture.parent, "restored-without-sessions")
	if _, err := Restore(t.Context(), archive, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "data", auth.SessionDirectory)); !os.IsNotExist(err) {
		t.Fatalf("restore brought back login state: %v", err)
	}
}

func TestBackupRejectsImportedSessionStateAndKeepsLookalikes(t *testing.T) {
	for _, root := range []string{"data/auth-sessions", "data/AUTH-SESSIONS", "data/Auth-Sessions"} {
		for _, suffix := range []string{"", "/sessions.json", "/sessions.json.bak", "/nested/orphan.tmp"} {
			if err := validatePath(root + suffix); err == nil {
				t.Fatalf("archive accepted authentication state: %s", root+suffix)
			}
		}
	}
	for _, path := range []string{"data/auth-sessions-old/readme.txt", "data/nested/auth-sessions/keep.txt", "themes/auth-sessions/keep.txt", "plugins/auth-sessions/keep.txt"} {
		if err := validatePath(path); err != nil {
			t.Fatalf("unrelated path was rejected: %s: %v", path, err)
		}
	}
}
