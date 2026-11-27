package engagement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

const maxDocumentBytes = 64 << 20

type document struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

type FileStore struct {
	mu   sync.RWMutex
	path string
	data document
}

func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{path: path, data: document{Version: 1, Entries: []Entry{}}}
	if path == "" {
		return s, nil
	}
	info, err := packagefs.LstatNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxDocumentBytes {
		return nil, ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(f, maxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxDocumentBytes || !utf8.Valid(body) {
		return nil, ErrInvalid
	}
	// Reject duplicate JSON keys rather than silently accepting the last value.
	if err := uniqueJSON(json.NewDecoder(bytes.NewReader(body))); err != nil {
		return nil, err
	}
	// Preserve the local depth limit and reject lossy Unicode and field aliases.
	if err := packagefs.ValidateUniqueJSONFor(body, &s.data); err != nil {
		return nil, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	s.data = document{}
	if err := decoder.Decode(&s.data); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, ErrInvalid
	}
	if s.data.Version != 1 || s.data.Entries == nil {
		return nil, ErrInvalid
	}
	seen, counts := map[entryKey]bool{}, map[ownerKey]int{}
	for _, e := range s.data.Entries {
		input, err := Normalize(Input{UserID: e.UserID, TargetID: e.TargetID, Kind: e.Kind, Collection: e.Collection, Active: true})
		key := entryKey{e.UserID, e.TargetID, e.Kind}
		owner := ownerKey{e.UserID, e.Kind}
		counts[owner]++
		if err != nil || input.Collection != e.Collection || e.CreatedAt.IsZero() || seen[key] || counts[owner] > Capacity(e.Kind) {
			return nil, ErrInvalid
		}
		seen[key] = true
	}
	return s, nil
}

type entryKey struct {
	user, target int64
	kind         string
}
type ownerKey struct {
	user int64
	kind string
}

func uniqueJSON(d *json.Decoder) error {
	var value func(int) error
	value = func(depth int) error {
		if depth > 8 {
			return ErrInvalid
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				keys := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok || keys[name] {
						return ErrInvalid
					}
					keys[name] = true
					if err := value(depth + 1); err != nil {
						return err
					}
				}
				end, err := d.Token()
				if err != nil || end != json.Delim('}') {
					return ErrInvalid
				}
			case '[':
				for d.More() {
					if err := value(depth + 1); err != nil {
						return err
					}
				}
				end, err := d.Token()
				if err != nil || end != json.Delim(']') {
					return ErrInvalid
				}
			default:
				return ErrInvalid
			}
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func (s *FileStore) Ready(ctx context.Context) error { return ctx.Err() }

func (s *FileStore) Set(ctx context.Context, input Input) error {
	input, err := Normalize(input)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index, count := -1, 0
	for i, e := range s.data.Entries {
		if e.UserID == input.UserID && e.Kind == input.Kind {
			count++
			if e.TargetID == input.TargetID {
				index = i
			}
		}
	}
	if index < 0 && !input.Active {
		return nil
	}
	if index >= 0 && input.Active && s.data.Entries[index].Collection == input.Collection {
		return nil
	}
	if index < 0 && count >= Capacity(input.Kind) {
		return ErrLimit
	}
	d := document{Version: 1, Entries: append([]Entry{}, s.data.Entries...)}
	if index >= 0 {
		if input.Active {
			d.Entries[index].Collection = input.Collection
		} else {
			d.Entries = append(d.Entries[:index], d.Entries[index+1:]...)
		}
	} else {
		d.Entries = append(d.Entries, Entry{UserID: input.UserID, TargetID: input.TargetID, Kind: input.Kind, Collection: input.Collection, CreatedAt: time.Now().UTC()})
	}
	return s.persist(ctx, d)
}

// persist is called with the store write lock held; memory advances only after
// the atomic file replacement succeeds.
func (s *FileStore) persist(ctx context.Context, d document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.path != "" {
		body, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if len(body) > maxDocumentBytes {
			return ErrLimit
		}
		if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
			return err
		}
		if _, err := packagefs.LstatNoSymlink(filepath.Dir(s.path)); err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(s.path), ".engagement-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if err := f.Chmod(0600); err != nil {
			f.Close()
			return err
		}
		if _, err := f.Write(body); err != nil {
			f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := packagefs.ReplaceRegularFileNoSymlink(f.Name(), s.path); err != nil {
			return err
		}
	}
	s.data = d
	return nil
}

func (s *FileStore) Merge(ctx context.Context, user int64, inputs []Input, preview bool) (MergeResult, error) {
	inputs, err := NormalizeMerge(user, inputs)
	if err != nil {
		return MergeResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return MergeResult{}, err
	}
	seen, counts := map[entryKey]bool{}, map[string]int{}
	for _, e := range s.data.Entries {
		if e.UserID == user {
			seen[entryKey{user, e.TargetID, e.Kind}] = true
			counts[e.Kind]++
		}
	}
	result := MergeResult{}
	d := document{Version: 1, Entries: append([]Entry{}, s.data.Entries...)}
	now := time.Now().UTC()
	for _, input := range inputs {
		key := entryKey{user, input.TargetID, input.Kind}
		if seen[key] {
			result.Existing++
			continue
		}
		counts[input.Kind]++
		if counts[input.Kind] > Capacity(input.Kind) {
			return MergeResult{}, ErrLimit
		}
		seen[key] = true
		d.Entries = append(d.Entries, Entry{UserID: user, TargetID: input.TargetID, Kind: input.Kind, Collection: input.Collection, CreatedAt: now})
		result.Added++
	}
	if !preview && result.Added > 0 {
		if err := s.persist(ctx, d); err != nil {
			return MergeResult{}, err
		}
	}
	return result, nil
}

func (s *FileStore) Entries(ctx context.Context, user int64, kind string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if user <= 0 || Capacity(kind) == 0 {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []Entry{}
	for _, e := range s.data.Entries {
		if e.UserID == user && e.Kind == kind {
			result = append(result, e)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].TargetID > result[j].TargetID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

func (s *FileStore) States(ctx context.Context, user int64, kind string, ids []int64) ([]State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ValidTargets(user, kind, ids) {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]State, len(ids))
	positions := make(map[int64]int, len(ids))
	for i, id := range ids {
		result[i].TargetID = id
		positions[id] = i
	}
	for _, e := range s.data.Entries {
		i, ok := positions[e.TargetID]
		if !ok || e.Kind != kind {
			continue
		}
		result[i].Count++
		if e.UserID == user {
			result[i].Active = true
			result[i].Collection = e.Collection
		}
	}
	return result, nil
}
