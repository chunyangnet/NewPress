// Package dirlock coordinates NewPress processes that use the same local
// files. Lock files are permanent; ownership belongs to the open OS handle,
// never to a PID or the presence of the file.
package dirlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"newpress.local/newpress/internal/packagefs"
)

const FileName = ".newpress.lock"

var ErrBusy = errors.New("NewPress directory is in use; stop the application and other maintenance commands before retrying")

type Set struct {
	files []*os.File
	once  sync.Once
	err   error
}

// Acquire obtains nonblocking exclusive locks in a stable order. A failed
// attempt releases every lock it already acquired. All participants must use
// this protocol; it cannot prevent an external editor from changing files.
func Acquire(directories ...string) (*Set, error) {
	paths := make([]string, 0, len(directories))
	seen := make(map[string]bool)
	for _, directory := range directories {
		if strings.TrimSpace(directory) == "" {
			return nil, errors.New("lock directory must not be empty")
		}
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return nil, err
		}
		key := absolute
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			paths = append(paths, absolute)
			seen[key] = true
		}
	}
	sort.Strings(paths)
	set := &Set{}
	for _, directory := range paths {
		file, err := acquire(directory)
		if err != nil {
			_ = set.Close()
			return nil, fmt.Errorf("lock directory %q: %w", directory, err)
		}
		set.files = append(set.files, file)
	}
	return set, nil
}

func acquire(directory string) (*os.File, error) {
	if err := packagefs.MkdirAllNoSymlink(directory, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, FileName)
	if err := packagefs.ValidateRegularFileTargetNoSymlink(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	actual, statErr := file.Stat()
	current, pathErr := packagefs.LstatNoSymlink(path)
	if statErr != nil || pathErr != nil || !actual.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(actual, current) {
		_ = file.Close()
		return nil, errors.New("lock file identity changed while acquiring ownership")
	}
	return file, nil
}

// Close releases the OS locks and is safe to call more than once. Deliberately
// do not remove the files: unlinking a lock could let another process lock a
// different inode while an existing owner still holds the original handle.
func (set *Set) Close() error {
	if set == nil {
		return nil
	}
	set.once.Do(func() {
		for index := len(set.files) - 1; index >= 0; index-- {
			set.err = errors.Join(set.err, set.files[index].Close())
		}
	})
	return set.err
}
