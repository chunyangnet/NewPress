package packagefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractRejectsAliasedParentDirectoriesBeforeWriting(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries []archiveEntry
	}{
		{"implicit parents", []archiveEntry{{Name: "Assets/one.css", Body: "one"}, {Name: "assets/two.css", Body: "two"}}},
		{"explicit parent first", []archiveEntry{{Name: "Assets/", Mode: os.ModeDir | 0750}, {Name: "assets/one.css", Body: "one"}}},
		{"explicit parent last", []archiveEntry{{Name: "assets/one.css", Body: "one"}, {Name: "Assets/", Mode: os.ModeDir | 0750}}},
		{"nested implicit parents", []archiveEntry{{Name: "assets/Icons/one.svg", Body: "one"}, {Name: "assets/icons/nested/two.svg", Body: "two"}}},
		{"empty child directory", []archiveEntry{{Name: "Assets/empty/", Mode: os.ModeDir | 0750}, {Name: "assets/one.css", Body: "one"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := createArchiveEntries(t, test.entries)
			destination := t.TempDir()
			if err := Extract(archive, destination, DefaultLimits()); err == nil || !strings.Contains(err.Error(), "collide") {
				t.Errorf("directory aliases passed metadata validation: %v", err)
			}
			if children, err := os.ReadDir(destination); err != nil || len(children) != 0 {
				t.Errorf("invalid archive changed the extraction destination: %v, %v", children, err)
			}
		})
	}
}

func TestExtractPreservesConsistentExplicitAndImplicitParents(t *testing.T) {
	for _, directoriesFirst := range []bool{true, false} {
		name := "directories last"
		if directoriesFirst {
			name = "directories first"
		}
		t.Run(name, func(t *testing.T) {
			directories := []archiveEntry{{Name: "Assets/", Mode: os.ModeDir | 0750}, {Name: "Assets/Icons/", Mode: os.ModeDir | 0750}}
			files := []archiveEntry{{Name: "Assets/one.css", Body: "one"}, {Name: "Assets/Icons/two.svg", Body: "two"}, {Name: "Assets/implicit/three.css", Body: "three"}}
			entries := append(append([]archiveEntry{}, directories...), files...)
			if !directoriesFirst {
				entries = append(append([]archiveEntry{}, files...), directories...)
			}
			archive := createArchiveEntries(t, entries)
			destination := t.TempDir()
			if err := Extract(archive, destination, DefaultLimits()); err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				body, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(file.Name)))
				if err != nil || string(body) != file.Body {
					t.Errorf("file %q did not retain its path/content: %q, %v", file.Name, body, err)
				}
			}
		})
	}
}
