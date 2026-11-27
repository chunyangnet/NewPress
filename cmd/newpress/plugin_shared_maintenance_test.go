package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/dirlock"
)

func TestPluginSharedMaintenanceCommandValidationAndFileMode(t *testing.T) {
	path, _, _ := maintenanceCommandFileFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		nil, {"unknown"}, {"inspect", "--reason", "unused"}, {"inspect", "--type", "task"}, {"inspect", "--limit", "0"}, {"inspect", "--offset", "-1"}, {"inspect", "--job", "bad"},
		{"quarantine", "--type", "authority", "--reference", strings.Repeat("a", 64), "--request-key", "key", "--operator", "operator", "--reason", "checked"},
		{"quarantine", "--type", "webhook_intent", "--reference", strings.Repeat("a", 64), "--request-key", "key", "--operator", "operator", "--reason", "checked"},
		{"repair", "--id", strings.Repeat("a", 64), "--operator", "operator", "--reason", "checked"},
		{"repair", "--id", strings.Repeat("a", 64), "--operator", "operator", "--reason", "checked", "--confirm-all-workers-stopped", "--replacement", "missing-digest.json"},
		{"repair", "--id", strings.Repeat("a", 64), "--operator", "operator", "--reason", "checked", "--confirm-all-workers-stopped", "--keep-reference", strings.Repeat("b", 64), "--replacement", "record.json", "--replacement-sha256", strings.Repeat("c", 64)},
	} {
		if err := maintainSharedPluginRecords(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatal(args, err)
		}
	}
	for _, action := range []string{"inspect", "quarantines"} {
		if err := pluginCommand([]string{"maintain-shared-records", action}); err == nil || !strings.Contains(err.Error(), "requires PostgreSQL") {
			t.Fatal(action, err)
		}
		if err := pluginCommand([]string{"maintain-shared-records", action, "--type", "webhook_intent"}); err == nil || !strings.Contains(err.Error(), "requires PostgreSQL") {
			t.Fatal("webhook type rejected before storage selection", err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("file storage changed", err)
	}
	t.Setenv("NEWPRESS_DATABASE_URL", "postgres://127.0.0.1:1/unused?sslmode=disable")
	root := filepath.Dir(path)
	locks, err := dirlock.Acquire(root, filepath.Join(root, "themes"), filepath.Join(root, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	defer locks.Close()
	if err := maintainSharedPluginRecords([]string{"inspect"}); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatal("directory lock bypass", err)
	}
}
