package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func sessionFixture(id, username string, now time.Time) SessionRecord {
	return SessionRecord{ID: sessionDigest(id), Username: username, DisplayName: username, RoleName: "author",
		CredentialDigest: sessionDigest("credential"), CSRFToken: sessionDigest("csrf:" + id),
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
}

func TestSessionStoreCapacityExpiryAndConcurrentRevocation(t *testing.T) {
	for _, disk := range []bool{false, true} {
		name := "memory"
		if disk {
			name = "file"
		}
		t.Run(name, func(t *testing.T) {
			s := NewMemorySessionStore()
			if disk {
				var err error
				s, err = NewFileSessionStore(filepath.Join(t.TempDir(), "private", "sessions.json"))
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			now := time.Date(2026, 11, 27, 1, 0, 0, 0, time.UTC)
			p := SessionPolicy{Now: now, IdleTTL: 10 * time.Minute, MaxActive: 3, MaxPerIdentity: 2}
			for i, owner := range []string{"alice", "alice", "alice", "bob", "carol"} {
				p.Now = now.Add(time.Duration(i) * time.Second)
				r := sessionFixture(string(rune('a'+i)), owner, p.Now)
				if err := s.Create(ctx, r, p); err != nil {
					t.Fatal(err)
				}
			}
			items, err := s.List(ctx, p.Now, p.IdleTTL)
			if err != nil || len(items) != 3 {
				t.Fatalf("bounded inventory = %d, %v", len(items), err)
			}
			for _, id := range []string{"a", "b"} {
				if _, err := s.Get(ctx, sessionDigest(id)); !errors.Is(err, ErrSessionNotFound) {
					t.Fatalf("capacity did not evict oldest %s: %v", id, err)
				}
			}
			id := sessionDigest("c")
			var wg sync.WaitGroup
			for range 12 {
				wg.Go(func() {
					err := s.Touch(ctx, id, now.Add(time.Minute), p.IdleTTL)
					if err != nil && !errors.Is(err, ErrSessionNotFound) {
						t.Errorf("concurrent touch: %v", err)
					}
				})
			}
			if err := s.Delete(ctx, id); err != nil {
				t.Fatal(err)
			}
			wg.Wait()
			if err := s.Touch(ctx, id, now.Add(2*time.Minute), p.IdleTTL); !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("touch resurrected revoked session: %v", err)
			}
			items, err = s.List(ctx, now.Add(11*time.Minute), p.IdleTTL)
			if err != nil || len(items) != 0 {
				t.Fatalf("expired inventory = %d, %v", len(items), err)
			}
		})
	}
}

func TestFileSessionsCoalesceActivityAndNeverRestoreRevokedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	s, err := NewFileSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	r := sessionFixture("activity", "alice", now)
	p := SessionPolicy{Now: now, IdleTTL: 10 * time.Minute, MaxActive: 3, MaxPerIdentity: 2}
	if err := s.Create(t.Context(), r, p); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := s.Touch(t.Context(), r.ID, now.Add(time.Second), p.IdleTTL); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("every validation rewrote the session snapshot")
	}
	if err := s.Touch(t.Context(), r.ID, now.Add(31*time.Second), p.IdleTTL); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Get(t.Context(), r.ID)
	if err != nil || !loaded.LastSeenAt.Equal(now.Add(31*time.Second)) || loaded.ExpiresAt != r.ExpiresAt {
		t.Fatalf("durable activity/absolute expiry = %#v, %v", loaded, err)
	}
	if err := reopened.Delete(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	stale, err := NewFileSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Get(t.Context(), r.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("logout did not survive restart: %v", err)
	}
	if err := os.WriteFile(path+".bak", before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileSessionStore(path); err == nil {
		t.Fatal("corrupt primary silently recovered an old login snapshot")
	}
}

func TestFileSessionFailedCommitPreservesPriorStateAndRejectsSecondWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	s, err := NewFileSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := sessionFixture("commit", "alice", now)
	p := SessionPolicy{Now: now, IdleTTL: time.Hour, MaxActive: 3, MaxPerIdentity: 2}
	if err := s.Create(t.Context(), r, p); err != nil {
		t.Fatal(err)
	}
	stale, err := NewFileSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".bak", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), r.ID); err == nil {
		t.Fatal("failed durable revoke returned success")
	}
	if _, err := s.Get(t.Context(), r.ID); err != nil {
		t.Fatalf("failed commit changed in-memory state: %v", err)
	}
	if err := os.Remove(path + ".bak"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	if err := stale.Create(t.Context(), sessionFixture("other", "bob", now), p); err == nil {
		t.Fatal("stale second writer overwrote another process's revocation")
	}
}

func TestPersistentManagerRestartCSRFPasswordAndRoleRefresh(t *testing.T) {
	dir := t.TempDir()
	password := "Restart-safe owner password 2026"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	newManager := func() (*Manager, *FileUserStore) {
		t.Helper()
		users, err := NewFileUserStore(filepath.Join(dir, "users.json"))
		if err != nil {
			t.Fatal(err)
		}
		sessions, err := NewFileSessionStore(filepath.Join(dir, SessionDirectory, "sessions.json"))
		if err != nil {
			t.Fatal(err)
		}
		m, err := NewWithContext(t.Context(), Config{Username: "owner", PasswordHash: hash, Users: users, Sessions: sessions})
		if err != nil {
			t.Fatal(err)
		}
		return m, users
	}
	m, _ := newManager()
	token, owner, err := m.Authenticate("127.0.0.1", "owner", password)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(dir, SessionDirectory, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{token, hash, password, "capabilities"} {
		if bytes.Contains(payload, []byte(secret)) {
			t.Fatal("session persistence retained bearer, credential, or authorization material")
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, SessionDirectory, "sessions.json"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("session file permissions: %v, %v", info, err)
		}
	}
	m, users := newManager()
	loaded, err := m.ResolveSession(t.Context(), token)
	if err != nil || loaded.UserID != owner.UserID || loaded.CSRFToken != owner.CSRFToken || !loaded.ExpiresAt.Equal(owner.ExpiresAt) {
		t.Fatalf("restart lost identity/CSRF/absolute lifetime: %#v, %v", loaded, err)
	}
	// Ordinary profile version changes do not act as authentication epochs.
	u, err := users.GetUser(t.Context(), owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = users.UpdateUser(t.Context(), u.ID, UserMutation{ExpectedVersion: u.Version}, UserInput{
		Username: u.Username, Email: u.Email, DisplayName: "Updated owner", Status: u.Status, Locale: u.Locale, RoleSlugs: []string{"administrator"},
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = m.ResolveSession(t.Context(), token)
	if err != nil || loaded.DisplayName != "Updated owner" {
		t.Fatalf("profile change unexpectedly ended a login: %#v, %v", loaded, err)
	}
	u, _ = users.GetUser(t.Context(), owner.UserID)
	newHash, err := HashPassword("Changed restart-safe owner password 2026")
	if err != nil {
		t.Fatal(err)
	}
	// Change credentials through the store, as the offline reset command does.
	if _, err := users.UpdateUserPassword(t.Context(), u.ID, UserMutation{ExpectedVersion: u.Version}, newHash); err != nil {
		t.Fatal(err)
	}
	m, _ = newManager()
	if _, err := m.ResolveSession(t.Context(), token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("old password session survived an offline reset/restart: %v", err)
	}
}

type faultSessionStore struct {
	SessionStore
	getErr, createErr, touchErr, deleteErr, listErr error
}

func (s *faultSessionStore) Get(ctx context.Context, id string) (SessionRecord, error) {
	if s.getErr != nil {
		return SessionRecord{}, s.getErr
	}
	return s.SessionStore.Get(ctx, id)
}
func (s *faultSessionStore) Create(ctx context.Context, r SessionRecord, p SessionPolicy) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.SessionStore.Create(ctx, r, p)
}
func (s *faultSessionStore) Touch(ctx context.Context, id string, now time.Time, ttl time.Duration) error {
	if s.touchErr != nil {
		return s.touchErr
	}
	return s.SessionStore.Touch(ctx, id, now, ttl)
}
func (s *faultSessionStore) Delete(ctx context.Context, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.SessionStore.Delete(ctx, id)
}
func (s *faultSessionStore) List(ctx context.Context, now time.Time, ttl time.Duration) ([]SessionRecord, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.SessionStore.List(ctx, now, ttl)
}

func TestSessionStorageFailuresAreExplicitAndFailClosed(t *testing.T) {
	s := &faultSessionStore{SessionStore: NewMemorySessionStore()}
	hash, err := HashPassword("Session failure password 2026")
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{Username: "owner", PasswordHash: hash, Sessions: s})
	if err != nil {
		t.Fatal(err)
	}
	token, principal, err := m.Authenticate("ip", "owner", "Session failure password 2026")
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected storage failure")
	s.getErr = failure
	if _, err := m.ResolveSession(t.Context(), token); !errors.Is(err, ErrSessionUnavailable) || !errors.Is(err, failure) {
		t.Fatalf("lookup failure = %v", err)
	}
	s.getErr, s.touchErr = nil, failure
	if _, err := m.ResolveSession(t.Context(), token); !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("renewal failure = %v", err)
	}
	s.touchErr, s.deleteErr = nil, failure
	if err := m.RevokeSessionContext(t.Context(), token); !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("revocation failure = %v", err)
	}
	s.deleteErr, s.listErr = nil, failure
	if _, err := m.ListSessionsContext(t.Context(), principal, token, true); !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("inventory failure = %v", err)
	}
	s.listErr, s.createErr = nil, failure
	if token, p, err := m.Authenticate("ip", "owner", "Session failure password 2026"); token != "" || p.Username != "" || !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("failed persistence issued a login: %q, %#v, %v", token, p, err)
	}
}

func TestSessionDocumentRejectsCorruptionAndCancellation(t *testing.T) {
	now := time.Now().UTC()
	r := sessionFixture("integrity", "owner", now)
	valid, _ := json.Marshal(sessionDocument{Schema: 1, Kind: "newpress.sessions", Sessions: []SessionRecord{r}})
	for name, payload := range map[string][]byte{
		"duplicate record": []byte(strings.Replace(string(valid), `]}`, ","+string(mustJSONSession(t, r))+`]} `, 1)),
		"duplicate schema": []byte(strings.Replace(string(valid), `"schema":1`, `"schema":1,"Schema":1`, 1)),
		"unknown schema":   []byte(strings.Replace(string(valid), `"schema":1`, `"schema":9`, 1)),
		"invalid digest":   []byte(strings.Replace(string(valid), r.ID, "short", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.json")
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileSessionStore(path); err == nil {
				t.Fatal("accepted corrupt session state")
			}
		})
	}
	s := NewMemorySessionStore()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := SessionPolicy{Now: now, IdleTTL: time.Hour, MaxActive: 3, MaxPerIdentity: 2}
	if err := s.Create(ctx, r, p); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled create = %v", err)
	}
	items, err := s.List(t.Context(), now, time.Hour)
	if err != nil || len(items) != 0 {
		t.Fatal("cancelled request changed storage")
	}
}

func mustJSONSession(t *testing.T, r SessionRecord) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSessionBulkRevocationCountsOnlyLiveSessions(t *testing.T) {
	for _, disk := range []bool{false, true} {
		name := "memory"
		if disk {
			name = "file"
		}
		t.Run(name, func(t *testing.T) {
			s := NewMemorySessionStore()
			if disk {
				var err error
				s, err = NewFileSessionStore(filepath.Join(t.TempDir(), "sessions.json"))
				if err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now().UTC()
			p := SessionPolicy{Now: now, IdleTTL: time.Minute, MaxActive: 4, MaxPerIdentity: 4}
			for _, id := range []string{"expired", "active", "current"} {
				if err := s.Create(t.Context(), sessionFixture(id, "owner", now), p); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range []string{"active", "current"} {
				if err := s.Touch(t.Context(), sessionDigest(id), now.Add(45*time.Second), p.IdleTTL); err != nil {
					t.Fatal(err)
				}
			}
			count, err := s.DeleteUser(t.Context(), 0, "OWNER", sessionDigest("current"), now.Add(time.Minute), p.IdleTTL)
			if err != nil || count != 1 {
				t.Fatalf("live revocation count = %d, %v", count, err)
			}
			for _, id := range []string{"expired", "active"} {
				if _, err := s.Get(t.Context(), sessionDigest(id)); !errors.Is(err, ErrSessionNotFound) {
					t.Fatalf("bulk revoke retained %s: %v", id, err)
				}
			}
			if _, err := s.Get(t.Context(), sessionDigest("current")); err != nil {
				t.Fatalf("bulk revoke removed current session: %v", err)
			}
		})
	}
}

type pausedSessionUserStore struct {
	UserStore
	entered chan struct{}
	release chan struct{}
}

func (s *pausedSessionUserStore) GetUser(ctx context.Context, id int64) (User, error) {
	close(s.entered)
	select {
	case <-s.release:
		return s.UserStore.GetUser(ctx, id)
	case <-ctx.Done():
		return User{}, ctx.Err()
	}
}

func TestSessionValidationDoesNotResurrectLogoutDuringUserLookup(t *testing.T) {
	users, err := NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	const password = "Concurrent session lookup password 2026"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{Username: "owner", PasswordHash: hash, Users: users})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := m.Authenticate("ip", "owner", password)
	if err != nil {
		t.Fatal(err)
	}
	paused := &pausedSessionUserStore{UserStore: users, entered: make(chan struct{}), release: make(chan struct{})}
	m.users = paused
	result := make(chan error, 1)
	go func() {
		_, err := m.ResolveSession(t.Context(), token)
		result <- err
	}()
	select {
	case <-paused.entered:
	case err := <-result:
		t.Fatalf("validation ended before user lookup: %v", err)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	revokeErr := m.RevokeSessionContext(t.Context(), token)
	close(paused.release)
	if revokeErr != nil {
		t.Fatal(revokeErr)
	}
	if err := <-result; !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("in-flight lookup accepted a revoked login: %v", err)
	}
}

type deadlineSessionStore struct {
	SessionStore
	deadline time.Time
}

func (s *deadlineSessionStore) Get(ctx context.Context, id string) (SessionRecord, error) {
	s.deadline, _ = ctx.Deadline()
	return s.SessionStore.Get(ctx, id)
}

func TestSessionResolutionBoundsStorageDeadline(t *testing.T) {
	s := &deadlineSessionStore{SessionStore: NewMemorySessionStore()}
	hash, err := HashPassword("Bounded session storage password 2026")
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{Username: "owner", PasswordHash: hash, Sessions: s})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := m.createSession(time.Now(), Principal{Username: "owner"}, SessionMetadata{}, hash)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	if _, err := m.ResolveSession(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	if s.deadline.IsZero() || s.deadline.After(time.Now().Add(sessionOperationTimeout)) || !s.deadline.After(before) {
		t.Fatalf("unbounded session lookup deadline: %v", s.deadline)
	}
	parentDeadline := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(t.Context(), parentDeadline)
	defer cancel()
	if _, err := m.ResolveSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if !s.deadline.Equal(parentDeadline) {
		t.Fatalf("session lookup extended the caller deadline: %v > %v", s.deadline, parentDeadline)
	}
}
