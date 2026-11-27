package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

type FileStore struct {
	mu           sync.Mutex
	path         string
	state        State
	digest       [32]byte
	exists       bool
	locationMu   sync.RWMutex
	locationFunc func() *time.Location
}

// SetLocationFunc keeps retention and batch validation on the configured site
// calendar instead of UTC.
func (s *FileStore) SetLocationFunc(location func() *time.Location) {
	if s == nil {
		return
	}
	s.locationMu.Lock()
	s.locationFunc = location
	s.locationMu.Unlock()
}

func (s *FileStore) zone() *time.Location {
	s.locationMu.RLock()
	fn := s.locationFunc
	s.locationMu.RUnlock()
	if fn != nil {
		if location := fn(); location != nil {
			return location
		}
	}
	return time.UTC
}

func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{path: path, state: EmptyState()}
	if path == "" {
		return s, nil
	}
	body, exists, err := readFile(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return s, nil
	}
	s.state, err = Decode(body)
	if err != nil {
		return nil, err
	}
	s.digest, s.exists = sha256.Sum256(body), true
	return s, nil
}
func readFile(path string) ([]byte, bool, error) {
	f, info, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if info.Size() > MaxDocumentBytes {
		return nil, false, ErrCapacity
	}
	body, err := io.ReadAll(io.LimitReader(f, MaxDocumentBytes+1))
	if len(body) > MaxDocumentBytes {
		return nil, false, ErrCapacity
	}
	return body, true, err
}
func (s *FileStore) unchanged() error {
	if s.path == "" {
		return nil
	}
	body, exists, err := readFile(s.path)
	if err != nil {
		return err
	}
	if exists != s.exists || exists && sha256.Sum256(body) != s.digest {
		return ErrChanged
	}
	return nil
}
func (s *FileStore) Snapshot(ctx context.Context, now time.Time) (State, error) {
	location := operationLocation(ctx, s.zone)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if err := s.unchanged(); err != nil {
		return State{}, err
	}
	next := Clone(s.state)
	PruneIn(&next, now, location)
	return next, nil
}
func (s *FileStore) Apply(ctx context.Context, b Batch, now time.Time) error {
	location := operationLocation(ctx, s.zone)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.unchanged(); err != nil {
		return err
	}
	next, changed, err := MergeIn(s.state, b, now, location)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	body, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(body) > MaxDocumentBytes {
		return ErrCapacity
	}
	if s.path != "" {
		parent := filepath.Dir(s.path)
		if err := os.MkdirAll(parent, 0700); err != nil {
			return err
		}
		if _, err := packagefs.LstatNoSymlink(parent); err != nil {
			return err
		}
		f, err := os.CreateTemp(parent, ".analytics-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if err = f.Chmod(0600); err == nil {
			_, err = f.Write(body)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.unchanged(); err != nil {
			return err
		}
		if err := packagefs.ReplaceRegularFileNoSymlink(f.Name(), s.path); err != nil {
			return err
		}
		s.digest, s.exists = sha256.Sum256(body), true
	}
	s.state = next
	return nil
}
