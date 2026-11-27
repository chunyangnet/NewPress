package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreRejectsLossyUnicodeBeyondArchiveDigests(t *testing.T) {
	fixture := fixtureSite(t, true)
	archivePath := filepath.Join(fixture.parent, "valid.zip")
	if _, err := Create(t.Context(), fixture.cfg, archivePath); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, title string }{
		{"invalid UTF8", "\"restore-\xff\""},
		{"high surrogate", `"restore-\ud800"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries := readZIP(t, archivePath)
			var manifest Manifest
			if err := json.Unmarshal(entries[0].body, &manifest); err != nil {
				t.Fatal(err)
			}
			changed := false
			for index := range entries {
				if entries[index].header.Name != "data/content.json" {
					continue
				}
				entry := &entries[index]
				before := entry.body
				entry.body = bytes.ReplaceAll(before, []byte(`"备份文章第二版"`), []byte(test.title))
				if bytes.Equal(before, entry.body) {
					t.Fatal("fixture did not change content title")
				}
				checksum := sha256.Sum256(entry.body)
				for item := range manifest.Files {
					if manifest.Files[item].Path == entry.header.Name {
						manifest.Files[item].Size = int64(len(entry.body))
						manifest.Files[item].SHA256 = hex.EncodeToString(checksum[:])
					}
				}
				manifest.TotalBytes += int64(len(entry.body) - len(before))
				changed = true
			}
			if !changed {
				t.Fatal("fixture has no content file")
			}
			manifest.ManifestSHA256 = manifestDigest(manifest)
			var err error
			entries[0].body, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			bad := filepath.Join(t.TempDir(), "lossy-unicode.zip")
			writeZIP(t, bad, entries)
			if _, err := Inspect(t.Context(), bad); err != nil {
				t.Fatalf("archive checksums should be consistent: %v", err)
			}
			before, err := os.ReadFile(bad)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(t.TempDir(), "restored")
			if _, err := Restore(t.Context(), bad, destination); err == nil {
				t.Error("lossy content reached restore publication")
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Error("failed validation published a destination")
			}
			assertNoStaging(t, filepath.Dir(destination))
			if after, err := os.ReadFile(bad); err != nil || !bytes.Equal(before, after) {
				t.Errorf("restore changed the backup source: %v", err)
			}
		})
	}
}
