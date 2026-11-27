package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"newpress.local/newpress/internal/audit"
	"regexp"
	"time"
)

var ErrPhoneConflict = errors.New("phone already exists")
var ErrRegistrationConflict = errors.New("registration identity already exists")
var phonePattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

func ValidPhone(phone string) bool { return phonePattern.MatchString(phone) }

func (m *Manager) RegistrationLink(ctx context.Context, raw, kind string) (AccessToken, error) {
	if kind != AccessRegister && kind != AccessInvite {
		return AccessToken{}, ErrAccessInvalid
	}
	store, err := m.accessStore()
	if err != nil {
		return AccessToken{}, err
	}
	hash, err := AccessHash(raw)
	if err != nil {
		return AccessToken{}, err
	}
	return store.LookupAccess(ctx, hash, kind, m.now())
}

type RegistrationStore interface {
	FindRegistration(context.Context, string, string) (User, error)
	BindRegistration(context.Context, int64, int64, UserInput, string) (User, error)
}

func (m *Manager) FindRegistration(ctx context.Context, kind, key string) (User, error) {
	store, ok := m.users.(RegistrationStore)
	if !ok {
		return User{}, ErrUserStoreNotAvailable
	}
	if key == "" {
		return User{}, ErrUserNotFound
	}
	return store.FindRegistration(ctx, kind, key)
}

func RandomCredential() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (m *Manager) RegistrationPassword(ctx context.Context, password string) (string, error) {
	return m.hashPassword(ctx, password)
}

func (m *Manager) RegisterAccount(ctx context.Context, input UserInput, password string) (User, error) {
	input.Status = UserStatusActive
	input.RoleSlugs = []string{"subscriber"}
	input, fields := NormalizeUserInput(input)
	if len(fields) > 0 {
		return User{}, &UserValidationError{Fields: fields}
	}
	if existing, err := m.FindRegistration(ctx, "request", input.RegistrationKey); err == nil {
		if existing.RegistrationDigest != input.RegistrationDigest {
			return User{}, ErrRegistrationConflict
		}
		return existing, nil
	} else if !errors.Is(err, ErrUserNotFound) {
		return User{}, err
	}
	h, err := m.RegistrationPassword(ctx, password)
	if err != nil {
		return User{}, err
	}
	u, err := m.users.CreateUser(ctx, UserMutation{Actor: "self-service"}, input, h)
	if errors.Is(err, ErrRegistrationConflict) {
		if existing, lookup := m.FindRegistration(ctx, "request", input.RegistrationKey); lookup == nil && existing.RegistrationDigest == input.RegistrationDigest {
			return existing, nil
		}
	}
	return u, err
}

func (m *Manager) BindRegistration(ctx context.Context, u User, input UserInput, password string) (User, error) {
	store, ok := m.users.(RegistrationStore)
	if !ok {
		return User{}, ErrUserStoreNotAvailable
	}
	h := ""
	var err error
	if password != "" {
		h, err = m.RegistrationPassword(ctx, password)
		if err != nil {
			return User{}, err
		}
	}
	return store.BindRegistration(ctx, u.ID, u.Version, input, h)
}

// Only call after proving a trusted identity (OAuth, a delivered code, or a
// successful new registration). Never infer ownership from a provider email.
func (m *Manager) LoginVerifiedUser(ctx context.Context, id int64, metadata SessionMetadata) (string, error) {
	if m == nil || m.users == nil {
		return "", ErrUserStoreNotAvailable
	}
	ctx, cancel := context.WithTimeout(ctx, sessionOperationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", sessionUnavailable(err)
	}
	u, err := m.users.GetUser(ctx, id)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return "", ErrInvalidCredentials
		}
		return "", sessionUnavailable(err)
	}
	if u.Status != UserStatusActive {
		return "", ErrInvalidCredentials
	}
	_ = m.users.RecordUserLogin(ctx, id, m.now())
	if err := ctx.Err(); err != nil {
		return "", sessionUnavailable(err)
	}
	token, _, err := m.createSessionContext(ctx, m.now(), principalFromUser(u), normalizeSessionMetadata(metadata), u.PasswordHash)
	return token, err
}

func (s *FileUserStore) FindRegistration(ctx context.Context, kind, key string) (User, error) {
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if key == "" {
		return User{}, ErrUserNotFound
	}
	for _, u := range s.users {
		if kind == "external" && storedHasExternal(u, key) || kind == "request" && u.RegistrationKey == key || kind == "phone" && u.Phone == key || kind == "email" && u.Email == key {
			return s.userLocked(u), nil
		}
	}
	return User{}, ErrUserNotFound
}

func (s *FileUserStore) registrationUniqueLocked(except int64, input UserInput) error {
	for id, u := range s.users {
		if id == except {
			continue
		}
		if input.Phone != "" && u.Phone == input.Phone {
			return ErrPhoneConflict
		}
		if input.ExternalKey != "" && storedHasExternal(u, input.ExternalKey) || input.RegistrationKey != "" && u.RegistrationKey == input.RegistrationKey {
			return ErrRegistrationConflict
		}
	}
	return nil
}

func (s *FileUserStore) BindRegistration(ctx context.Context, id, version int64, input UserInput, passwordHash string) (User, error) {
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok || u.Status != UserStatusActive || !storedHasExternal(u, input.ExternalKey) {
		return User{}, ErrUserNotFound
	}
	if u.Version != version {
		return User{}, ErrUserVersionConflict
	}
	input.RoleSlugs = append([]string(nil), u.RoleSlugs...)
	input.Status = u.Status
	n, fields := NormalizeUserInput(input)
	if len(fields) > 0 {
		return User{}, &UserValidationError{Fields: fields}
	}
	if err := s.uniqueUserLocked(id, n.Username, n.Email); err != nil {
		return User{}, err
	}
	if err := s.registrationUniqueLocked(id, n); err != nil {
		return User{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	if n.Email != u.Email {
		u.EmailVerifiedAt = nil
	}
	if n.Phone != u.Phone {
		u.PhoneVerifiedAt = nil
	}
	u.Username = n.Username
	u.DisplayName = n.DisplayName
	u.Email = n.Email
	u.Phone = n.Phone
	if n.EmailVerified && n.Email != "" {
		u.EmailVerifiedAt = &now
	}
	if n.PhoneVerified && n.Phone != "" {
		u.PhoneVerifiedAt = &now
	}
	if passwordHash != "" {
		if err := ValidatePasswordHash(passwordHash); err != nil {
			return User{}, err
		}
		u.PasswordHash = passwordHash
		u.PasswordChangedAt = now
		u.DisablePassword = false
	}
	u.Version++
	u.RegistrationKey = n.RegistrationKey
	u.RegistrationDigest = n.RegistrationDigest
	u.RegistrationVersion = u.Version
	u.UpdatedAt = now
	candidate := cloneStoredUsers(s.users)
	candidate[id] = u
	event := audit.Event{Action: "user.identity_bound", ObjectType: "user", Actor: "self-service", Metadata: map[string]any{"version": u.Version}}
	if err := s.persistCandidate(candidate, s.roles, s.nextUserID, s.nextRoleID, now, &event); err != nil {
		return User{}, err
	}
	s.users = candidate
	s.updatedAt = now
	return s.userLocked(u), nil
}

func validRegistrationDigest(value string) bool {
	b, e := hex.DecodeString(value)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == value
}
