package questions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/packagefs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileStore publishes a complete snapshot only after durable replacement succeeds.
type FileStore struct {
	mu     sync.Mutex
	path   string
	state  State
	digest [32]byte
	exists bool
}

func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{path: path, state: Empty()}
	if path == "" {
		return s, nil
	}
	b, exists, e := read(path)
	if e != nil {
		return nil, e
	}
	if exists {
		s.state, e = Decode(b)
		if e != nil {
			return nil, e
		}
	}
	s.exists = exists
	s.digest = sha256.Sum256(b)
	return s, nil
}
func read(path string) ([]byte, bool, error) {
	f, info, e := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(e, os.ErrNotExist) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	defer f.Close()
	if info.Size() > MaxBytes {
		return nil, false, ErrCapacity
	}
	b, e := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if len(b) > MaxBytes {
		return nil, false, ErrCapacity
	}
	return b, true, e
}
func (s *FileStore) unchanged() error {
	if s.path == "" {
		return nil
	}
	b, exists, e := read(s.path)
	if e != nil {
		return e
	}
	if exists != s.exists || sha256.Sum256(b) != s.digest {
		return ErrConflict
	}
	return nil
}
func (s *FileStore) Ready(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	return s.unchanged()
}
func (s *FileStore) Snapshot(ctx context.Context) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return State{}, e
	}
	if e := s.unchanged(); e != nil {
		return State{}, e
	}
	return Clone(s.state), nil
}
func (s *FileStore) commit(ctx context.Context, next State) error {
	b, e := json.Marshal(next)
	if e != nil {
		return e
	}
	if len(b) > MaxBytes {
		return ErrCapacity
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = s.unchanged(); e != nil {
		return e
	}
	if s.path != "" {
		parent := filepath.Dir(s.path)
		if e = packagefs.MkdirAllNoSymlink(parent, 0700); e != nil {
			return e
		}
		f, e := os.CreateTemp(parent, ".questions-*")
		if e != nil {
			return e
		}
		defer os.Remove(f.Name())
		if e = f.Chmod(0600); e == nil {
			_, e = f.Write(b)
		}
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if e = s.unchanged(); e != nil {
			return e
		}
		if e = packagefs.ReplaceRegularFileNoSymlink(f.Name(), s.path); e != nil {
			return e
		}
		s.digest = sha256.Sum256(b)
		s.exists = true
	}
	s.state = next
	return nil
}
func (s *FileStore) Apply(ctx context.Context, id int64, a Actor, c Command, now time.Time) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return Result{}, e
	}
	if e := s.unchanged(); e != nil {
		return Result{}, e
	}
	next, r, e := Apply(s.state, id, a, c, now)
	if e != nil || r.Replay {
		return r, e
	}
	if e = s.commit(ctx, next); e != nil {
		return Result{}, e
	}
	return r, nil
}
func (s *FileStore) PendingActivity(ctx context.Context, now time.Time, limit int) ([]activity.Event, error) {
	state, e := s.Snapshot(ctx)
	if e != nil {
		return nil, e
	}
	return activity.Pending(state.Events, now, limit), nil
}
func (s *FileStore) AckActivity(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := Clone(s.state)
	next.Events = activity.Without(next.Events, key)
	return s.commit(ctx, next)
}
