package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/dirlock"
)

func TestCoreUpdateThemeSnapshotExcludesProcessLocks(t *testing.T) {
	for _, spelling := range []string{dirlock.FileName, ".NEWPRESS.LOCK", ".NewPress.Lock"} {
		for _, held := range []bool{false, true} {
			name := spelling + "/unlocked"
			if held {
				name = spelling + "/held"
			}
			t.Run(name, func(t *testing.T) {
				manager := managerFixture(t)
				nested := filepath.Join(manager.themes, "custom")
				if err := os.Mkdir(nested, 0o700); err != nil {
					t.Fatal(err)
				}
				contents := map[string]string{
					"custom.txt":                   "keep custom theme",
					"custom/template.html":         "<main>custom template</main>",
					".newpress.lock.backup":        "user file with a similar name",
					"custom/.NEWPRESS.LOCK.backup": "another user file",
				}
				for rel, body := range contents {
					if err := os.WriteFile(filepath.Join(manager.themes, filepath.FromSlash(rel)), []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				const marker = "existing lock metadata\n"
				for _, dir := range []string{manager.themes, nested} {
					if err := os.WriteFile(filepath.Join(dir, spelling), []byte(marker), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				var locks *dirlock.Set
				if held {
					var err error
					locks, err = dirlock.Acquire(manager.themes, nested)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = locks.Close() })
				}
				archive := filepath.Join(t.TempDir(), "update.zip")
				if err := os.WriteFile(archive, zipFixture(t, "bin/"+binaryName()), 0o600); err != nil {
					t.Fatal(err)
				}
				release, err := stageArchive(t.Context(), archive, manager.root, randomID(), manager.themes)
				if err != nil {
					t.Errorf("process lock prevented theme snapshot: %v", err)
				} else {
					snapshot := releaseThemes(manager.root, release, "")
					for rel, want := range contents {
						if got, err := os.ReadFile(filepath.Join(snapshot, filepath.FromSlash(rel))); err != nil || string(got) != want {
							t.Errorf("snapshot changed user file %q: %q, %v", rel, got, err)
						}
					}
					if err := filepath.WalkDir(snapshot, func(path string, entry fs.DirEntry, err error) error {
						if err != nil {
							return err
						}
						if strings.EqualFold(entry.Name(), dirlock.FileName) {
							t.Errorf("snapshot retained a process lock: %s", path)
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if got, err := os.ReadFile(executable(manager.root, release, "")); err != nil || string(got) != "fixture content" {
						t.Errorf("staged program changed: %q, %v", got, err)
					}
				}
				if held {
					other, err := dirlock.Acquire(manager.themes, nested)
					_ = other.Close()
					if !errors.Is(err, dirlock.ErrBusy) {
						t.Errorf("snapshot disturbed lock ownership: %v", err)
					}
					if err := locks.Close(); err != nil {
						t.Fatal(err)
					}
				}
				for _, dir := range []string{manager.themes, nested} {
					if got, err := os.ReadFile(filepath.Join(dir, spelling)); err != nil || string(got) != marker {
						t.Errorf("source lock was changed: %q, %v", got, err)
					}
				}
				for rel, want := range contents {
					if got, err := os.ReadFile(filepath.Join(manager.themes, filepath.FromSlash(rel))); err != nil || string(got) != want {
						t.Errorf("source file %q was changed: %q, %v", rel, got, err)
					}
				}
			})
		}
	}
}

func TestCoreUpdateRejectsInvalidThemeLockPaths(t *testing.T) {
	for _, spelling := range []string{dirlock.FileName, ".NEWPRESS.LOCK", ".NewPress.Lock"} {
		for _, kind := range []string{"empty directory", "nonempty directory", "symlink"} {
			t.Run(spelling+"/"+kind, func(t *testing.T) {
				manager := managerFixture(t)
				lockPath := filepath.Join(manager.themes, "custom", spelling)
				if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
					t.Fatal(err)
				}
				const marker = "preserve invalid lock input"
				var retained string
				if kind == "symlink" {
					retained = filepath.Join(t.TempDir(), "outside.txt")
					if err := os.WriteFile(retained, []byte(marker), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(retained, lockPath); err != nil {
						t.Skipf("symbolic links unavailable: %v", err)
					}
				} else {
					if err := os.Mkdir(lockPath, 0o700); err != nil {
						t.Fatal(err)
					}
					if kind == "nonempty directory" {
						retained = filepath.Join(lockPath, "child.txt")
						if err := os.WriteFile(retained, []byte(marker), 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
				archive := filepath.Join(t.TempDir(), "update.zip")
				if err := os.WriteFile(archive, zipFixture(t, "bin/"+binaryName()), 0o600); err != nil {
					t.Fatal(err)
				}
				id := randomID()
				if _, err := stageArchive(t.Context(), archive, manager.root, id, manager.themes); err == nil {
					t.Error("invalid theme lock path was accepted")
				}
				if _, err := os.Stat(filepath.Join(manager.root, "releases", id)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("failed stage left a release directory: %v", err)
				}
				if _, err := os.Lstat(lockPath); err != nil {
					t.Errorf("invalid source lock path changed: %v", err)
				}
				if retained != "" {
					if body, err := os.ReadFile(retained); err != nil || string(body) != marker {
						t.Errorf("source content changed: %q, %v", body, err)
					}
				}
				if body, err := os.ReadFile(filepath.Join(manager.themes, "custom.txt")); err != nil || string(body) != "keep custom theme" {
					t.Errorf("original theme changed: %q, %v", body, err)
				}
			})
		}
	}
}

func TestCoreUpdateAllowsThemeRootNamedLikeLock(t *testing.T) {
	for _, name := range []string{dirlock.FileName, ".NEWPRESS.LOCK", ".NewPress.Lock"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			themes := filepath.Join(t.TempDir(), name)
			if err := os.Mkdir(themes, 0o700); err != nil {
				t.Fatal(err)
			}
			const content = "theme directory may use an arbitrary basename"
			if err := os.WriteFile(filepath.Join(themes, "custom.txt"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "update.zip")
			if err := os.WriteFile(archive, zipFixture(t, "bin/"+binaryName()), 0o600); err != nil {
				t.Fatal(err)
			}
			release, err := stageArchive(t.Context(), archive, root, randomID(), themes)
			if err != nil {
				t.Fatal(err)
			}
			if body, err := os.ReadFile(filepath.Join(releaseThemes(root, release, ""), "custom.txt")); err != nil || string(body) != content {
				t.Errorf("theme root content changed: %q, %v", body, err)
			}
		})
	}
}
