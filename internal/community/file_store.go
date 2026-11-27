package community

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/packagefs"
)

type fileState struct {
	Schema  int      `json:"schema"`
	Circles []Circle `json:"circles"`
}
type FileStore struct {
	mu         sync.RWMutex
	path       string
	circles    map[string]Circle
	digest     [32]byte
	exists     bool
	audit      *audit.Ledger
	auditFault error
}

func (s *FileStore) Ready(ctx context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.unchanged()
}

func decode(b []byte, target any) error {
	// Validate the original bytes before decoding can replace damaged Unicode
	// or let differently cased keys overwrite the same persisted field.
	if err := packagefs.DecodeJSONInto(bytes.NewReader(b), MaxDocumentBytes, "community state", target); err != nil {
		return ErrInvalid
	}
	return nil
}
func NewFileStore(path string, ledgers ...*audit.Ledger) (*FileStore, error) {
	s := &FileStore{path: path, circles: map[string]Circle{}}
	if len(ledgers) > 0 {
		s.audit = ledgers[0]
	}
	if path == "" {
		return s, nil
	}
	b, exists, err := readFile(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return s, nil
	}
	var state fileState
	if err = decode(b, &state); err != nil {
		return nil, err
	}
	if state.Schema != 1 || len(state.Circles) > MaxCircles {
		return nil, ErrInvalid
	}
	for _, c := range state.Circles {
		if err = Validate(c); err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(c)
		if len(encoded) > MaxCircleBytes {
			return nil, ErrCapacity
		}
		if _, ok := s.circles[c.Slug]; ok {
			return nil, ErrInvalid
		}
		s.circles[c.Slug] = c
	}
	s.digest, s.exists = sha256.Sum256(b), true
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
	b, err := io.ReadAll(io.LimitReader(f, MaxDocumentBytes+1))
	if len(b) > MaxDocumentBytes {
		return nil, false, ErrCapacity
	}
	return b, true, err
}
func (s *FileStore) unchanged() error {
	if s.auditFault != nil {
		return s.auditFault
	}
	if s.audit != nil {
		if err := s.audit.Ready(); err != nil {
			return err
		}
	}
	if s.path == "" {
		return nil
	}
	b, exists, err := readFile(s.path)
	if err != nil {
		return err
	}
	if exists != s.exists || exists && sha256.Sum256(b) != s.digest {
		return ErrChanged
	}
	return nil
}
func (s *FileStore) List(ctx context.Context) ([]Circle, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]Circle, 0, len(s.circles))
	for _, c := range s.circles {
		result = append(result, Header(c))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Slug < result[j].Slug })
	return result, nil
}
func (s *FileStore) Get(ctx context.Context, slug string) (Circle, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return Circle{}, err
	}
	c, ok := s.circles[slug]
	if !ok {
		return Circle{}, ErrNotFound
	}
	return Clone(c), nil
}
func (s *FileStore) Apply(ctx context.Context, slug string, a Actor, cmd Command, now time.Time) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := s.unchanged(); err != nil {
		return Result{}, err
	}
	var old *Circle
	if c, ok := s.circles[slug]; ok {
		old = &c
	} else if len(s.circles) >= MaxCircles {
		return Result{}, ErrCapacity
	}
	next, result, err := Mutate(old, slug, a, cmd, now)
	if err != nil {
		return Result{}, err
	}
	if result.Replay {
		return result, nil
	}
	event := AuditEvent(a, cmd, result)
	if err := s.commitLocked(ctx, slug, next, event); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *FileStore) commitLocked(ctx context.Context, slug string, next Circle, event audit.Event) error {
	state := fileState{Schema: 1, Circles: make([]Circle, 0, len(s.circles)+1)}
	for k, c := range s.circles {
		if k != slug {
			state.Circles = append(state.Circles, c)
		}
	}
	state.Circles = append(state.Circles, next)
	sort.Slice(state.Circles, func(i, j int) bool { return state.Circles[i].Slug < state.Circles[j].Slug })
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(b) > MaxDocumentBytes {
		return ErrCapacity
	}
	if s.path != "" {
		parent := filepath.Dir(s.path)
		if err = packagefs.MkdirAllNoSymlink(parent, 0700); err != nil {
			return err
		}
		f, e := os.CreateTemp(parent, ".community-*")
		if e != nil {
			return e
		}
		defer os.Remove(f.Name())
		if err = f.Chmod(0600); err == nil {
			_, err = f.Write(b)
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
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = s.unchanged(); err != nil {
			return err
		}
		write := func() error { return packagefs.ReplaceRegularFileNoSymlink(f.Name(), s.path) }
		if s.audit != nil {
			_, err = s.audit.ApplyFile(audit.FactCommunity, event, b, s.path, write)
			if err != nil {
				s.auditFault = err
			}
		} else {
			err = write()
		}
		if err != nil {
			return err
		}
		s.digest, s.exists = sha256.Sum256(b), true
	}
	s.circles[slug] = next
	return nil
}

func (s *FileStore) PendingActivity(ctx context.Context, now time.Time, limit int) ([]activity.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.unchanged(); err != nil {
		return nil, err
	}
	all := []activity.Event{}
	for _, c := range s.circles {
		all = append(all, c.Activities...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].At.Equal(all[j].At) {
			return all[i].Key < all[j].Key
		}
		return all[i].At.Before(all[j].At)
	})
	return activity.Pending(all, now, limit), nil
}
func (s *FileStore) AckActivity(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.unchanged(); err != nil {
		return err
	}
	for slug, c := range s.circles {
		for _, e := range c.Activities {
			if e.Key == key {
				next := Clone(c)
				next.Activities = activity.Without(next.Activities, key)
				return s.commitLocked(ctx, slug, next, audit.Event{Action: "community.delivery.ack", ObjectType: "community_circle", ObjectID: "all", Actor: "system"})
			}
		}
	}
	return nil
}
