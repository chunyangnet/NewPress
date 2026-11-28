package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

func (m *Manager) Open(id int64) (Item, ReadSeekCloser, error) {
	return m.open(id, false)
}

// OpenForManagement includes retained trash objects. Callers must enforce
// management authorization and private, non-storable response headers.
func (m *Manager) OpenForManagement(id int64) (Item, ReadSeekCloser, error) {
	return m.open(id, true)
}

func (m *Manager) open(id int64, includeTrash bool) (Item, ReadSeekCloser, error) {
	if m == nil || id <= 0 {
		return Item{}, nil, ErrNotFound
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, exists := m.items[id]
	if !exists || (!includeTrash && item.Status() == StatusTrash) {
		return Item{}, nil, ErrNotFound
	}
	return m.openObjectLocked(item)
}

func (m *Manager) openObjectLocked(item Item) (Item, ReadSeekCloser, error) {
	if m.root == "" {
		payload, ok := m.memoryObjects[item.ObjectKey]
		payload = append([]byte(nil), payload...)
		if !ok {
			return item, nil, ErrObjectMissing
		}
		return item, &memoryObject{Reader: bytes.NewReader(payload)}, nil
	}
	target, err := m.objectPath(item.ObjectKey)
	if err != nil {
		return item, nil, err
	}
	file, info, err := packagefs.OpenRegularNoSymlink(target)
	if errors.Is(err, os.ErrNotExist) {
		return item, nil, ErrObjectMissing
	}
	if err != nil {
		return item, nil, fmt.Errorf("open media object: %w", err)
	}
	if info.Size() != item.SizeBytes {
		_ = file.Close()
		return item, nil, ErrObjectMissing
	}
	return item, file, nil
}

func (m *Manager) commitObject(ctx context.Context, staged *stagedUpload, item Item) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if m.root == "" {
		if payload, exists := m.memoryObjects[item.ObjectKey]; exists {
			if !bytes.Equal(payload, staged.payload) {
				return false, ErrObjectCorrupt
			}
			return false, nil
		}
		m.memoryObjects[item.ObjectKey] = append([]byte(nil), staged.payload...)
		return true, nil
	}
	target, err := m.objectPath(item.ObjectKey)
	if err != nil {
		return false, err
	}
	if err := packagefs.MkdirAllNoSymlink(filepath.Dir(target), 0o750); err != nil {
		return false, fmt.Errorf("create media object directory: %w", err)
	}
	if file, info, err := packagefs.OpenRegularNoSymlink(target); err == nil {
		if !info.Mode().IsRegular() || info.Size() != item.SizeBytes {
			_ = file.Close()
			return false, ErrObjectCorrupt
		}
		// A retained original can survive an interrupted index commit or
		// index recovery. Its name and size alone do not verify its bytes.
		verifyErr := verifyObjectContent(ctx, file, item)
		closeErr := file.Close()
		if verifyErr != nil {
			return false, verifyErr
		}
		if closeErr != nil {
			return false, fmt.Errorf("close existing media object: %w", closeErr)
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect media object: %w", err)
	}
	if err := renameWithWindowsRetry(staged.path, target); err != nil {
		return false, fmt.Errorf("commit media object: %w", err)
	}
	staged.path = ""
	if err := packagefs.ChmodRegularNoSymlink(target, 0o640); err != nil {
		_ = os.Remove(target)
		return false, fmt.Errorf("set media object permissions: %w", err)
	}
	return true, nil
}

func (m *Manager) verifyDuplicateObjectLocked(ctx context.Context, staged *stagedUpload, item Item) error {
	if m.root == "" {
		payload, exists := m.memoryObjects[item.ObjectKey]
		if !exists {
			return ErrObjectMissing
		}
		if !bytes.Equal(payload, staged.payload) {
			return ErrObjectCorrupt
		}
		return ctx.Err()
	}
	_, reader, err := m.openObjectLocked(item)
	if err != nil {
		return err
	}
	verifyErr := verifyObjectContent(ctx, reader, item)
	closeErr := reader.Close()
	if verifyErr != nil {
		return verifyErr
	}
	return closeErr
}

func verifyObjectContent(ctx context.Context, reader io.Reader, item Item) error {
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(contextReader{context: ctx, reader: reader}, item.SizeBytes+1))
	if err != nil {
		return fmt.Errorf("verify existing media object: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if size != item.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != item.ChecksumSHA256 {
		return ErrObjectCorrupt
	}
	return nil
}

func (m *Manager) stageObjectDeletionLocked(item Item) (func(bool), error) {
	if m.root == "" {
		payload, ok := m.memoryObjects[item.ObjectKey]
		if !ok {
			return nil, ErrObjectMissing
		}
		delete(m.memoryObjects, item.ObjectKey)
		return func(commit bool) {
			if !commit {
				m.memoryObjects[item.ObjectKey] = payload
			}
		}, nil
	}
	target, err := m.objectPath(item.ObjectKey)
	if err != nil {
		return nil, err
	}
	file, _, err := packagefs.OpenRegularNoSymlink(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrObjectMissing
	}
	if err != nil {
		return nil, fmt.Errorf("inspect media object for deletion: %w", err)
	}
	_ = file.Close()
	if _, err := packagefs.LstatNoSymlink(filepath.Join(m.root, "tmp")); err != nil {
		return nil, fmt.Errorf("inspect media temporary directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Join(m.root, "tmp"), ".delete-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("reserve media deletion path: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, err
	}
	_ = os.Remove(temporaryPath)
	if err := renameWithWindowsRetry(target, temporaryPath); errors.Is(err, os.ErrNotExist) {
		return nil, ErrObjectMissing
	} else if err != nil {
		return nil, fmt.Errorf("stage media object deletion: %w", err)
	}
	return func(commit bool) {
		if commit {
			_ = os.Remove(temporaryPath)
			return
		}
		_ = renameWithWindowsRetry(temporaryPath, target)
	}, nil
}

// renameWithWindowsRetry absorbs the short sharing violations that Windows can
// report while antivirus software or a just-finished HTTP response releases a
// file handle. Other platforms and non-transient errors keep os.Rename's
// immediate behavior.
func renameWithWindowsRetry(oldPath, newPath string) error {
	const attempts = 6
	for attempt := 0; ; attempt++ {
		err := os.Rename(oldPath, newPath)
		if err == nil || runtime.GOOS != "windows" || attempt+1 >= attempts || !transientWindowsFileError(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
}

func transientWindowsFileError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	// ERROR_ACCESS_DENIED, ERROR_SHARING_VIOLATION and ERROR_LOCK_VIOLATION.
	return errno == 5 || errno == 32 || errno == 33
}

func (m *Manager) removeObjectLocked(item Item) error {
	if m.root == "" {
		delete(m.memoryObjects, item.ObjectKey)
		return nil
	}
	target, err := m.objectPath(item.ObjectKey)
	if err != nil {
		return err
	}
	if err := packagefs.ValidateRegularFileTargetNoSymlink(target); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *Manager) objectPath(objectKey string) (string, error) {
	if m.root == "" {
		return "", nil
	}
	if err := validateObjectKey(objectKey); err != nil {
		return "", err
	}
	target := filepath.Join(m.root, filepath.FromSlash(objectKey))
	relative, err := filepath.Rel(m.root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("media object path escapes library")
	}
	return target, nil
}
