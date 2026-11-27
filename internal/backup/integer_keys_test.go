package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRestoreRejectsAliasedIntegerKeysBeyondArchiveDigests(t *testing.T) {
	fixture := fixtureSite(t, true)
	archivePath := filepath.Join(fixture.parent, "valid.zip")
	if _, err := Create(t.Context(), fixture.cfg, archivePath); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ file, field string }{
		{"data/notifications.json", "preferences"},
		{"data/analytics.json", "posts"},
	} {
		t.Run(test.field, func(t *testing.T) {
			entries := readZIP(t, archivePath)
			var manifest Manifest
			if err := json.Unmarshal(entries[0].body, &manifest); err != nil {
				t.Fatal(err)
			}
			changed := false
			for index := range entries {
				if entries[index].header.Name != test.file {
					continue
				}
				entry := &entries[index]
				pattern := regexp.MustCompile(`"` + test.field + `":\{"([0-9]+)":`)
				matches := pattern.FindSubmatchIndex(entry.body)
				if matches == nil {
					t.Fatal("fixture has no integer-keyed data")
				}
				key := string(entry.body[matches[2]:matches[3]])
				var value json.RawMessage
				if err := json.NewDecoder(bytes.NewReader(entry.body[matches[1]:])).Decode(&value); err != nil {
					t.Fatal(err)
				}
				injection := []byte(fmt.Sprintf(`%q:{%q:%s,%q:`, test.field, "0"+key, value, key))
				beforeSize := int64(len(entry.body))
				entry.body = bytes.Replace(entry.body, entry.body[matches[0]:matches[1]], injection, 1)
				checksum := sha256.Sum256(entry.body)
				for item := range manifest.Files {
					if manifest.Files[item].Path == test.file {
						manifest.Files[item].Size = int64(len(entry.body))
						manifest.Files[item].SHA256 = hex.EncodeToString(checksum[:])
					}
				}
				manifest.TotalBytes += int64(len(entry.body)) - beforeSize
				changed = true
			}
			if !changed {
				t.Fatal("fixture file missing")
			}
			manifest.ManifestSHA256 = manifestDigest(manifest)
			var err error
			entries[0].body, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			bad := filepath.Join(t.TempDir(), "aliased-integer.zip")
			writeZIP(t, bad, entries)
			if _, err := Inspect(t.Context(), bad); err != nil {
				t.Fatalf("recomputed archive checksums must pass: %v", err)
			}
			before, err := os.ReadFile(bad)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(t.TempDir(), "restored")
			if _, err := Restore(t.Context(), bad, destination); err == nil || !strings.Contains(err.Error(), "duplicate JSON field") {
				t.Errorf("ambiguous data reached restore publication: %v", err)
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Error("failed validation published destination")
			}
			assertNoStaging(t, filepath.Dir(destination))
			if after, err := os.ReadFile(bad); err != nil || !bytes.Equal(before, after) {
				t.Errorf("restore changed backup source: %v", err)
			}
		})
	}
}
