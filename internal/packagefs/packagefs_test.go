package packagefs

import (
	"archive/zip"
	"encoding/binary"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testManifest struct {
	Schema int    `json:"schema"`
	Name   string `json:"name"`
}

type archiveEntry struct {
	Name    string
	Body    string
	Mode    fs.FileMode
	NonUTF8 bool
}

func TestOpenZIPRejectsExcessiveEntryCountBeforeParsing(t *testing.T) {
	t.Parallel()
	archive := make([]byte, 22)
	binary.LittleEndian.PutUint32(archive[0:4], 0x06054b50)
	binary.LittleEndian.PutUint16(archive[8:10], 2)
	binary.LittleEndian.PutUint16(archive[10:12], 2)
	path := filepath.Join(t.TempDir(), "oversized-directory.zip")
	if err := os.WriteFile(path, archive, 0600); err != nil {
		t.Fatal(err)
	}
	if file, _, err := OpenZIP(path, 1<<20, 1); err == nil {
		file.Close()
		t.Fatal("accepted EOCD entry count above limit")
	} else if !strings.Contains(err.Error(), "entry count") {
		t.Fatalf("entry count was not rejected during preflight: %v", err)
	}
}

func TestOpenZIPAcceptsEmptyAndCommentedArchives(t *testing.T) {
	t.Parallel()
	for _, withEntry := range []bool{false, true} {
		withEntry := withEntry
		t.Run(map[bool]string{false: "empty", true: "commented entry"}[withEntry], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "valid.zip")
			output, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(output)
			if err := writer.SetComment("package fixture"); err != nil {
				t.Fatal(err)
			}
			if withEntry {
				entry, err := writer.Create("entry.txt")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write([]byte("content")); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := output.Close(); err != nil {
				t.Fatal(err)
			}
			file, reader, err := OpenZIP(path, 1<<20, 10)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			want := 0
			if withEntry {
				want = 1
			}
			if len(reader.File) != want {
				t.Fatalf("archive entries = %d, want %d", len(reader.File), want)
			}
		})
	}
}

func TestDecodeJSONRejectsAmbiguousOrUnboundedDocuments(t *testing.T) {
	t.Parallel()
	valid, err := DecodeJSON[testManifest](strings.NewReader(`{"schema":1,"name":"ok"}`), 128, "test manifest")
	if err != nil || valid.Schema != 1 || valid.Name != "ok" {
		t.Fatalf("decode valid manifest = %#v, %v", valid, err)
	}
	for name, input := range map[string]string{
		"duplicate field": `{"schema":1,"schema":2,"name":"bad"}`,
		"unknown field":   `{"schema":1,"name":"bad","script":true}`,
		"trailing value":  `{"schema":1,"name":"bad"} {}`,
		"trailing junk":   `{"schema":1,"name":"bad"} junk`,
		"empty":           ``,
		"oversized":       strings.Repeat(" ", 129),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeJSON[testManifest](strings.NewReader(input), 128, "test manifest"); err == nil {
				t.Fatalf("accepted invalid JSON document %q", input)
			}
		})
	}
	if _, err := DecodeJSON[testManifest](strings.NewReader(`{}`), 0, "test manifest"); err == nil {
		t.Fatal("accepted non-positive JSON limit")
	}
}

func TestReadJSONRejectsDuplicateManifest(t *testing.T) {
	t.Parallel()
	archive := createArchiveEntries(t, []archiveEntry{
		{Name: "plugin.json", Body: `{"schema":1,"name":"first"}`},
		{Name: "./plugin.json", Body: `{"schema":1,"name":"second"}`},
	})
	if _, err := ReadJSON[testManifest](archive, "plugin.json"); err == nil {
		t.Fatal("accepted duplicate manifest paths")
	}
}

func TestReadJSONRejectsNonUTF8ArchiveMetadata(t *testing.T) {
	t.Parallel()
	archive := createArchiveEntries(t, []archiveEntry{
		{Name: "plugin.json", Body: `{"schema":1}`},
		{Name: string([]byte{'b', 'a', 'd', 0xff}), Body: "bad", NonUTF8: true},
	})
	if _, err := ReadJSON[testManifest](archive, "plugin.json"); err == nil {
		t.Fatal("accepted archive entry marked as non-UTF-8")
	}
}

func TestExtractRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"../escape.txt", "/absolute.txt", "safe/../../escape.txt", "safe/../escape.txt", "./safe.txt", "safe//item.txt", "safe\\escape.txt", "asset.txt:stream", "CON/readme.txt", "COM¹.txt", "LPT².txt", "cafe\u0301.txt", string([]byte{'b', 'a', 'd', 0xff})} {
		name := name
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			t.Parallel()
			archive := createArchive(t, map[string]string{name: "bad"})
			err := Extract(archive, t.TempDir(), DefaultLimits())
			if err == nil {
				t.Fatalf("expected %q to be rejected", name)
			}
		})
	}
}

func TestValidateRelativePathRejectsWindowsDeviceNames(t *testing.T) {
	for _, name := range []string{"COM¹", "COM².txt", "COM³.data", "LPT¹", "LPT².txt", "LPT³.data"} {
		if err := ValidateRelativePath(name); err == nil {
			t.Errorf("accepted Windows device path %q", name)
		}
	}
}

func TestReadJSONRejectsNonCanonicalManifestPath(t *testing.T) {
	t.Parallel()
	archive := createArchiveEntries(t, []archiveEntry{{Name: "./plugin.json", Body: `{"schema":1}`}})
	if _, err := ReadJSON[testManifest](archive, "plugin.json"); err == nil {
		t.Fatal("accepted non-canonical manifest path")
	}
}

func TestExtractRejectsCaseCollision(t *testing.T) {
	t.Parallel()
	archive := createArchive(t, map[string]string{"Assets/app.css": "a", "assets/app.css": "b"})
	if err := Extract(archive, t.TempDir(), DefaultLimits()); err == nil {
		t.Fatal("expected case-insensitive path collision to be rejected")
	}
}

func TestExtractRejectsSpecialFiles(t *testing.T) {
	t.Parallel()
	for name, mode := range map[string]fs.FileMode{
		"symlink": os.ModeSymlink | 0o777,
		"fifo":    os.ModeNamedPipe | 0o600,
		"socket":  os.ModeSocket | 0o600,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			archive := createArchiveEntries(t, []archiveEntry{{Name: name, Body: "target", Mode: mode}})
			if err := Extract(archive, t.TempDir(), DefaultLimits()); err == nil {
				t.Fatalf("accepted special file mode %v", mode)
			}
		})
	}
}

func TestExtractRejectsInvalidLimits(t *testing.T) {
	t.Parallel()
	archive := createArchive(t, map[string]string{"file.txt": "content"})
	for _, limits := range []Limits{
		{MaxFiles: 0, MaxFileBytes: 1, MaxTotal: 1, MaxArchiveBytes: 1},
		{MaxFiles: 1, MaxFileBytes: 0, MaxTotal: 1, MaxArchiveBytes: 1},
		{MaxFiles: 1, MaxFileBytes: 1, MaxTotal: 0, MaxArchiveBytes: 1},
		{MaxFiles: -1, MaxFileBytes: 1, MaxTotal: 1, MaxArchiveBytes: 1},
		{MaxFiles: 1, MaxFileBytes: math.MaxInt64, MaxTotal: math.MaxInt64, MaxArchiveBytes: 1},
		{MaxFiles: 1, MaxFileBytes: 1, MaxTotal: 1, MaxArchiveBytes: 0},
		{MaxFiles: 1, MaxFileBytes: 1, MaxTotal: 1, MaxArchiveBytes: math.MaxInt64},
	} {
		if err := Extract(archive, t.TempDir(), limits); err == nil {
			t.Fatalf("accepted invalid limits %#v", limits)
		}
	}
}

func TestOpenArchiveRejectsOversizedBeforeZipParsing(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "oversized.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Truncate(1025); err != nil {
		archive.Close()
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openArchive(archivePath, 1024, DefaultLimits().MaxFiles); err == nil || !strings.Contains(err.Error(), "exceeds 1024 bytes") {
		t.Fatalf("oversized archive error = %v", err)
	}
}

func TestCompressionRatioUsesExactBoundary(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		uncompressed uint64
		compressed   uint64
		maximum      uint64
		want         bool
	}{
		{uncompressed: 1000, compressed: 1, maximum: 1000, want: false},
		{uncompressed: 1001, compressed: 1, maximum: 1000, want: true},
		{uncompressed: 2000, compressed: 2, maximum: 1000, want: false},
		{uncompressed: 2001, compressed: 2, maximum: 1000, want: true},
		{uncompressed: 2 << 20, compressed: 0, maximum: 1000, want: true},
		{uncompressed: 2 << 20, compressed: 1, maximum: 0, want: false},
	} {
		if got := compressionRatioExceeds(test.uncompressed, test.compressed, test.maximum); got != test.want {
			t.Fatalf("compressionRatioExceeds(%d, %d, %d) = %t, want %t", test.uncompressed, test.compressed, test.maximum, got, test.want)
		}
	}
}

func TestCopyFileRemovesPartialTargetWhenLimitIsExceeded(t *testing.T) {
	t.Parallel()
	archive := createArchive(t, map[string]string{"large.txt": "too large"})
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	target := filepath.Join(t.TempDir(), "large.txt")
	written, err := copyFile(reader.File[0], target, 3)
	if err == nil || written != 4 {
		t.Fatalf("copy result = %d, %v", written, err)
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial target still exists: %v", statErr)
	}
}

func TestExtractValidPackage(t *testing.T) {
	t.Parallel()
	archive := createArchive(t, map[string]string{
		"plugin.json":    `{"schema":1}`,
		"assets/app.css": "body{}",
	})
	destination := t.TempDir()
	if err := Extract(archive, destination, DefaultLimits()); err != nil {
		t.Fatalf("extract valid package: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "assets", "app.css"))
	if err != nil || string(data) != "body{}" {
		t.Fatalf("unexpected extracted file: %q, %v", data, err)
	}
}

func TestExtractPreflightsArchiveBeforeWriting(t *testing.T) {
	t.Parallel()
	archive := createArchiveEntries(t, []archiveEntry{
		{Name: "safe.txt", Body: "written before a later invalid entry"},
		{Name: "../escape.txt", Body: "bad"},
	})
	destination := t.TempDir()
	if err := Extract(archive, destination, DefaultLimits()); err == nil {
		t.Fatal("accepted archive with a later unsafe path")
	}
	children, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 0 {
		t.Fatalf("preflight failure left output behind: %v", children)
	}
}

func TestExtractRejectsDestinationSymlinksAndExistingContents(t *testing.T) {
	t.Parallel()
	archive := createArchive(t, map[string]string{"file.txt": "content"})
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "existing"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Extract(archive, root, DefaultLimits()); err == nil {
		t.Fatal("accepted non-empty extraction destination")
	}

	outside := t.TempDir()
	symlinkRoot := filepath.Join(t.TempDir(), "destination")
	if err := os.Symlink(outside, symlinkRoot); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	if err := Extract(archive, symlinkRoot, DefaultLimits()); err == nil {
		t.Fatal("accepted symlink extraction destination")
	}

}

func TestExtractRejectsDestinationSymlinkAncestors(t *testing.T) {
	t.Parallel()
	archive := createArchive(t, map[string]string{"file.txt": "content"})
	ancestorRoot := t.TempDir()
	ancestorTarget := t.TempDir()
	linkedAncestor := filepath.Join(ancestorRoot, "linked")
	if err := os.Symlink(ancestorTarget, linkedAncestor); err != nil {
		t.Skipf("creating symlink ancestor is unavailable: %v", err)
	}
	linkedDestination := filepath.Join(linkedAncestor, "destination")
	if err := os.Mkdir(linkedDestination, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := Extract(archive, linkedDestination, DefaultLimits()); err == nil {
		t.Fatal("accepted extraction destination with a symlink ancestor")
	}
	if _, err := os.Lstat(filepath.Join(ancestorTarget, "destination", "file.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrote through symlink ancestor: %v", err)
	}
}

func TestLstatNoSymlinkRejectsSymlinkAncestors(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "file.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(outside, linked); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	if _, err := LstatNoSymlink(filepath.Join(linked, "file.txt")); err == nil {
		t.Fatal("accepted a path with a symlink ancestor")
	}
	info, err := LstatNoSymlink(filepath.Join(root, "missing.txt"))
	if info != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing path result = %v, %v", info, err)
	}
}

func TestMkdirAllAndOpenRegularDoNotFollowSymlinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real", "nested")
	if err := MkdirAllNoSymlink(realDirectory, 0o750); err != nil {
		t.Fatalf("create real directory: %v", err)
	}
	filePath := filepath.Join(realDirectory, "file.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, info, err := OpenRegularNoSymlink(filePath)
	if err != nil || info.Size() != 7 {
		t.Fatalf("open regular file = %v, %v", info, err)
	}
	file.Close()

	outside := t.TempDir()
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(outside, linked); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	if err := MkdirAllNoSymlink(filepath.Join(linked, "created"), 0o750); err == nil {
		t.Fatal("created directory through symlink ancestor")
	}
	if _, err := os.Stat(filepath.Join(outside, "created")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("linked directory creation escaped root: %v", err)
	}
	if _, _, err := OpenRegularNoSymlink(filepath.Join(linked, "file.txt")); err == nil {
		t.Fatal("opened regular file through symlink ancestor")
	}
	if _, _, err := OpenRegularNoSymlink(filepath.Join(linked, "missing.txt")); err == nil {
		t.Fatal("accepted a missing file below a symlink ancestor")
	}
	linkedFile := filepath.Join(root, "linked-file.txt")
	if err := os.Symlink(filePath, linkedFile); err != nil {
		t.Skipf("creating file symlink is unavailable: %v", err)
	}
	if _, _, err := OpenRegularNoSymlink(linkedFile); err == nil {
		t.Fatal("opened a linked regular file")
	}
}

func TestReplaceRegularFileRejectsLinkedTargetsAndRollback(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	createTemporary := func() string {
		temporary, err := os.CreateTemp(directory, ".replacement-*.tmp")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := temporary.WriteString("replacement"); err != nil {
			temporary.Close()
			t.Fatal(err)
		}
		if err := temporary.Close(); err != nil {
			t.Fatal(err)
		}
		return temporary.Name()
	}

	target := filepath.Join(directory, "target.json")
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	temporary := createTemporary()
	defer os.Remove(temporary)
	if err := ReplaceRegularFileNoSymlink(temporary, target); err == nil {
		t.Fatal("replaced a linked target")
	}
	payload, err := os.ReadFile(outside)
	if err != nil || string(payload) != "outside" {
		t.Fatalf("linked target changed outside file: %q, %v", payload, err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target+".bak"); err != nil {
		t.Fatal(err)
	}
	temporary = createTemporary()
	defer os.Remove(temporary)
	if err := ReplaceRegularFileNoSymlink(temporary, target); err == nil {
		t.Fatal("replaced a target with a linked rollback file")
	}
	payload, err = os.ReadFile(outside)
	if err != nil || string(payload) != "outside" {
		t.Fatalf("linked rollback changed outside file: %q, %v", payload, err)
	}
}

func TestExtractRequiresExistingDestinationDirectory(t *testing.T) {
	t.Parallel()
	archive := createArchive(t, map[string]string{"file.txt": "content"})
	destination := filepath.Join(t.TempDir(), "missing")
	if err := Extract(archive, destination, DefaultLimits()); err == nil {
		t.Fatal("accepted a missing extraction destination")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing destination was created: %v", err)
	}
}

func TestExtractRejectsArchiveFileDirectoryConflictBeforeWriting(t *testing.T) {
	t.Parallel()
	for name, entries := range map[string][]archiveEntry{
		"parent first": {
			{Name: "assets", Body: "file"},
			{Name: "assets/app.css", Body: "child"},
		},
		"child first": {
			{Name: "assets/app.css", Body: "child"},
			{Name: "assets", Body: "file"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			archive := createArchiveEntries(t, entries)
			destination := t.TempDir()
			if err := Extract(archive, destination, DefaultLimits()); err == nil {
				t.Fatal("accepted archive file/directory conflict")
			}
			children, err := os.ReadDir(destination)
			if err != nil {
				t.Fatal(err)
			}
			if len(children) != 0 {
				t.Fatalf("conflicting archive left output behind: %v", children)
			}
		})
	}
}

func TestExtractCleansEarlierOutputsWhenArchiveDataIsCorrupt(t *testing.T) {
	t.Parallel()
	archive := createArchiveEntries(t, []archiveEntry{
		{Name: "first/ok.txt", Body: "first"},
		{Name: "broken.txt", Body: strings.Repeat("0123456789abcdef", 256)},
	})
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	offset, err := reader.File[1].DataOffset()
	if err != nil {
		reader.Close()
		t.Fatal(err)
	}
	compressed := reader.File[1].CompressedSize64
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(archive, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	position := offset + int64(compressed/2)
	buffer := []byte{0}
	if _, err := file.ReadAt(buffer, position); err != nil {
		file.Close()
		t.Fatal(err)
	}
	buffer[0] ^= 0xff
	if _, err := file.WriteAt(buffer, position); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	destination := t.TempDir()
	if err := Extract(archive, destination, DefaultLimits()); err == nil {
		t.Fatal("accepted corrupt archive data")
	}
	children, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 0 {
		t.Fatalf("failed extraction left output behind: %v", children)
	}
}

func createArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	entries := make([]archiveEntry, 0, len(files))
	for name, body := range files {
		entries = append(entries, archiveEntry{Name: name, Body: body})
	}
	return createArchiveEntries(t, entries)
}

func createArchiveEntries(t *testing.T, entries []archiveEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "package.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, item := range entries {
		header := &zip.FileHeader{Name: item.Name, Method: zip.Deflate, NonUTF8: item.NonUTF8}
		if item.Mode != 0 {
			header.SetMode(item.Mode)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(item.Body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
