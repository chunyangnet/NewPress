package shop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"newpress.local/newpress/internal/packagefs"
	"os"
	"path/filepath"
	"sync"
)

type FileBackend struct {
	mu     sync.Mutex
	path   string
	state  State
	digest [32]byte
	exists bool
}

func NewFileBackend(path string) (*FileBackend, error) {
	b := &FileBackend{path: path, state: InitialState()}
	if path != "" {
		body, ok, e := readStateFile(path)
		if e != nil {
			return nil, e
		}
		if ok {
			if b.state, e = packagefs.DecodeJSON[State](bytes.NewReader(body), MaxStateBytes, "shop state"); e != nil {
				return nil, e
			}
			NormalizeState(&b.state)
			if e = ValidateState(b.state); e != nil {
				return nil, e
			}
			b.digest, b.exists = sha256.Sum256(body), true
		}
	}
	return b, nil
}
func readStateFile(path string) ([]byte, bool, error) {
	f, info, e := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(e, os.ErrNotExist) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	defer f.Close()
	if info.Size() > MaxStateBytes {
		return nil, false, ErrUnavailable
	}
	body, e := io.ReadAll(io.LimitReader(f, MaxStateBytes+1))
	if len(body) > MaxStateBytes {
		return nil, false, ErrUnavailable
	}
	return body, true, e
}
func (b *FileBackend) unchanged() error {
	if b.path == "" {
		return nil
	}
	body, ok, e := readStateFile(b.path)
	if e != nil {
		return e
	}
	if ok != b.exists || ok && sha256.Sum256(body) != b.digest {
		return ErrUnavailable
	}
	return nil
}
func clone(s State) State {
	body, _ := json.Marshal(s)
	var out State
	_ = json.Unmarshal(body, &out)
	NormalizeState(&out)
	return out
}
func (b *FileBackend) Read(ctx context.Context) (State, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return State{}, e
	}
	if e := b.unchanged(); e != nil {
		return State{}, e
	}
	return clone(b.state), nil
}
func (b *FileBackend) Update(ctx context.Context, fn func(*State) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := b.unchanged(); e != nil {
		return e
	}
	s := clone(b.state)
	if e := fn(&s); e != nil {
		return e
	}
	if e := ValidateState(s); e != nil {
		return e
	}
	body, e := json.Marshal(s)
	if e != nil {
		return e
	}
	if len(body) > MaxStateBytes {
		return ErrUnavailable
	}
	previous, _ := json.Marshal(b.state)
	if bytes.Equal(body, previous) {
		return nil
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if b.path != "" {
		if e = os.MkdirAll(filepath.Dir(b.path), 0700); e != nil {
			return e
		}
		if _, e = packagefs.LstatNoSymlink(filepath.Dir(b.path)); e != nil {
			return e
		}
		f, e := os.CreateTemp(filepath.Dir(b.path), ".shop-*")
		if e != nil {
			return e
		}
		defer os.Remove(f.Name())
		if e = f.Chmod(0600); e == nil {
			_, e = f.Write(body)
		}
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		if e = b.unchanged(); e != nil {
			return e
		}
		if e = packagefs.ReplaceRegularFileNoSymlink(f.Name(), b.path); e != nil {
			return e
		}
		b.digest, b.exists = sha256.Sum256(body), true
	}
	// Mutation callbacks may retain input slices or result maps. Publish an
	// isolated snapshot so no caller can mutate state outside a transaction.
	b.state = clone(s)
	return nil
}
