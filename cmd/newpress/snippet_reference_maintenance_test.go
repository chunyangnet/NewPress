package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnippetReferenceCommandValidationAndFileMode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "untouched")
	t.Setenv("NEWPRESS_DATA_DIR", root)
	t.Setenv("NEWPRESS_THEMES_DIR", filepath.Join(root, "themes"))
	t.Setenv("NEWPRESS_PLUGINS_DIR", filepath.Join(root, "plugins"))
	t.Setenv("NEWPRESS_DATABASE_URL", "")
	t.Setenv("NEWPRESS_DATABASE_URL_FILE", "")
	for _, args := range [][]string{
		nil, {"references"}, {"inspect"}, {"references", "unknown"},
		{"references", "inspect", "--limit", "0"}, {"references", "inspect", "--limit", "101"},
		{"references", "rebuild", "--after", "1"}, {"references", "rebuild", "--after", "3", "--through", "2"},
		{"references", "inspect", "--after", "-1"}, {"references", "inspect", "--through", "-1"},
		{"references", "inspect", "--after", "9223372036854775808"},
		{"references", "inspect", "--post", "1"}, {"references", "rebuild", "extra"},
	} {
		if err := run(append([]string{"snippet"}, args...)); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatal(args, err)
		}
	}
	for _, action := range []string{"inspect", "rebuild"} {
		if err := run([]string{"snippet", "references", action}); err == nil || !strings.Contains(err.Error(), "requires PostgreSQL") {
			t.Fatal(action, err)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("invalid/file-mode command created local storage", err)
	}
}
