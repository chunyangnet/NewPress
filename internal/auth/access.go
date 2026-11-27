package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const (
	AccessRegister  = "register"
	AccessRecover   = "recover"
	AccessInvite    = "invite"
	MaxAccessTokens = 1000
)

var ErrAccessInvalid = errors.New("account link is invalid or expired")

// AccessToken contains only a digest of the bearer secret. Email and binding
// metadata are private and must never be returned by a public lookup endpoint.
type AccessToken struct {
	Hash        string     `json:"hash"`
	Kind        string     `json:"kind"`
	Email       string     `json:"email"`
	UserID      int64      `json:"user_id,omitempty"`
	UserVersion int64      `json:"user_version,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	ConsumedAt  *time.Time `json:"consumed_at,omitempty"`
	WindowAt    time.Time  `json:"window_at"`
	Requests    int        `json:"requests"`
}

type AccessStore interface {
	IssueAccess(context.Context, AccessToken, string) error
	LookupAccess(context.Context, string, string, time.Time) (AccessToken, error)
	RedeemAccess(context.Context, string, string, UserInput, string, time.Time) (User, error)
	ListInvitations(context.Context, time.Time) ([]AccessToken, error)
	RevokeInvitation(context.Context, string, string, time.Time) error
}

func NormalizeAccessEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if len(email) > 320 || !validEmail(email) {
		return "", &UserValidationError{Fields: map[string]string{"email": "请输入有效的邮箱地址"}}
	}
	return email, nil
}

func NewAccessToken(kind, email string, now time.Time) (string, AccessToken, error) {
	email, err := NormalizeAccessEmail(email)
	if err != nil {
		return "", AccessToken{}, err
	}
	if kind != AccessRegister && kind != AccessRecover && kind != AccessInvite {
		return "", AccessToken{}, ErrAccessInvalid
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", AccessToken{}, err
	}
	raw := base64.RawURLEncoding.EncodeToString(secret[:])
	hash, _ := AccessHash(raw)
	ttl := 30 * time.Minute
	if kind == AccessInvite {
		ttl = 7 * 24 * time.Hour
	}
	now = now.UTC().Truncate(time.Second)
	return raw, AccessToken{Hash: hash, Kind: kind, Email: email, CreatedAt: now, ExpiresAt: now.Add(ttl), WindowAt: now, Requests: 1}, nil
}

func AccessHash(raw string) (string, error) {
	if len(raw) != 43 {
		return "", ErrAccessInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return "", ErrAccessInvalid
	}
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:]), nil
}

func ValidAccessRecord(t AccessToken) bool {
	h, err := hex.DecodeString(t.Hash)
	email, emailErr := NormalizeAccessEmail(t.Email)
	validKind := t.Kind == AccessRegister || t.Kind == AccessRecover || t.Kind == AccessInvite
	ttl := 30 * time.Minute
	if t.Kind == AccessInvite {
		ttl = 7 * 24 * time.Hour
	}
	return err == nil && len(h) == 32 && hex.EncodeToString(h) == t.Hash && emailErr == nil && email == t.Email && validKind &&
		!t.CreatedAt.IsZero() && t.ExpiresAt.After(t.CreatedAt) && t.ExpiresAt.Sub(t.CreatedAt) <= ttl &&
		!t.WindowAt.IsZero() && !t.WindowAt.After(t.CreatedAt) && t.Requests >= 1 && t.Requests <= 5 &&
		(t.ConsumedAt == nil || !t.ConsumedAt.Before(t.CreatedAt)) && t.UserID >= 0 && t.UserVersion >= 0 &&
		((t.UserID == 0 && t.UserVersion == 0) || (t.Kind == AccessRecover && t.UserID > 0 && t.UserVersion > 0))
}

func AccessUsable(t AccessToken, kind string, now time.Time) bool {
	return t.Kind == kind && t.ConsumedAt == nil && now.Before(t.ExpiresAt) && !now.Before(t.CreatedAt)
}

// AccessWindow keeps request limits after consumption and when changing between
// registration and recovery. Issuing a new link invalidates the old one.
func AccessWindow(next AccessToken, previous []AccessToken) (AccessToken, error) {
	for _, old := range previous {
		if old.Email != next.Email {
			continue
		}
		if next.CreatedAt.Sub(old.CreatedAt) < time.Minute {
			return AccessToken{}, ErrRateLimited
		}
		if next.CreatedAt.Sub(old.WindowAt) < time.Hour && old.Requests >= next.Requests {
			next.WindowAt, next.Requests = old.WindowAt, old.Requests+1
		}
	}
	if next.Requests > 5 {
		return AccessToken{}, ErrRateLimited
	}
	return next, nil
}

func (m *Manager) accessStore() (AccessStore, error) {
	if m == nil {
		return nil, ErrUserStoreNotAvailable
	}
	store, ok := m.users.(AccessStore)
	if !ok {
		return nil, ErrUserStoreNotAvailable
	}
	return store, nil
}

func (m *Manager) IssueAccess(ctx context.Context, kind, email, actor string) (string, AccessToken, error) {
	store, err := m.accessStore()
	if err != nil {
		return "", AccessToken{}, err
	}
	raw, record, err := NewAccessToken(kind, email, m.now())
	if err != nil {
		return "", AccessToken{}, err
	}
	if kind == AccessRecover {
		u, err := m.users.FindUserByLogin(ctx, record.Email)
		if err != nil && !errors.Is(err, ErrUserNotFound) {
			return "", AccessToken{}, err
		}
		if err == nil && u.Status == UserStatusActive && strings.EqualFold(u.Email, record.Email) {
			record.UserID, record.UserVersion = u.ID, u.Version
		}
	}
	if err := store.IssueAccess(ctx, record, actor); err != nil {
		return "", AccessToken{}, err
	}
	return raw, record, nil
}

func (m *Manager) CompleteAccess(ctx context.Context, raw, kind string, input UserInput, password string) (User, error) {
	store, err := m.accessStore()
	if err != nil {
		return User{}, err
	}
	hash, err := AccessHash(raw)
	if err != nil {
		return User{}, err
	}
	record, err := store.LookupAccess(ctx, hash, kind, m.now())
	if err != nil {
		return User{}, err
	}
	// No expensive password hashing for invalid, used or expired credentials.
	if kind != AccessRecover {
		input.Email, input.Status, input.RoleSlugs = record.Email, UserStatusActive, []string{"subscriber"}
		var fields map[string]string
		input, fields = NormalizeUserInput(input)
		if len(fields) > 0 {
			return User{}, &UserValidationError{Fields: fields}
		}
	}
	hashPassword, err := m.hashPassword(ctx, password)
	if err != nil {
		return User{}, err
	}
	u, err := store.RedeemAccess(ctx, hash, kind, input, hashPassword, m.now())
	if err != nil {
		return User{}, err
	}
	if err := m.RevokeUserSessionsContext(ctx, u.ID); err != nil {
		return u, err
	}
	m.mu.Lock()
	m.configured = true
	m.mu.Unlock()
	return u, nil
}

func (m *Manager) ListInvitations(ctx context.Context) ([]AccessToken, error) {
	store, err := m.accessStore()
	if err != nil {
		return nil, err
	}
	return store.ListInvitations(ctx, m.now())
}

func (m *Manager) RevokeInvitation(ctx context.Context, hash, actor string) error {
	store, err := m.accessStore()
	if err != nil {
		return err
	}
	return store.RevokeInvitation(ctx, hash, actor, m.now())
}
