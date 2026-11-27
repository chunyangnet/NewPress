package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/packagefs"
)

type source struct {
	name string
	path string
}

func sources(cfg config.Config) ([]source, error) {
	if cfg.DatabaseURL != "" {
		return nil, errors.New("local backup requires file storage; use PostgreSQL backup tools together with media and deployment backups for database mode")
	}
	result := []source{{name: "data", path: cfg.DataDir}, {name: "themes", path: cfg.ThemesDir}, {name: "plugins", path: cfg.PluginsDir}}
	for index := range result {
		if strings.TrimSpace(result[index].path) == "" {
			return nil, errors.New("backup source directory is empty")
		}
		absolute, err := filepath.Abs(result[index].path)
		if err != nil {
			return nil, err
		}
		info, err := packagefs.LstatNoSymlink(absolute)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("backup source %s must be an existing real directory", result[index].name)
		}
		result[index].path = absolute
	}
	for left := range result {
		for right := left + 1; right < len(result); right++ {
			if within(result[left].path, result[right].path) || within(result[right].path, result[left].path) {
				return nil, errors.New("backup data, themes and plugins directories must be distinct and must not contain each other")
			}
		}
	}
	return result, nil
}

func within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func scan(ctx context.Context, roots []source) ([]string, []File, int64, error) {
	directories := make([]string, 0)
	files := make([]File, 0)
	var total int64
	for _, root := range roots {
		err := filepath.WalkDir(root.path, func(fullPath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("backup cannot include symbolic links: %s", fullPath)
			}
			relative, err := filepath.Rel(root.path, fullPath)
			if err != nil {
				return err
			}
			name := root.name
			if relative != "." {
				name += "/" + filepath.ToSlash(relative)
			}
			if isSessionPath(name) {
				if !entry.IsDir() {
					return errors.New("session storage path is not a directory")
				}
				return filepath.SkipDir
			}
			if strings.EqualFold(entry.Name(), dirlock.FileName) {
				if entry.IsDir() {
					return errors.New("process lock path is a directory")
				}
				return nil
			}
			if isTemporaryMediaPath(name) {
				if !entry.IsDir() {
					return errors.New("media temporary path is not a directory")
				}
				return filepath.SkipDir
			}
			if err := validatePath(name); err != nil {
				return err
			}
			if len(files)+len(directories) >= MaxEntries {
				return fmt.Errorf("backup exceeds %d entries", MaxEntries)
			}
			if entry.IsDir() {
				if _, err := packagefs.LstatNoSymlink(fullPath); err != nil {
					return err
				}
				directories = append(directories, name)
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("backup cannot include special files: %s", name)
			}
			file, err := copySource(ctx, fullPath, io.Discard)
			if err != nil {
				return fmt.Errorf("read backup file %s: %w", name, err)
			}
			file.Path = name
			if total > MaxTotalBytes-file.Size {
				return errors.New("backup exceeds 4 GiB expanded limit")
			}
			total += file.Size
			files = append(files, file)
			return nil
		})
		if err != nil {
			return nil, nil, 0, err
		}
	}
	sort.Strings(directories)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return directories, files, total, nil
}

func copySource(ctx context.Context, path string, target io.Writer) (File, error) {
	file, info, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return File{}, err
	}
	defer file.Close()
	if info.Size() < 0 || info.Size() > MaxFileBytes {
		return File{}, errors.New("file exceeds 512 MiB backup limit")
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(target, digest), io.LimitReader(contextReader{ctx, file}, info.Size()+1))
	if err != nil {
		return File{}, err
	}
	if err := stableFile(path, file, info, written); err != nil {
		return File{}, err
	}
	return File{Size: written, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func stableFile(path string, file *os.File, before os.FileInfo, read int64) error {
	after, statErr := file.Stat()
	current, pathErr := packagefs.LstatNoSymlink(path)
	if statErr != nil || pathErr != nil || read != before.Size() || !after.Mode().IsRegular() || !current.Mode().IsRegular() ||
		!os.SameFile(before, after) || !os.SameFile(before, current) || before.Size() != after.Size() || before.Size() != current.Size() ||
		!before.ModTime().Equal(after.ModTime()) || !before.ModTime().Equal(current.ModTime()) {
		return errors.New("file changed during backup verification")
	}
	return nil
}

func matchesInventory(ctx context.Context, roots []source, manifest Manifest) error {
	directories, files, total, err := scan(ctx, roots)
	if err != nil {
		return err
	}
	if total != manifest.TotalBytes || !reflect.DeepEqual(directories, manifest.Directories) || !reflect.DeepEqual(files, manifest.Files) {
		return errors.New("site files changed or require recovery; stop writers and start the site successfully before creating a new backup")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(payload []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(payload)
}

func newStaging(parent string) (string, func(), error) {
	if _, err := packagefs.LstatNoSymlink(parent); err != nil {
		return "", nil, err
	}
	directory, err := os.MkdirTemp(parent, ".newpress-restore-")
	if err != nil {
		return "", nil, err
	}
	identity, err := packagefs.LstatNoSymlink(directory)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		// Remove only the exact directory we created, still beneath the checked
		// parent. Never traverse an exchanged root or a user-supplied target.
		current, err := packagefs.LstatNoSymlink(directory)
		if err == nil && current.IsDir() && os.SameFile(identity, current) &&
			filepath.Dir(directory) == parent && strings.HasPrefix(filepath.Base(directory), ".newpress-restore-") {
			_ = os.RemoveAll(directory)
		}
	}
	return directory, cleanup, nil
}

func syncDirectory(directory string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := packagefs.OpenDirectoryNoSymlink(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
