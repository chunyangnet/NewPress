package backup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestScanExcludesMediaTemporaryDirectoryAliases(t *testing.T) {
	for _, temporary := range []string{"media/tmp", "media/TMP", "Media/Tmp", "MEDIA/TMP"} {
		t.Run(temporary, func(t *testing.T) {
			root := t.TempDir()
			upload := filepath.Join(root, filepath.FromSlash(temporary), "nested", "upload.bin")
			writeFixtureFile(t, upload, []byte("unfinished upload"))
			kept := path.Dir(temporary) + "/tmp-backup/keep.txt"
			writeFixtureFile(t, filepath.Join(root, filepath.FromSlash(kept)), []byte("keep"))
			directories, files, total, err := scan(t.Context(), []source{{name: "data", path: root}})
			if err != nil {
				t.Fatal(err)
			}
			wantDirectories := []string{"data", "data/" + path.Dir(temporary), "data/" + path.Dir(kept)}
			if !slices.Equal(directories, wantDirectories) || len(files) != 1 || files[0].Path != "data/"+kept || total != 4 {
				t.Errorf("temporary upload was not excluded: directories=%v files=%v total=%d", directories, files, total)
			}
			if body, err := os.ReadFile(upload); err != nil || string(body) != "unfinished upload" {
				t.Fatalf("scan changed source upload: %q, %v", body, err)
			}
		})
	}
}

func TestScanRejectsMediaTemporaryFileAliases(t *testing.T) {
	for _, temporary := range []string{"media/tmp", "media/TMP", "Media/Tmp", "MEDIA/TMP"} {
		t.Run(temporary, func(t *testing.T) {
			root := t.TempDir()
			writeFixtureFile(t, filepath.Join(root, filepath.FromSlash(temporary)), []byte("not a directory"))
			if _, _, _, err := scan(t.Context(), []source{{name: "data", path: root}}); err == nil || !strings.Contains(err.Error(), "temporary path is not a directory") {
				t.Fatalf("invalid temporary path accepted: %v", err)
			}
		})
	}
}

func TestBackupPathRejectsTemporaryMediaAliases(t *testing.T) {
	for _, temporary := range []string{"data/media/tmp", "data/media/TMP", "data/Media/Tmp", "data/MEDIA/TMP"} {
		for _, suffix := range []string{"", "/nested/upload.bin"} {
			name := temporary + "/directory"
			if suffix != "" {
				name = temporary + suffix
			}
			t.Run(name, func(t *testing.T) {
				if err := validatePath(temporary + suffix); err == nil || !strings.Contains(err.Error(), "temporary media") {
					t.Fatalf("temporary media path accepted: %v", err)
				}
			})
		}
	}
}

func TestBackupPathRetainsTemporaryNameLookalikes(t *testing.T) {
	for _, name := range []string{"data/media/tmp-backup/keep.txt", "data/media/TMP.txt", "data/media/objects/tmp/keep.txt", "themes/media/tmp/keep.txt", "plugins/assets/media/tmp/keep.txt"} {
		t.Run(name, func(t *testing.T) {
			if err := validatePath(name); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBackupOmitsTemporaryAliasThroughCreateInspectAndRestore(t *testing.T) {
	fixture := fixtureSite(t, false)
	mediaRoot := filepath.Join(fixture.cfg.DataDir, "media")
	// An intermediate name makes the case-only rename work on either type of filesystem.
	intermediate := filepath.Join(mediaRoot, "temporary-rename")
	if err := os.Rename(filepath.Join(mediaRoot, "tmp"), intermediate); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(intermediate, filepath.Join(mediaRoot, "TMP")); err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(mediaRoot, "TMP", "interrupted-upload.tmp")
	writeFixtureFile(t, upload, []byte("unfinished upload"))
	writeFixtureFile(t, filepath.Join(mediaRoot, "tmp-backup", "keep.txt"), []byte("keep"))
	archivePath := filepath.Join(fixture.parent, "without-temporary.zip")
	created, err := Create(t.Context(), fixture.cfg, archivePath)
	if err != nil || !created.Verified || !created.ApplicationValidated {
		t.Fatalf("create backup: %+v, %v", created, err)
	}
	for _, entry := range readZIP(t, archivePath) {
		if strings.HasPrefix(strings.ToLower(entry.header.Name), "data/media/tmp/") {
			t.Errorf("backup published temporary upload entry %s", entry.header.Name)
		}
	}
	inspected, err := Inspect(t.Context(), archivePath)
	if err != nil || !inspected.Verified || inspected.ArchiveSHA256 != created.ArchiveSHA256 {
		t.Fatalf("inspect backup: %+v, %v", inspected, err)
	}
	destination := filepath.Join(fixture.parent, "restored")
	if _, err := Restore(t.Context(), archivePath, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "data", "media", "TMP", "interrupted-upload.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unfinished upload was restored: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(destination, "data", "media", "tmp-backup", "keep.txt")); err != nil || string(body) != "keep" {
		t.Errorf("lookalike path was not preserved: %q, %v", body, err)
	}
	if body, err := os.ReadFile(upload); err != nil || string(body) != "unfinished upload" {
		t.Errorf("backup changed source upload: %q, %v", body, err)
	}
	assertNoStaging(t, fixture.parent)
}

func TestBackupRejectsArchiveWithTemporaryAlias(t *testing.T) {
	fixture := fixtureSite(t, false)
	archivePath := filepath.Join(fixture.parent, "valid.zip")
	if _, err := Create(t.Context(), fixture.cfg, archivePath); err != nil {
		t.Fatal(err)
	}
	for _, withUpload := range []bool{false, true} {
		name := "empty directory"
		if withUpload {
			name = "unfinished upload"
		}
		t.Run(name, func(t *testing.T) {
			entries := readZIP(t, archivePath)
			var manifest Manifest
			if err := json.Unmarshal(entries[0].body, &manifest); err != nil {
				t.Fatal(err)
			}
			directory := "data/media/TMP"
			directoryHeader := zip.FileHeader{Name: directory + "/", Method: zip.Store}
			directoryHeader.SetMode(os.ModeDir | 0o700)
			entries = append(entries, zipEntry{header: directoryHeader})
			manifest.Directories = append(manifest.Directories, directory)
			sort.Strings(manifest.Directories)
			if withUpload {
				body := []byte("unfinished upload")
				fileHeader := zip.FileHeader{Name: directory + "/upload.bin", Method: zip.Deflate}
				fileHeader.SetMode(0o600)
				entries = append(entries, zipEntry{header: fileHeader, body: body})
				digest := sha256.Sum256(body)
				manifest.Files = append(manifest.Files, File{Path: fileHeader.Name, Size: int64(len(body)), SHA256: hex.EncodeToString(digest[:])})
				sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
				manifest.TotalBytes += int64(len(body))
			}
			manifest.ManifestSHA256 = manifestDigest(manifest)
			var err error
			entries[0].body, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			bad := filepath.Join(t.TempDir(), "temporary-media.zip")
			writeZIP(t, bad, entries)
			before, err := os.ReadFile(bad)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Inspect(t.Context(), bad); err == nil || !strings.Contains(err.Error(), "temporary media") {
				t.Errorf("inspection accepted excluded directory: %v", err)
			}
			for _, existing := range []bool{false, true} {
				parent := t.TempDir()
				destination := filepath.Join(parent, "restore")
				if existing {
					if err := os.Mkdir(destination, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := Restore(t.Context(), bad, destination); err == nil || !strings.Contains(err.Error(), "temporary media") {
					t.Errorf("restore accepted excluded directory (existing=%v): %v", existing, err)
				}
				if existing {
					if entries, err := os.ReadDir(destination); err != nil || len(entries) != 0 {
						t.Errorf("failed restore changed empty target: %v, %v", entries, err)
					}
				} else if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("failed restore published a target: %v", err)
				}
				assertNoStaging(t, parent)
			}
			if after, err := os.ReadFile(bad); err != nil || !bytes.Equal(before, after) {
				t.Errorf("validation changed the input archive: %v", err)
			}
		})
	}
}
