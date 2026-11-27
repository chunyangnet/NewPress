package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

const maxSessionFileBytes = 8 << 20

type sessionDocument struct {
	Schema   int             `json:"schema"`
	Kind     string          `json:"kind"`
	Sessions []SessionRecord `json:"sessions"`
}

// FileSessionStore uses the application's existing exclusive directory lock.
// Candidate snapshots are published only after durable replacement succeeds.
// There is deliberately no stale snapshot recovery: it could resurrect a logout.
type FileSessionStore struct {
	mu      sync.Mutex
	path    string
	records map[string]SessionRecord
	durable map[string]time.Time
	file    os.FileInfo
}

var _ SessionStore = (*FileSessionStore)(nil)

func NewMemorySessionStore() *FileSessionStore {
	return &FileSessionStore{records: make(map[string]SessionRecord), durable: make(map[string]time.Time)}
}

func NewFileSessionStore(path string) (*FileSessionStore, error) {
	if path == "" {
		return nil, errors.New("session store path is required")
	}
	s := NewMemorySessionStore()
	var err error
	s.path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	file, info, err := packagefs.OpenRegularNoSymlink(s.path)
	if errors.Is(err, os.ErrNotExist) {
		// Lazily create the file on the first login, including after a restore.
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() <= 0 || info.Size() > maxSessionFileBytes {
		return nil, errors.New("invalid session file size")
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxSessionFileBytes+1))
	if err != nil {
		return nil, err
	}
	var doc sessionDocument
	if err := packagefs.DecodeJSONInto(bytes.NewReader(payload), maxSessionFileBytes, "sessions", &doc); err != nil {
		return nil, err
	}
	if doc.Schema != 1 || doc.Kind != "newpress.sessions" || doc.Sessions == nil || len(doc.Sessions) > maxActiveSessions {
		return nil, errors.New("invalid session document")
	}
	for _, r := range doc.Sessions {
		if err := r.Validate(); err != nil {
			return nil, err
		}
		if _, exists := s.records[r.ID]; exists {
			return nil, errors.New("duplicate session digest")
		}
		s.records[r.ID], s.durable[r.ID] = r, r.LastSeenAt
	}
	s.file = info
	if err := s.readyLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FileSessionStore) Ready(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.readyLocked()
}

// Flush persists coalesced activity during a graceful shutdown. An abrupt stop
// can only shorten an idle window by SessionTouchInterval, never extend it.
func (s *FileSessionStore) Flush(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.path == "" {
		return nil
	}
	for id, r := range s.records {
		if r.LastSeenAt.After(s.durable[id]) {
			return s.commitLocked(ctx, s.cloneLocked())
		}
	}
	return s.readyLocked()
}

func (s *FileSessionStore) readyLocked() error {
	if s.path == "" {
		return nil
	}
	info, err := packagefs.LstatNoSymlink(s.path)
	if errors.Is(err, os.ErrNotExist) && s.file == nil {
		return nil
	}
	if err != nil {
		return err
	}
	if s.file == nil || !info.Mode().IsRegular() || !os.SameFile(s.file, info) ||
		info.Size() != s.file.Size() || !info.ModTime().Equal(s.file.ModTime()) {
		return errors.New("session file changed outside this process; restart to reload")
	}
	return nil
}

func (s *FileSessionStore) Get(ctx context.Context, id string) (SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return SessionRecord{}, err
	}
	if err := s.readyLocked(); err != nil {
		return SessionRecord{}, err
	}
	r, ok := s.records[id]
	if !ok {
		return SessionRecord{}, ErrSessionNotFound
	}
	return r, nil
}

func (s *FileSessionStore) Create(ctx context.Context, r SessionRecord, p SessionPolicy) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if !r.Live(p.Now, p.IdleTTL) {
		return ErrSessionNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.records[r.ID]; exists {
		return errors.New("session digest already exists")
	}
	candidate := s.cloneLocked()
	for id, existing := range candidate {
		if !existing.Live(p.Now, p.IdleTTL) {
			delete(candidate, id)
		}
	}
	var own []SessionRecord
	for _, existing := range candidate {
		if existing.Identity() == r.Identity() {
			own = append(own, existing)
		}
	}
	sortSessionRecords(own)
	for len(own) >= p.MaxPerIdentity {
		delete(candidate, own[0].ID)
		own = own[1:]
	}
	if len(candidate) >= p.MaxActive {
		all := make([]SessionRecord, 0, len(candidate))
		for _, existing := range candidate {
			all = append(all, existing)
		}
		sortSessionRecords(all)
		for len(candidate) >= p.MaxActive {
			delete(candidate, all[0].ID)
			all = all[1:]
		}
	}
	candidate[r.ID] = r
	return s.commitLocked(ctx, candidate)
}

func sortSessionRecords(records []SessionRecord) {
	sort.Slice(records, func(i, j int) bool {
		if !records[i].LastSeenAt.Equal(records[j].LastSeenAt) {
			return records[i].LastSeenAt.Before(records[j].LastSeenAt)
		}
		return records[i].ID < records[j].ID
	})
}

func (s *FileSessionStore) Touch(ctx context.Context, id string, now time.Time, idleTTL time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.readyLocked(); err != nil {
		return err
	}
	r, exists := s.records[id]
	if !exists || !r.Live(now, idleTTL) {
		return ErrSessionNotFound
	}
	if !now.After(r.LastSeenAt) {
		return nil
	}
	r.LastSeenAt = now
	if s.path == "" || now.Sub(s.durable[id]) < SessionTouchInterval(idleTTL) {
		s.records[id] = r
		return nil
	}
	candidate := s.cloneLocked()
	candidate[id] = r
	return s.commitLocked(ctx, candidate)
}

func (s *FileSessionStore) List(ctx context.Context, now time.Time, idleTTL time.Duration) ([]SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.readyLocked(); err != nil {
		return nil, err
	}
	result := make([]SessionRecord, 0, len(s.records))
	for _, r := range s.records {
		if r.Live(now, idleTTL) {
			result = append(result, r)
		}
	}
	return result, nil
}

func (s *FileSessionStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.readyLocked(); err != nil {
		return err
	}
	if _, exists := s.records[id]; !exists {
		return nil
	}
	candidate := s.cloneLocked()
	delete(candidate, id)
	return s.commitLocked(ctx, candidate)
}

func (s *FileSessionStore) DeleteUser(ctx context.Context, userID int64, username, exceptID string, now time.Time, idleTTL time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := s.readyLocked(); err != nil {
		return 0, err
	}
	identity := (SessionRecord{UserID: userID, Username: username}).Identity()
	if userID <= 0 && username == "" {
		return 0, errors.New("session identity is required")
	}
	candidate := s.cloneLocked()
	count := 0
	changed := false
	for id, r := range candidate {
		if id != exceptID && r.Identity() == identity {
			delete(candidate, id)
			changed = true
			if r.Live(now, idleTTL) {
				count++
			}
		}
	}
	if !changed {
		return 0, nil
	}
	if err := s.commitLocked(ctx, candidate); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *FileSessionStore) cloneLocked() map[string]SessionRecord {
	candidate := make(map[string]SessionRecord, len(s.records))
	for id, r := range s.records {
		candidate[id] = r
	}
	return candidate
}

func (s *FileSessionStore) commitLocked(ctx context.Context, candidate map[string]SessionRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.readyLocked(); err != nil {
		return err
	}
	if s.path == "" {
		s.records = candidate
		return nil
	}
	doc := sessionDocument{Schema: 1, Kind: "newpress.sessions", Sessions: make([]SessionRecord, 0, len(candidate))}
	for _, r := range candidate {
		doc.Sessions = append(doc.Sessions, r)
	}
	sort.Slice(doc.Sessions, func(i, j int) bool { return doc.Sessions[i].ID < doc.Sessions[j].ID })
	payload, err := packagefs.MarshalUniqueJSON(doc)
	if err != nil {
		return err
	}
	if len(payload) > maxSessionFileBytes {
		return errors.New("session snapshot capacity exceeded")
	}
	dir := filepath.Dir(s.path)
	if err := packagefs.MkdirAllNoSymlink(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".newpress-sessions-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err = tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err = tmp.Write(payload); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = packagefs.ReplaceRegularFileNoSymlink(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("replace session snapshot: %w", err)
	}
	// Replacement has committed. Reconcile memory even if the durability check
	// subsequently fails; never publish the previous snapshot over a revocation.
	s.records = candidate
	s.durable = make(map[string]time.Time, len(candidate))
	for id, r := range candidate {
		s.durable[id] = r.LastSeenAt
	}
	s.file, err = packagefs.LstatNoSymlink(s.path)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		handle, err := packagefs.OpenDirectoryNoSymlink(dir)
		if err != nil {
			return err
		}
		defer handle.Close()
		if err := handle.Sync(); err != nil {
			return err
		}
	}
	return nil
}
