package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnippetMaintenanceCommandGuardsAndPrivateFile(t *testing.T) {
	t.Setenv("NEWPRESS_DATABASE_URL", "")
	t.Setenv("NEWPRESS_DATABASE_URL_FILE", "")
	t.Setenv("NEWPRESS_DATA_DIR", filepath.Join(t.TempDir(), "missing-data"))
	for _, args := range [][]string{
		{}, {"review"}, {"review", "--snippet-id", "-1"}, {"quarantines", "--limit", "0"},
		{"quarantine", "--snippet-id", "1"}, {"repair", "--id", strings.Repeat("a", 64)},
		{"repair", "--confirm-all-instances-stopped", "--replacement", "missing"},
		{"export-repair", "--snippet-id", "1"}, {"review", "--snippet-id", "1", "--file", ""},
		{"quarantines", "--confirm-all-instances-stopped"},
	} {
		if err := run(append([]string{"snippet", "records"}, args...)); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatal(args, err)
		}
	}
	if err := run([]string{"snippet", "records", "review", "--snippet-id", "1"}); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "repair.json")
	raw := []byte(`{"schema":1,"kind":"newpress.snippet-repair","private":"private-file-body"}`)
	if err := writeSnippetRepairFile(path, raw); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	read, err := readSnippetRepairFile(path, hash)
	if err != nil || !bytes.Equal(raw, read) {
		t.Fatal(err)
	}
	if _, err = readSnippetRepairFile(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	if _, err = readSnippetRepairFile(path, "bad"); err == nil {
		t.Fatal("invalid checksum accepted")
	}
	if err = writeSnippetRepairFile(path, []byte("replacement")); !os.IsExist(err) {
		t.Fatal("export overwrote an existing file", err)
	}
	retained, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, retained) {
		t.Fatal("existing export changed", err)
	}
}
