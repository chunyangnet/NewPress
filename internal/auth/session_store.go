package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrSessionUnavailable = errors.New("session storage unavailable")

const (
	SessionDirectory        = "auth-sessions"
	sessionOperationTimeout = 5 * time.Second
)

// SessionRecord is private authentication state, never an HTTP or plugin DTO.
// ID is a token digest; CredentialDigest is a one-way fingerprint of the password
// hash, not the password hash itself. Authorization is always reloaded from Users.
type SessionRecord struct {
	ID               string          `json:"id"`
	UserID           int64           `json:"user_id"`
	Username         string          `json:"username"`
	DisplayName      string          `json:"display_name"`
	RoleName         string          `json:"role"`
	CredentialDigest string          `json:"credential_digest"`
	CSRFToken        string          `json:"csrf_token"`
	CreatedAt        time.Time       `json:"created_at"`
	LastSeenAt       time.Time       `json:"last_seen_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
	Metadata         SessionMetadata `json:"metadata"`
}

// SessionPolicy applies atomically to session creation, including across SQL
// connections. Limits cannot be enforced using a per-process cache.
type SessionPolicy struct {
	Now            time.Time
	IdleTTL        time.Duration
	MaxActive      int
	MaxPerIdentity int
}

// SessionStore commits Create/Delete/DeleteUser before returning success.
// DeleteUser removes matching records, returning the number that were live. Touch
// may coalesce activity writes, but must never insert a missing or expired row.
// Implementations return ErrSessionNotFound only for absent/expired sessions;
// I/O errors must remain distinguishable. All returned records are value copies.
type SessionStore interface {
	Ready(context.Context) error
	Create(context.Context, SessionRecord, SessionPolicy) error
	Get(context.Context, string) (SessionRecord, error)
	Touch(context.Context, string, time.Time, time.Duration) error
	List(context.Context, time.Time, time.Duration) ([]SessionRecord, error)
	Delete(context.Context, string) error
	DeleteUser(context.Context, int64, string, string, time.Time, time.Duration) (int, error)
}

func (r SessionRecord) Identity() string {
	if r.UserID > 0 {
		return fmt.Sprintf("id:%d", r.UserID)
	}
	return "name:" + strings.ToLower(r.Username)
}

func (r SessionRecord) Live(now time.Time, idleTTL time.Duration) bool {
	return now.Before(r.ExpiresAt) && now.Sub(r.LastSeenAt) < idleTTL
}

func (r SessionRecord) Validate() error {
	if _, err := sessionKeyFromID(r.ID); err != nil {
		return errors.New("invalid session digest")
	}
	if _, err := sessionKeyFromID(r.CredentialDigest); err != nil {
		return errors.New("invalid session credential fingerprint")
	}
	if _, err := sessionKeyFromID(r.CSRFToken); err != nil {
		return errors.New("invalid session CSRF state")
	}
	if r.UserID < 0 || r.Username == "" || len(r.Username) > 320 || len(r.DisplayName) > 1024 || len(r.RoleName) > 1024 ||
		r.CreatedAt.IsZero() || r.LastSeenAt.Before(r.CreatedAt) || !r.ExpiresAt.After(r.LastSeenAt) ||
		len(r.Metadata.IPAddress) > 64 || len(r.Metadata.UserAgent) > 512 {
		return errors.New("invalid session record")
	}
	return nil
}

func (p SessionPolicy) Validate() error {
	if p.Now.IsZero() || p.IdleTTL <= 0 || p.MaxActive < 1 || p.MaxActive > maxActiveSessions ||
		p.MaxPerIdentity < 1 || p.MaxPerIdentity > maxSessionsPerIdentity {
		return errors.New("invalid session policy")
	}
	return nil
}

// SessionTouchInterval bounds activity write amplification and possible early
// expiry after a crash. Never extend the absolute lifetime during renewal.
func SessionTouchInterval(idleTTL time.Duration) time.Duration {
	return min(30*time.Second, idleTTL/4)
}

func sessionDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func sessionUnavailable(err error) error {
	if err == nil || errors.Is(err, ErrSessionNotFound) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrSessionUnavailable, err)
}
