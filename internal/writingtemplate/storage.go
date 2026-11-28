package writingtemplate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"newpress.local/newpress/internal/packagefs"
)

type MemoryStorage struct {
	mu  sync.Mutex
	raw []byte
}

func (s *MemoryStorage) Read(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return bytes.Clone(s.raw), nil
}

func (s *MemoryStorage) Write(ctx context.Context, expected [sha256.Size]byte, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if sha256.Sum256(s.raw) != expected {
		return ErrConflict
	}
	if err := ValidateSnapshot(raw); err != nil {
		return err
	}
	s.raw = bytes.Clone(raw)
	return nil
}

// FileStorage relies on the application's lifetime directory lock. Its mutex
// coordinates this instance; it does not turn an unlocked shared file into a
// multi-process database. A changed primary is detected before publication.
type FileStorage struct {
	mu       sync.Mutex
	path     string
	observed bool
}

func NewFileStorage(path string) (*FileStorage, error) {
	if path == "" {
		return nil, ErrInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return &FileStorage{path: absolute}, nil
}

func (s *FileStorage) read() ([]byte, error) {
	f, before, err := packagefs.OpenRegularNoSymlink(s.path)
	if errors.Is(err, os.ErrNotExist) {
		if s.observed {
			return nil, ErrCorrupt
		}
		if _, backupErr := packagefs.LstatNoSymlink(s.path + ".bak"); !errors.Is(backupErr, os.ErrNotExist) {
			return nil, ErrCorrupt
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if before.Size() < 1 || before.Size() > MaxLibraryBytes {
		return nil, ErrCorrupt
	}
	// The checked size is bounded. One extra byte detects growth while avoiding
	// ReadAll's repeated buffer growth for a library near the byte limit.
	raw := make([]byte, int(before.Size())+1)
	n, err := io.ReadFull(f, raw)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	raw = raw[:n]
	after, err := packagefs.LstatNoSymlink(s.path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || before.Size() != int64(len(raw)) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, ErrConflict
	}
	s.observed = true
	return raw, nil
}

func (s *FileStorage) Read(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.read()
}

func (s *FileStorage) Write(ctx context.Context, expected [sha256.Size]byte, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := decodeLibrary(raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	previous, err := s.read()
	if err != nil {
		return err
	}
	if sha256.Sum256(previous) != expected {
		return ErrConflict
	}
	dir := filepath.Dir(s.path)
	if err = packagefs.MkdirAllNoSymlink(dir, 0750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".newpress-writing-templates-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Writing the temporary file may take time; check the primary again before
	// the existing atomic replacement, without accepting an older .bak.
	current, err := s.read()
	if err != nil {
		return err
	}
	if !bytes.Equal(current, previous) {
		return ErrConflict
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = packagefs.ReplaceRegularFileNoSymlink(f.Name(), s.path); err != nil {
		return err
	}
	s.observed = true
	if runtime.GOOS != "windows" {
		if d, e := packagefs.OpenDirectoryNoSymlink(dir); e == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return nil
}
