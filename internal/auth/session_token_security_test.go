package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type tokenValidationStore struct {
	SessionStore
	gets, deletes int
}

func (s *tokenValidationStore) Get(context.Context, string) (SessionRecord, error) {
	s.gets++
	return SessionRecord{}, ErrSessionNotFound
}

func (s *tokenValidationStore) Delete(context.Context, string) error {
	s.deletes++
	return nil
}

func TestInvalidSessionTokensNeverReachStorage(t *testing.T) {
	s := &tokenValidationStore{}
	m := &Manager{configured: true, sessionStore: s, now: time.Now, idleTTL: time.Hour}
	for _, token := range []string{"", strings.Repeat("A", 32), strings.Repeat("A", 256), strings.Repeat("A", 42) + "!", strings.Repeat("A", 42) + "B", strings.Repeat("A", 43) + "=", strings.Repeat("A", 43) + "\n"} {
		if _, err := m.ResolveSession(t.Context(), token); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("invalid token returned %v", err)
		}
		if err := m.RevokeSessionContext(t.Context(), token); err != nil {
			t.Errorf("invalid cookie should be an inert revocation: %v", err)
		}
	}
	if s.gets != 0 || s.deletes != 0 {
		t.Fatalf("malformed tokens reached session storage: lookups=%d deletes=%d", s.gets, s.deletes)
	}
	valid := strings.Repeat("A", 43)
	if _, err := m.ResolveSession(t.Context(), valid); !errors.Is(err, ErrSessionNotFound) || s.gets != 1 {
		t.Fatalf("well-formed missing token skipped storage: %v, lookups=%d", err, s.gets)
	}
	if err := m.RevokeSessionContext(t.Context(), valid); err != nil || s.deletes != 1 {
		t.Fatalf("well-formed revocation skipped storage: %v, deletes=%d", err, s.deletes)
	}
}
