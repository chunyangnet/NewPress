package backup

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRejectsDamagedDirectoryEntries(t *testing.T) {
	fixture := fixtureSite(t, false)
	archivePath := filepath.Join(fixture.parent, "valid.zip")
	if _, err := Create(t.Context(), fixture.cfg, archivePath); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatal(err)
	}
	var directory *zip.File
	for _, entry := range reader.File {
		if entry.Name == "data/" {
			directory = entry
			break
		}
	}
	if directory == nil {
		t.Fatal("fixture is missing data directory")
	}
	dataOffset, err := directory.DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	central := int(binary.LittleEndian.Uint32(original[len(original)-22+16:]))
	for {
		if central+46 > len(original) || binary.LittleEndian.Uint32(original[central:]) != 0x02014b50 {
			t.Fatal("fixture directory central record not found")
		}
		nameLength := int(binary.LittleEndian.Uint16(original[central+28:]))
		extraLength := int(binary.LittleEndian.Uint16(original[central+30:]))
		commentLength := int(binary.LittleEndian.Uint16(original[central+32:]))
		if string(original[central+46:central+46+nameLength]) == directory.Name {
			break
		}
		central += 46 + nameLength + extraLength + commentLength
	}
	local := int(binary.LittleEndian.Uint32(original[central+42:]))
	if binary.LittleEndian.Uint32(original[local:]) != 0x04034b50 || dataOffset < int64(local+30) {
		t.Fatal("fixture directory local record not found")
	}
	for _, kind := range []string{"checksum", "local header", "stored payload", "deflate stream", "descriptor"} {
		t.Run(kind, func(t *testing.T) {
			payload := bytes.Clone(original)
			switch kind {
			case "checksum":
				binary.LittleEndian.PutUint32(payload[central+16:], 1)
			case "local header":
				payload[local] ^= 0xff
			case "stored payload":
				binary.LittleEndian.PutUint32(payload[central+20:], 1)
			case "deflate stream":
				binary.LittleEndian.PutUint16(payload[central+10:], zip.Deflate)
				binary.LittleEndian.PutUint32(payload[central+20:], 1)
			case "descriptor":
				flags := binary.LittleEndian.Uint16(payload[central+8:])
				binary.LittleEndian.PutUint16(payload[central+8:], flags|8)
			}
			bad := filepath.Join(t.TempDir(), "bad.zip")
			writeFixtureFile(t, bad, payload)
			if _, err := Inspect(t.Context(), bad); err == nil {
				t.Error("damaged directory passed backup inspection")
			}
			for _, existing := range []bool{false, true} {
				parent := t.TempDir()
				destination := filepath.Join(parent, "restored")
				if existing {
					if err := os.Mkdir(destination, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := Restore(t.Context(), bad, destination); err == nil {
					t.Errorf("damaged directory restored (existing=%v)", existing)
				}
				if existing {
					if children, err := os.ReadDir(destination); err != nil || len(children) != 0 {
						t.Errorf("failed restore changed empty destination: %v, %v", children, err)
					}
				} else if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("failed restore published destination: %v", err)
				}
				assertNoStaging(t, parent)
			}
			if after, err := os.ReadFile(bad); err != nil || !bytes.Equal(payload, after) {
				t.Errorf("validation changed input archive: %v", err)
			}
		})
	}
}
