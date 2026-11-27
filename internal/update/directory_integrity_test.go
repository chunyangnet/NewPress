package update

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCoreUpdateValidatesDirectoryContents(t *testing.T) {
	original := zipFixture(t, "bin/", "bin/"+binaryName())
	central := int(binary.LittleEndian.Uint32(original[len(original)-22+16:]))
	if binary.LittleEndian.Uint32(original[central:]) != 0x02014b50 || string(original[central+46:central+50]) != "bin/" {
		t.Fatal("fixture directory central record not found")
	}
	for _, kind := range []string{"valid", "checksum", "local header", "stored payload", "descriptor", "algorithm"} {
		t.Run(kind, func(t *testing.T) {
			payload := bytes.Clone(original)
			switch kind {
			case "checksum":
				binary.LittleEndian.PutUint32(payload[central+16:], 1)
			case "local header":
				payload[0] ^= 0xff
			case "stored payload":
				binary.LittleEndian.PutUint32(payload[central+20:], 1)
			case "descriptor":
				binary.LittleEndian.PutUint16(payload[central+8:], 8)
			case "algorithm":
				binary.LittleEndian.PutUint16(payload[central+10:], 99)
			}
			archivePath := filepath.Join(t.TempDir(), "update.zip")
			if err := os.WriteFile(archivePath, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			manager := managerFixture(t)
			id := randomID()
			release, err := stageArchive(t.Context(), archivePath, manager.root, id, manager.themes)
			if kind != "valid" {
				if err == nil {
					t.Error("update with corrupt directory was staged")
				}
				if _, err := os.Stat(filepath.Join(manager.root, "releases", id)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("failed stage left a release directory: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if body, err := os.ReadFile(executable(manager.root, release, "")); err != nil || string(body) != "fixture content" {
					t.Errorf("valid staged binary changed: %q, %v", body, err)
				}
			}
			if body, err := os.ReadFile(filepath.Join(manager.themes, "custom.txt")); err != nil || string(body) != "keep custom theme" {
				t.Errorf("source themes changed: %q, %v", body, err)
			}
		})
	}
}
