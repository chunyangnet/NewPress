package packagefs

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageRejectsCorruptDirectoryEntriesBeforeWriting(t *testing.T) {
	for _, kind := range []string{"declared data", "hidden data", "checksum", "local header", "deflate stream", "descriptor", "encrypted", "algorithm", "oversized empty stream"} {
		t.Run(kind, func(t *testing.T) {
			archivePath := createDirectoryIntegrityArchive(t, kind)
			if _, err := ReadJSON[map[string]any](archivePath, "manifest.json"); err == nil {
				t.Error("corrupt directory passed manifest preflight")
			}
			destination := t.TempDir()
			if err := Extract(archivePath, destination, DefaultLimits()); err == nil {
				t.Error("corrupt directory was extracted")
			}
			if children, err := os.ReadDir(destination); err != nil || len(children) != 0 {
				t.Errorf("invalid directory left output: %v, %v", children, err)
			}
		})
	}
}

func TestPackageAcceptsVerifiedEmptyDirectories(t *testing.T) {
	for _, kind := range []string{"stored", "deflated", "deflated descriptor", "mode only"} {
		t.Run(kind, func(t *testing.T) {
			archivePath := createDirectoryIntegrityArchive(t, kind)
			if _, err := ReadJSON[map[string]any](archivePath, "manifest.json"); err != nil {
				t.Fatal(err)
			}
			destination := t.TempDir()
			if err := Extract(archivePath, destination, DefaultLimits()); err != nil {
				t.Fatal(err)
			}
			name := "assets"
			if kind == "mode only" {
				name = "assetsX"
			}
			if info, err := os.Stat(filepath.Join(destination, name)); err != nil || !info.IsDir() {
				t.Fatalf("empty directory missing: %v, %v", info, err)
			}
			if body, err := os.ReadFile(filepath.Join(destination, "first.txt")); err != nil || string(body) != "preserved" {
				t.Fatalf("file contents changed: %q, %v", body, err)
			}
		})
	}
}

// Build the directory as a same-length regular entry first, then change its
// local and central names. zip.Writer intentionally refuses directory payloads.
func createDirectoryIntegrityArchive(t *testing.T, kind string) string {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, file := range []struct{ name, body string }{{"manifest.json", `{}`}, {"first.txt", "preserved"}} {
		entry, err := writer.Create(file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	method := uint16(zip.Store)
	if strings.HasPrefix(kind, "deflat") || kind == "descriptor" || kind == "oversized empty stream" {
		method = zip.Deflate
	}
	header := &zip.FileHeader{Name: "assetsX", Method: method}
	header.SetMode(os.ModeDir | 0o750)
	var body []byte
	if kind == "declared data" || kind == "hidden data" {
		body = []byte("ignored directory payload")
	}
	compressed := body
	if method == zip.Deflate {
		var compressedBuffer bytes.Buffer
		compressor, err := flate.NewWriter(&compressedBuffer, flate.DefaultCompression)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := compressor.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := compressor.Close(); err != nil {
			t.Fatal(err)
		}
		compressed = compressedBuffer.Bytes()
	}
	if kind == "oversized empty stream" {
		// Non-final empty stored blocks, followed by a valid final empty block.
		compressed = bytes.Repeat([]byte{0, 0, 0, 0xff, 0xff}, (64<<10)/5+1)
		compressed = append(compressed, 1, 0, 0, 0xff, 0xff)
	}
	header.CRC32 = crc32.ChecksumIEEE(body)
	header.UncompressedSize64 = uint64(len(body))
	header.CompressedSize64 = uint64(len(compressed))
	header.ReaderVersion = 20
	if kind == "deflated descriptor" || kind == "descriptor" {
		header.Flags |= 8
	}
	entry, err := writer.CreateRaw(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(compressed); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	payload := buffer.Bytes()
	reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	directory := reader.File[2]
	dataOffset, err := directory.DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	centralName := bytes.LastIndex(payload, []byte("assetsX"))
	central := centralName - 46
	local := int(dataOffset) - 30 - len("assetsX")
	if central < 0 || binary.LittleEndian.Uint32(payload[central:]) != 0x02014b50 || binary.LittleEndian.Uint32(payload[local:]) != 0x04034b50 {
		t.Fatal("invalid directory test fixture")
	}
	if kind != "mode only" {
		copy(payload[centralName:], "assets/")
		copy(payload[local+30:], "assets/")
	}
	switch kind {
	case "hidden data":
		binary.LittleEndian.PutUint32(payload[central+24:], 0)
		binary.LittleEndian.PutUint32(payload[central+16:], 0)
	case "checksum":
		binary.LittleEndian.PutUint32(payload[central+16:], 1)
	case "local header":
		payload[local] ^= 0xff
	case "deflate stream":
		payload[dataOffset] = 0xff
	case "descriptor":
		descriptor := int(dataOffset) + int(directory.CompressedSize64)
		if binary.LittleEndian.Uint32(payload[descriptor:]) != 0x08074b50 {
			t.Fatal("missing fixture data descriptor")
		}
		payload[descriptor+4] ^= 1
	case "encrypted":
		binary.LittleEndian.PutUint16(payload[central+8:], header.Flags&^8|1)
	case "algorithm":
		binary.LittleEndian.PutUint16(payload[central+10:], 99)
	}
	archivePath := filepath.Join(t.TempDir(), "directory.zip")
	if err := os.WriteFile(archivePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return archivePath
}
