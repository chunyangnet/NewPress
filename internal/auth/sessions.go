package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (m *Manager) createSession(now time.Time, principal Principal, metadata SessionMetadata, passwordHash string) (string, Principal, error) {
	return m.createSessionContext(context.Background(), now, principal, metadata, passwordHash)
}

func (m *Manager) createSessionContext(ctx context.Context, now time.Time, principal Principal, metadata SessionMetadata, passwordHash string) (string, Principal, error) {
	ctx, cancel := context.WithTimeout(ctx, sessionOperationTimeout)
	defer cancel()
	var tokenBytes, csrfBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", Principal{}, fmt.Errorf("generate session token: %w", err)
	}
	if _, err := rand.Read(csrfBytes[:]); err != nil {
		return "", Principal{}, fmt.Errorf("generate CSRF token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
	principal.CSRFToken = base64.RawURLEncoding.EncodeToString(csrfBytes[:])
	principal.CreatedAt, principal.ExpiresAt = now, now.Add(m.absoluteTTL)
	principal.RoleSlugs = append([]string(nil), principal.RoleSlugs...)
	principal.Capabilities = append([]string(nil), principal.Capabilities...)
	record := SessionRecord{
		ID: sessionDigest(token), UserID: principal.UserID, Username: principal.Username,
		DisplayName: principal.Label(), RoleName: principal.RoleName,
		CredentialDigest: sessionDigest(passwordHash), CSRFToken: principal.CSRFToken,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: principal.ExpiresAt,
		Metadata: normalizeSessionMetadata(metadata),
	}
	if err := m.sessionStore.Create(ctx, record, SessionPolicy{
		Now: now, IdleTTL: m.idleTTL, MaxActive: maxActiveSessions, MaxPerIdentity: maxSessionsPerIdentity,
	}); err != nil {
		return "", Principal{}, sessionUnavailable(err)
	}
	return token, principal, nil
}

func (m *Manager) ValidateSession(token string) (Principal, bool) {
	return m.ValidateSessionContext(context.Background(), token)
}

// ValidateSessionContext remains a fail-closed compatibility helper. HTTP and
// other callers that need to distinguish an outage must use ResolveSession.
func (m *Manager) ValidateSessionContext(ctx context.Context, token string) (Principal, bool) {
	principal, err := m.ResolveSession(ctx, token)
	return principal, err == nil
}

func (m *Manager) ResolveSession(ctx context.Context, token string) (Principal, error) {
	if m == nil || !m.Configured() || !validSessionToken(token) {
		return Principal{}, ErrSessionNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, sessionOperationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Principal{}, sessionUnavailable(err)
	}
	now := m.now()
	r, err := m.sessionStore.Get(ctx, sessionDigest(token))
	if err != nil {
		return Principal{}, sessionUnavailable(err)
	}
	if !r.Live(now, m.idleTTL) {
		return Principal{}, ErrSessionNotFound
	}
	var principal Principal
	if m.users != nil && r.UserID > 0 {
		user, err := m.users.GetUser(ctx, r.UserID)
		if errors.Is(err, ErrUserNotFound) {
			return Principal{}, m.invalidateSession(ctx, r.ID)
		}
		if err != nil {
			return Principal{}, sessionUnavailable(err)
		}
		if user.Status != UserStatusActive || !constantTimeStringEqual(r.CredentialDigest, sessionDigest(user.PasswordHash)) {
			return Principal{}, m.invalidateSession(ctx, r.ID)
		}
		principal = principalFromUser(user)
	} else {
		if m.users != nil || r.UserID != 0 || !strings.EqualFold(r.Username, m.username) ||
			!constantTimeStringEqual(r.CredentialDigest, sessionDigest(m.passwordHash)) {
			return Principal{}, m.invalidateSession(ctx, r.ID)
		}
		principal = Principal{Username: m.username, DisplayName: m.username, Locale: "zh-CN", RoleName: "管理员",
			RoleSlugs: []string{"administrator"}, Capabilities: []string{CapabilityAll}, AccountVersion: 1}
	}
	principal.CSRFToken, principal.CreatedAt, principal.ExpiresAt = r.CSRFToken, r.CreatedAt, r.ExpiresAt
	// Touch rechecks existence and expiry after the user lookup. It can only
	// update an existing session, preventing a concurrent logout resurrection.
	if err := m.sessionStore.Touch(ctx, r.ID, m.now(), m.idleTTL); err != nil {
		return Principal{}, sessionUnavailable(err)
	}
	return principal, nil
}

func (m *Manager) invalidateSession(ctx context.Context, id string) error {
	if err := m.sessionStore.Delete(ctx, id); err != nil {
		return sessionUnavailable(err)
	}
	return ErrSessionNotFound
}

func (m *Manager) RevokeSession(token string) error {
	return m.RevokeSessionContext(context.Background(), token)
}

func (m *Manager) RevokeSessionContext(ctx context.Context, token string) error {
	if m == nil || !validSessionToken(token) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, sessionOperationTimeout)
	defer cancel()
	return sessionUnavailable(m.sessionStore.Delete(ctx, sessionDigest(token)))
}

func (m *Manager) RevokeUserSessions(userID int64) error {
	return m.RevokeUserSessionsContext(context.Background(), userID)
}

func (m *Manager) RevokeUserSessionsContext(ctx context.Context, userID int64) error {
	if m == nil || userID <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, sessionOperationTimeout)
	defer cancel()
	_, err := m.sessionStore.DeleteUser(ctx, userID, "", "", m.now(), m.idleTTL)
	return sessionUnavailable(err)
}

// ListSessions is the compatibility projection. Production handlers use the
// context variant so unavailable storage cannot appear to be an empty list.
func (m *Manager) ListSessions(principal Principal, currentToken string, includeAll bool) []SessionInfo {
	items, _ := m.ListSessionsContext(context.Background(), principal, currentToken, includeAll)
	return items
}

func (m *Manager) ListSessionsContext(ctx context.Context, principal Principal, currentToken string, includeAll bool) ([]SessionInfo, error) {
	if m == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, sessionOperationTimeout)
	defer cancel()
	includeAll = includeAll && principal.Can(CapabilitySessionsManage)
	records, err := m.sessionStore.List(ctx, m.now(), m.idleTTL)
	if err != nil {
		return nil, sessionUnavailable(err)
	}
	currentID := sessionDigest(currentToken)
	items := make([]SessionInfo, 0, len(records))
	for _, r := range records {
		if !includeAll && !sameSessionUser(principal, Principal{UserID: r.UserID, Username: r.Username}) {
			continue
		}
		items = append(items, m.sessionInfo(r, currentToken != "" && r.ID == currentID))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Current != items[j].Current {
			return items[i].Current
		}
		if !items[i].LastSeenAt.Equal(items[j].LastSeenAt) {
			return items[i].LastSeenAt.After(items[j].LastSeenAt)
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func (m *Manager) ActiveSessionCount() int {
	count, _ := m.ActiveSessionCountContext(context.Background())
	return count
}

func (m *Manager) ActiveSessionCountContext(ctx context.Context) (int, error) {
	if m == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, sessionOperationTimeout)
	defer cancel()
	records, err := m.sessionStore.List(ctx, m.now(), m.idleTTL)
	return len(records), sessionUnavailable(err)
}

func (m *Manager) RevokeSessionID(principal Principal, currentToken, sessionID string) (SessionInfo, error) {
	return m.RevokeSessionIDContext(context.Background(), principal, currentToken, sessionID)
}

func (m *Manager) RevokeSessionIDContext(ctx context.Context, principal Principal, currentToken, sessionID string) (SessionInfo, error) {
	if m == nil {
		return SessionInfo{}, ErrSessionNotFound
	}
	if _, err := sessionKeyFromID(sessionID); err != nil {
		return SessionInfo{}, ErrSessionNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, sessionOperationTimeout)
	defer cancel()
	r, err := m.sessionStore.Get(ctx, sessionID)
	if err != nil {
		return SessionInfo{}, sessionUnavailable(err)
	}
	if !r.Live(m.now(), m.idleTTL) {
		return SessionInfo{}, ErrSessionNotFound
	}
	if currentToken != "" && sessionID == sessionDigest(currentToken) {
		return SessionInfo{}, ErrCurrentSession
	}
	if !principal.Can(CapabilitySessionsManage) && !(principal.Can(CapabilitySessionsOwn) && sameSessionUser(principal, Principal{UserID: r.UserID, Username: r.Username})) {
		return SessionInfo{}, ErrSessionForbidden
	}
	if err := m.sessionStore.Delete(ctx, r.ID); err != nil {
		return SessionInfo{}, sessionUnavailable(err)
	}
	return m.sessionInfo(r, false), nil
}

func (m *Manager) RevokeOtherSessions(principal Principal, currentToken string) (int, error) {
	return m.RevokeOtherSessionsContext(context.Background(), principal, currentToken)
}

func (m *Manager) RevokeOtherSessionsContext(ctx context.Context, principal Principal, currentToken string) (int, error) {
	if m == nil || (!principal.Can(CapabilitySessionsManage) && !principal.Can(CapabilitySessionsOwn)) {
		return 0, ErrSessionForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, sessionOperationTimeout)
	defer cancel()
	exceptID := ""
	if currentToken != "" {
		exceptID = sessionDigest(currentToken)
	}
	count, err := m.sessionStore.DeleteUser(ctx, principal.UserID, principal.Username, exceptID, m.now(), m.idleTTL)
	return count, sessionUnavailable(err)
}

func (m *Manager) sessionInfo(r SessionRecord, current bool) SessionInfo {
	idleExpires := r.LastSeenAt.Add(m.idleTTL)
	if r.ExpiresAt.Before(idleExpires) {
		idleExpires = r.ExpiresAt
	}
	return SessionInfo{
		ID: r.ID, UserID: r.UserID, Username: r.Username, DisplayName: r.DisplayName, RoleName: r.RoleName,
		IPAddress: r.Metadata.IPAddress, UserAgent: r.Metadata.UserAgent,
		CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt, ExpiresAt: r.ExpiresAt, IdleExpiresAt: idleExpires, Current: current,
	}
}

func validSessionToken(token string) bool {
	// Issued tokens encode exactly 32 random bytes. Reject malformed input
	// before it can cause a storage lookup or durable logout mutation.
	if len(token) != base64.RawURLEncoding.EncodedLen(32) {
		return false
	}
	_, err := sessionKeyFromID(token)
	return err == nil
}

func sessionKeyFromID(value string) ([32]byte, error) {
	var key [32]byte
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != len(key) {
		return key, ErrSessionNotFound
	}
	copy(key[:], decoded)
	return key, nil
}

func sameSessionUser(left, right Principal) bool {
	if left.UserID > 0 || right.UserID > 0 {
		return left.UserID > 0 && left.UserID == right.UserID
	}
	return left.Username != "" && strings.EqualFold(left.Username, right.Username)
}
