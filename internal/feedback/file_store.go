package feedback

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

	"newpress.local/newpress/internal/packagefs"
)

type fileState struct {
	Schema int     `json:"schema"`
	Boards []Board `json:"boards"`
}
type FileStore struct {
	mu     sync.RWMutex
	path   string
	boards map[string]Board
	digest [32]byte
	exists bool
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
	if err := packagefs.DecodeJSONInto(bytes.NewReader(b), MaxDocumentBytes, "feedback state", target); err != nil {
		return ErrInvalid
	}
	return nil
}
func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{path: path, boards: map[string]Board{}}
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
	if state.Schema != 1 || len(state.Boards) > MaxBoards {
		return nil, ErrInvalid
	}
	for _, c := range state.Boards {
		if err = Validate(c); err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(c)
		if len(encoded) > MaxBoardBytes {
			return nil, ErrCapacity
		}
		if _, ok := s.boards[c.Slug]; ok {
			return nil, ErrInvalid
		}
		s.boards[c.Slug] = c
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
func (s *FileStore) List(ctx context.Context) ([]Board, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]Board, 0, len(s.boards))
	for _, c := range s.boards {
		result = append(result, Header(c))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Slug < result[j].Slug })
	return result, nil
}
func (s *FileStore) Get(ctx context.Context, slug string) (Board, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return Board{}, err
	}
	c, ok := s.boards[slug]
	if !ok {
		return Board{}, ErrNotFound
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
	var old *Board
	if c, ok := s.boards[slug]; ok {
		old = &c
	} else if len(s.boards) >= MaxBoards {
		return Result{}, ErrCapacity
	}
	next, result, err := Mutate(old, slug, a, cmd, now)
	if err != nil {
		return Result{}, err
	}
	if result.Replay {
		return result, nil
	}
	state := fileState{Schema: 1, Boards: make([]Board, 0, len(s.boards)+1)}
	for k, c := range s.boards {
		if k != slug {
			state.Boards = append(state.Boards, c)
		}
	}
	state.Boards = append(state.Boards, next)
	sort.Slice(state.Boards, func(i, j int) bool { return state.Boards[i].Slug < state.Boards[j].Slug })
	b, err := json.Marshal(state)
	if err != nil {
		return Result{}, err
	}
	if len(b) > MaxDocumentBytes {
		return Result{}, ErrCapacity
	}
	if s.path != "" {
		parent := filepath.Dir(s.path)
		if err = packagefs.MkdirAllNoSymlink(parent, 0700); err != nil {
			return Result{}, err
		}
		f, e := os.CreateTemp(parent, ".feedback-*")
		if e != nil {
			return Result{}, e
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
			return Result{}, err
		}
		if closeErr != nil {
			return Result{}, closeErr
		}
		if err = ctx.Err(); err != nil {
			return Result{}, err
		}
		if err = s.unchanged(); err != nil {
			return Result{}, err
		}
		if err = packagefs.ReplaceRegularFileNoSymlink(f.Name(), s.path); err != nil {
			return Result{}, err
		}
		s.digest, s.exists = sha256.Sum256(b), true
	}
	s.boards[slug] = next
	return result, nil
}
