// Package announcement manages the site's notice list independently of theme settings.
package announcement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

const MaxItems = 200
const maxBytes = 2 << 20

var ErrConflict = errors.New("公告列表已在其他窗口更新，请重新载入后核对并保存")
var ErrNotFound = errors.New("公告不存在")
var ErrInvalid = errors.New("请检查公告标题、内容、日期和链接")
var ErrCapacity = errors.New("最多保留 200 条公告，请先删除过期内容")

type Item struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
	Published bool      `json:"published"`
	Pinned    bool      `json:"pinned"`
	Order     int       `json:"order"`
	Date      time.Time `json:"date"`
}

type State struct {
	Schema   int    `json:"schema"`
	Revision string `json:"revision"`
	NextID   int64  `json:"next_id"`
	Items    []Item `json:"items"`
}

// Repository applies atomic, revision-checked document updates.
type Repository interface {
	Load(context.Context) ([]byte, bool, error)
	Create(context.Context, string, []byte) (bool, error)
	CompareAndSwap(context.Context, string, string, []byte) (bool, error)
}

type Store struct {
	mu         sync.Mutex
	path       string
	repository Repository
	state      State
}

func revision(s State) string {
	s.Revision = ""
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func initial() State {
	s := State{Schema: 1, NextID: 1, Items: []Item{}}
	s.Revision = revision(s)
	return s
}

func NewFileStore(path string) (*Store, error) {
	s := &Store{path: path, state: initial()}
	_, _, err := s.load(context.Background())
	return s, err
}

func NewRepositoryStore(ctx context.Context, repository Repository) (*Store, error) {
	s := &Store{repository: repository, state: initial()}
	_, _, err := s.load(ctx)
	return s, err
}

func ValidURL(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > 2048 || strings.ContainsAny(raw, "\x00\r\n\\") || strings.TrimSpace(raw) != raw {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return false
	}
	if strings.ContainsAny(u.Path, "\x00\r\n\\") || strings.HasPrefix(u.Path, "//") {
		return false
	}
	if u.Scheme == "" {
		return strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") && u.Host == ""
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != ""
}

func ValidateItem(i Item) error {
	if !utf8.ValidString(i.Title+i.Body) || strings.TrimSpace(i.Title) == "" || utf8.RuneCountInString(i.Title) > 120 || utf8.RuneCountInString(i.Body) > 6000 || strings.ContainsAny(i.Title+i.Body, "\x00\r") || !ValidURL(i.URL) || i.Order < 0 || i.Order > 9999 || i.Date.IsZero() || i.Date.Year() < 1970 || i.Date.Year() > 9999 {
		return ErrInvalid
	}
	return nil
}

func decode(raw []byte) (State, error) {
	s, err := packagefs.DecodeJSON[State](bytes.NewReader(raw), maxBytes, "公告列表")
	if err != nil {
		return State{}, err
	}
	if s.Schema != 1 || s.NextID < 1 || len(s.Items) > MaxItems || s.Items == nil || s.Revision != revision(s) {
		return State{}, ErrInvalid
	}
	seen := map[int64]bool{}
	for _, i := range s.Items {
		if i.ID < 1 || i.ID >= s.NextID || seen[i.ID] {
			return State{}, ErrInvalid
		}
		if err := ValidateItem(i); err != nil {
			return State{}, err
		}
		seen[i.ID] = true
	}
	return s, nil
}

func (s *Store) load(ctx context.Context) (State, bool, error) {
	if err := ctx.Err(); err != nil {
		return State{}, false, err
	}
	if s.repository != nil {
		raw, found, err := s.repository.Load(ctx)
		if err != nil {
			return State{}, false, err
		}
		if !found {
			return initial(), false, nil
		}
		v, err := decode(raw)
		return v, true, err
	}
	if s.path == "" {
		v := s.state
		v.Items = slices.Clone(v.Items)
		return v, true, nil
	}
	f, info, err := packagefs.OpenRegularNoSymlink(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return initial(), false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	defer f.Close()
	if info.Size() > maxBytes {
		return State{}, false, ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return State{}, false, err
	}
	v, err := decode(raw)
	return v, true, err
}

func (s *Store) Snapshot(ctx context.Context) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _, err := s.load(ctx)
	return v, err
}

// Public returns only published notices, ordered by pin, manual order and date.
func Public(state State, now time.Time) []Item {
	items := []Item{}
	for _, i := range state.Items {
		if i.Published && !i.Date.After(now) {
			items = append(items, i)
		}
	}
	Sort(items)
	return items
}

func Sort(items []Item) {
	slices.SortStableFunc(items, func(a, b Item) int {
		if a.Pinned != b.Pinned {
			if a.Pinned {
				return -1
			}
			return 1
		}
		if a.Order != b.Order {
			return a.Order - b.Order
		}
		if c := b.Date.Compare(a.Date); c != 0 {
			return c
		}
		if a.ID > b.ID {
			return -1
		}
		if a.ID < b.ID {
			return 1
		}
		return 0
	})
}

func (s *Store) Save(ctx context.Context, expected, action string, item Item) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, found, err := s.load(ctx)
	if err != nil {
		return State{}, err
	}
	if expected == "" || state.Revision != expected {
		return State{}, ErrConflict
	}
	index := slices.IndexFunc(state.Items, func(i Item) bool { return i.ID == item.ID })
	switch action {
	case "save":
		item.Title, item.Body, item.URL = strings.TrimSpace(item.Title), strings.TrimSpace(item.Body), strings.TrimSpace(item.URL)
		if err := ValidateItem(item); err != nil {
			return State{}, err
		}
		if item.ID == 0 {
			if len(state.Items) >= MaxItems {
				return State{}, ErrCapacity
			}
			item.ID = state.NextID
			state.NextID++
			state.Items = append(state.Items, item)
		} else {
			if index < 0 {
				return State{}, ErrNotFound
			}
			state.Items[index] = item
		}
	case "delete":
		if index < 0 {
			return State{}, ErrNotFound
		}
		state.Items = slices.Delete(state.Items, index, index+1)
	default:
		return State{}, ErrInvalid
	}
	state.Revision = revision(state)
	raw, err := json.Marshal(state)
	if err != nil {
		return State{}, err
	}
	if len(raw) > maxBytes {
		return State{}, ErrInvalid
	}
	if s.repository != nil {
		var saved bool
		if found {
			saved, err = s.repository.CompareAndSwap(ctx, expected, state.Revision, raw)
		} else {
			saved, err = s.repository.Create(ctx, state.Revision, raw)
		}
		if err != nil {
			return State{}, err
		}
		if !saved {
			return State{}, ErrConflict
		}
	} else if s.path != "" {
		if err = packagefs.MkdirAllNoSymlink(filepath.Dir(s.path), 0700); err != nil {
			return State{}, err
		}
		f, err := os.CreateTemp(filepath.Dir(s.path), ".announcements-*")
		if err != nil {
			return State{}, err
		}
		defer os.Remove(f.Name())
		if err = f.Chmod(0600); err == nil {
			_, err = f.Write(raw)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return State{}, err
		}
		if closeErr != nil {
			return State{}, closeErr
		}
		current, _, err := s.load(ctx)
		if err != nil {
			return State{}, err
		}
		if current.Revision != expected {
			return State{}, ErrConflict
		}
		if err = packagefs.ReplaceRegularFileNoSymlink(f.Name(), s.path); err != nil {
			return State{}, err
		}
	} else {
		s.state = state
	}
	state.Items = slices.Clone(state.Items)
	return state, nil
}
