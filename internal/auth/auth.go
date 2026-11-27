package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	CookieName             = "newpress_admin"
	defaultMemoryKiB       = 64 * 1024
	defaultTime            = 3
	defaultThreads         = 2
	defaultKeyLength       = 32
	maxPasswordBytes       = 1024
	maxActiveSessions      = 4096
	maxSessionsPerIdentity = 32
	maxLoginAttemptKeys    = 4096
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrRateLimited        = errors.New("too many login attempts")
	ErrPasswordBusy       = fmt.Errorf("password work capacity exhausted: %w", ErrRateLimited)
	ErrSessionNotFound    = errors.New("session not found")
	ErrSessionForbidden   = errors.New("session operation forbidden")
	ErrCurrentSession     = errors.New("current session must be ended through logout")
)

type Config struct {
	Username             string
	PasswordHash         string
	AbsoluteTTL          time.Duration
	IdleTTL              time.Duration
	Users                UserStore
	Sessions             SessionStore
	BootstrapEmail       string
	BootstrapDisplayName string
	BootstrapLocale      string
}

type Principal struct {
	UserID         int64
	Username       string
	Email          string
	DisplayName    string
	Locale         string
	RoleName       string
	RoleSlugs      []string
	Capabilities   []string
	AccountVersion int64
	CSRFToken      string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// SessionMetadata records bounded request context for a successful login. It
// is operational metadata only: no raw cookie or credential material is ever
// retained or projected.
type SessionMetadata struct {
	IPAddress string
	UserAgent string
}

// SessionInfo is the safe management projection of a stored session. ID is
// the SHA-256 token digest encoded as base64url, not the bearer token itself.
type SessionInfo struct {
	ID            string    `json:"id"`
	UserID        int64     `json:"user_id,omitempty"`
	Username      string    `json:"username"`
	DisplayName   string    `json:"display_name"`
	RoleName      string    `json:"role"`
	IPAddress     string    `json:"ip_address,omitempty"`
	UserAgent     string    `json:"user_agent,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	IdleExpiresAt time.Time `json:"idle_expires_at"`
	Current       bool      `json:"current"`
}

type passwordParameters struct {
	memory  uint32
	time    uint32
	threads uint8
	keyLen  uint32
}

type loginAttempt struct {
	failures     int
	windowStart  time.Time
	blockedUntil time.Time
}

type Manager struct {
	username       string
	passwordHash   string
	dummyHash      string
	users          UserStore
	configured     bool
	absoluteTTL    time.Duration
	idleTTL        time.Duration
	mu             sync.Mutex
	sessionStore   SessionStore
	attempts       map[string]loginAttempt
	verificationCh chan struct{}
	now            func() time.Time
}

func New(config Config) (*Manager, error) {
	return NewWithContext(context.Background(), config)
}

func NewWithContext(ctx context.Context, config Config) (*Manager, error) {
	if config.AbsoluteTTL <= 0 {
		config.AbsoluteTTL = 12 * time.Hour
	}
	if config.IdleTTL <= 0 {
		config.IdleTTL = 30 * time.Minute
	}
	if config.IdleTTL > config.AbsoluteTTL {
		return nil, errors.New("admin idle timeout cannot exceed the absolute session lifetime")
	}
	if config.PasswordHash != "" {
		if config.Username == "" {
			config.Username = "admin"
		}
		if len(config.Username) > 80 || strings.TrimSpace(config.Username) != config.Username {
			return nil, errors.New("admin username must be 1-80 characters without surrounding whitespace")
		}
		if err := ValidatePasswordHash(config.PasswordHash); err != nil {
			return nil, fmt.Errorf("invalid admin password hash: %w", err)
		}
	}
	manager := &Manager{
		username:       config.Username,
		passwordHash:   config.PasswordHash,
		users:          config.Users,
		configured:     config.PasswordHash != "",
		absoluteTTL:    config.AbsoluteTTL,
		idleTTL:        config.IdleTTL,
		sessionStore:   config.Sessions,
		attempts:       make(map[string]loginAttempt),
		verificationCh: make(chan struct{}, 2),
		now:            time.Now,
	}
	if manager.sessionStore == nil {
		manager.sessionStore = NewMemorySessionStore()
	}
	if err := manager.sessionStore.Ready(ctx); err != nil {
		return nil, fmt.Errorf("open session storage: %w", err)
	}
	if config.Users == nil {
		return manager, nil
	}
	if err := config.Users.EnsureRoles(ctx, BuiltInRoles()); err != nil {
		return nil, fmt.Errorf("seed built-in roles: %w", err)
	}
	count, err := config.Users.CountManagedUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("count persistent users: %w", err)
	}
	if count == 0 && config.PasswordHash != "" {
		email := strings.TrimSpace(config.BootstrapEmail)
		if email == "" {
			email = "admin@localhost.invalid"
		}
		displayName := strings.TrimSpace(config.BootstrapDisplayName)
		if displayName == "" {
			displayName = config.Username
		}
		locale := strings.TrimSpace(config.BootstrapLocale)
		if locale == "" {
			locale = "zh-CN"
		}
		if _, err := config.Users.CreateUser(ctx, UserMutation{Actor: "environment-bootstrap"}, UserInput{
			Username: config.Username, Email: email, DisplayName: displayName,
			Status: UserStatusActive, Locale: locale, RoleSlugs: []string{"administrator"},
		}, config.PasswordHash); err != nil {
			return nil, fmt.Errorf("bootstrap persistent administrator: %w", err)
		}
		count = 1
	}
	manager.configured = count > 0
	manager.passwordHash = ""
	if count > 0 {
		users, err := config.Users.ListUsers(ctx, UserQuery{Limit: 1})
		if err != nil {
			return nil, fmt.Errorf("load dummy authentication hash: %w", err)
		}
		if len(users.Items) > 0 {
			manager.dummyHash = users.Items[0].PasswordHash
		}
	}
	if manager.dummyHash == "" {
		manager.dummyHash, err = HashPassword("NewPress timing equalizer 2026")
		if err != nil {
			return nil, fmt.Errorf("create authentication equalizer: %w", err)
		}
	}
	return manager, nil
}

func (m *Manager) Configured() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.configured
}

func (m *Manager) Username() string {
	if m == nil {
		return ""
	}
	return m.username
}

func (principal Principal) Can(capability string) bool {
	return roleAllows(principal.Capabilities, strings.TrimSpace(capability))
}

func (principal Principal) HasRole(slug string) bool {
	slug = strings.ToLower(strings.TrimSpace(slug))
	for _, role := range principal.RoleSlugs {
		if role == slug {
			return true
		}
	}
	return false
}

func (principal Principal) Label() string {
	if strings.TrimSpace(principal.DisplayName) != "" {
		return principal.DisplayName
	}
	return principal.Username
}

func (m *Manager) PersistentUsers() bool {
	return m != nil && m.users != nil
}

func (m *Manager) FirstAdminSetupAvailable(ctx context.Context) (bool, error) {
	if m == nil || m.users == nil {
		return false, ErrUserStoreNotAvailable
	}
	store, ok := m.users.(FirstAdminSetupStore)
	if !ok {
		return false, ErrUserStoreNotAvailable
	}
	return store.FirstAdminSetupAvailable(ctx)
}

// ClaimFirstAdministrator is available only for persistent stores that provide
// a database-atomic first-admin gate. Password hashing happens before the
// transaction; the repository closes the durable gate together with the row.
func (m *Manager) ClaimFirstAdministrator(ctx context.Context, input UserInput, password string) (User, error) {
	if m == nil || m.users == nil {
		return User{}, ErrUserStoreNotAvailable
	}
	m.mu.Lock()
	configured := m.configured
	m.mu.Unlock()
	if configured {
		return User{}, ErrFirstAdminSetupClosed
	}
	store, ok := m.users.(FirstAdminSetupStore)
	if !ok {
		return User{}, ErrUserStoreNotAvailable
	}
	passwordHash, err := m.hashPassword(ctx, password)
	if err != nil {
		return User{}, err
	}
	user, err := store.ClaimFirstAdministrator(ctx, UserMutation{Actor: "first-admin-setup"}, input, passwordHash)
	if err != nil {
		return User{}, err
	}
	m.mu.Lock()
	m.configured = true
	m.mu.Unlock()
	return user, nil
}

// Ready includes the persistent identity store in the service readiness
// boundary. In-memory authentication has no external readiness dependency.
func (m *Manager) Ready(ctx context.Context) error {
	if m == nil {
		return errors.New("authentication manager is nil")
	}
	ctx, cancel := context.WithTimeout(ctx, sessionOperationTimeout)
	defer cancel()
	if err := m.sessionStore.Ready(ctx); err != nil {
		return sessionUnavailable(err)
	}
	if m.users == nil {
		return nil
	}
	return m.users.Ready(ctx)
}

func HashPassword(password string) (string, error) {
	if !utf8.ValidString(password) {
		return "", errors.New("password must be valid UTF-8")
	}
	if utf8.RuneCountInString(password) < 12 {
		return "", errors.New("password must contain at least 12 characters")
	}
	if len(password) > maxPasswordBytes {
		return "", errors.New("password is too long")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	params := passwordParameters{
		memory: defaultMemoryKiB, time: defaultTime, threads: defaultThreads, keyLen: defaultKeyLength,
	}
	digest := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, params.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		params.memory,
		params.time,
		params.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest),
	), nil
}

func VerifyPassword(encoded, password string) (bool, error) {
	params, salt, expected, err := decodePasswordHash(encoded)
	if err != nil {
		return false, err
	}
	if len(password) > maxPasswordBytes {
		return false, nil
	}
	actual := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, params.keyLen)
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func ValidatePasswordHash(encoded string) error {
	_, _, _, err := decodePasswordHash(encoded)
	return err
}

func decodePasswordHash(encoded string) (passwordParameters, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return passwordParameters{}, nil, nil, errors.New("expected an argon2id PHC string")
	}
	if !strings.HasPrefix(parts[2], "v=") {
		return passwordParameters{}, nil, nil, errors.New("invalid argon2 version")
	}
	version, err := strconv.Atoi(strings.TrimPrefix(parts[2], "v="))
	if err != nil || version != argon2.Version {
		return passwordParameters{}, nil, nil, errors.New("unsupported argon2 version")
	}
	parameterParts := strings.Split(parts[3], ",")
	if len(parameterParts) != 3 || !strings.HasPrefix(parameterParts[0], "m=") || !strings.HasPrefix(parameterParts[1], "t=") || !strings.HasPrefix(parameterParts[2], "p=") {
		return passwordParameters{}, nil, nil, errors.New("invalid argon2 parameters")
	}
	memory, memoryErr := strconv.ParseUint(strings.TrimPrefix(parameterParts[0], "m="), 10, 32)
	timeCost, timeErr := strconv.ParseUint(strings.TrimPrefix(parameterParts[1], "t="), 10, 32)
	threads, threadsErr := strconv.ParseUint(strings.TrimPrefix(parameterParts[2], "p="), 10, 8)
	if memoryErr != nil || timeErr != nil || threadsErr != nil {
		return passwordParameters{}, nil, nil, errors.New("invalid argon2 parameters")
	}
	params := passwordParameters{memory: uint32(memory), time: uint32(timeCost), threads: uint8(threads)}
	if params.memory < 8*1024 || params.memory > 256*1024 || params.time < 1 || params.time > 10 || params.threads < 1 || params.threads > 8 {
		return passwordParameters{}, nil, nil, errors.New("argon2 parameters are outside accepted safety bounds")
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return passwordParameters{}, nil, nil, errors.New("invalid argon2 salt")
	}
	digest, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(digest) < 16 || len(digest) > 64 {
		return passwordParameters{}, nil, nil, errors.New("invalid argon2 digest")
	}
	params.keyLen = uint32(len(digest))
	return params, salt, digest, nil
}

func (m *Manager) Authenticate(attemptKey, username, password string) (string, Principal, error) {
	return m.AuthenticateContext(context.Background(), attemptKey, username, password)
}

func (m *Manager) AuthenticateContext(ctx context.Context, attemptKey, username, password string) (string, Principal, error) {
	return m.AuthenticateWithMetadataContext(ctx, attemptKey, username, password, SessionMetadata{})
}

func (m *Manager) AuthenticateWithMetadataContext(ctx context.Context, attemptKey, username, password string, metadata SessionMetadata) (string, Principal, error) {
	if err := ctx.Err(); err != nil {
		return "", Principal{}, err
	}
	if !m.Configured() {
		return "", Principal{}, ErrInvalidCredentials
	}
	now := m.now()
	if !m.loginAllowed(attemptKey, now) {
		return "", Principal{}, ErrRateLimited
	}

	principal := Principal{}
	passwordHash := m.passwordHash
	accountValid := false
	var user User
	var lookupErr error
	if m.users != nil {
		loaded, err := m.users.FindUserByLogin(ctx, username)
		if err == nil {
			user = loaded
			passwordHash = loaded.PasswordHash
			accountValid = loaded.Status == UserStatusActive && loaded.PasswordLogin
		} else {
			passwordHash = m.dummyHash
			if !errors.Is(err, ErrUserNotFound) {
				lookupErr = err
			}
		}
	} else {
		accountValid = constantTimeStringEqual(username, m.username)
	}
	verified, verifyErr := m.verifyPassword(ctx, passwordHash, password)
	if err := ctx.Err(); err != nil {
		return "", Principal{}, err
	}
	if errors.Is(verifyErr, ErrRateLimited) {
		return "", Principal{}, verifyErr
	}
	if lookupErr != nil {
		return "", Principal{}, sessionUnavailable(lookupErr)
	}
	if verifyErr != nil || !verified || !accountValid {
		m.recordLoginFailure(attemptKey, now)
		return "", Principal{}, ErrInvalidCredentials
	}
	if m.users != nil {
		principal = principalFromUser(user)
		_ = m.users.RecordUserLogin(ctx, user.ID, now)
	} else {
		principal = Principal{
			Username: m.username, DisplayName: m.username, Locale: "zh-CN", RoleName: "管理员",
			RoleSlugs: []string{"administrator"}, Capabilities: []string{CapabilityAll}, AccountVersion: 1,
		}
	}
	if err := ctx.Err(); err != nil {
		return "", Principal{}, err
	}
	m.clearLoginFailures(attemptKey)
	return m.createSessionContext(ctx, now, principal, normalizeSessionMetadata(metadata), passwordHash)
}

func (m *Manager) ListUsers(ctx context.Context, query UserQuery) (UserList, error) {
	if m == nil || m.users == nil {
		return UserList{}, ErrUserStoreNotAvailable
	}
	return m.users.ListUsers(ctx, query)
}

func (m *Manager) GetUser(ctx context.Context, id int64) (User, error) {
	if m == nil || m.users == nil {
		return User{}, ErrUserStoreNotAvailable
	}
	return m.users.GetUser(ctx, id)
}

// FindUserByUsername resolves public mention syntax without treating an email
// address as a username or exposing private email search through the API.
func (m *Manager) FindUserByUsername(ctx context.Context, username string) (User, error) {
	if m == nil || m.users == nil {
		return User{}, ErrUserStoreNotAvailable
	}
	u, err := m.users.FindUserByLogin(ctx, username)
	if err != nil {
		return User{}, err
	}
	if !strings.EqualFold(u.Username, username) {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

func (m *Manager) ListRoles(ctx context.Context) ([]Role, error) {
	if m == nil || m.users == nil {
		return nil, ErrUserStoreNotAvailable
	}
	return m.users.ListRoles(ctx)
}

func (m *Manager) CreateUser(ctx context.Context, actor Principal, input UserInput, password string) (User, error) {
	if m == nil || m.users == nil {
		return User{}, ErrUserStoreNotAvailable
	}
	passwordHash, err := m.hashPassword(ctx, password)
	if err != nil {
		return User{}, err
	}
	user, err := m.users.CreateUser(ctx, UserMutation{Actor: actor.Username}, input, passwordHash)
	if err != nil {
		return User{}, err
	}
	m.mu.Lock()
	m.configured = true
	m.mu.Unlock()
	return user, nil
}

func (m *Manager) UpdateUser(ctx context.Context, actor Principal, id int64, expectedVersion int64, input UserInput) (User, error) {
	if m == nil || m.users == nil {
		return User{}, ErrUserStoreNotAvailable
	}
	user, err := m.users.UpdateUser(ctx, id, UserMutation{ExpectedVersion: expectedVersion, Actor: actor.Username}, input)
	if err == nil && user.Status != UserStatusActive {
		if err := m.RevokeUserSessionsContext(ctx, id); err != nil {
			return user, err
		}
	}
	return user, err
}

func (m *Manager) UpdateUserPassword(ctx context.Context, actor Principal, id int64, expectedVersion int64, password string) (User, error) {
	if m == nil || m.users == nil {
		return User{}, ErrUserStoreNotAvailable
	}
	passwordHash, err := m.hashPassword(ctx, password)
	if err != nil {
		return User{}, err
	}
	user, err := m.users.UpdateUserPassword(ctx, id, UserMutation{ExpectedVersion: expectedVersion, Actor: actor.Username}, passwordHash)
	if err != nil {
		return User{}, err
	}
	if err := m.RevokeUserSessionsContext(ctx, id); err != nil {
		return user, err
	}
	return user, nil
}

func principalFromUser(user User) Principal {
	roleSlugs := make([]string, 0, len(user.Roles))
	for _, role := range user.Roles {
		roleSlugs = append(roleSlugs, role.Slug)
	}
	return Principal{
		UserID: user.ID, Username: user.Username, Email: user.Email, DisplayName: user.DisplayName,
		Locale: user.Locale, RoleName: user.PrimaryRoleName(), RoleSlugs: roleSlugs,
		Capabilities: mergedCapabilities(user.Roles), AccountVersion: user.Version,
	}
}

func (m *Manager) CheckCSRF(principal Principal, candidate string) bool {
	if len(candidate) < 32 || len(candidate) > 256 || len(principal.CSRFToken) != len(candidate) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(principal.CSRFToken), []byte(candidate)) == 1
}

func (m *Manager) loginAllowed(key string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	attempt, ok := m.attempts[key]
	if !ok {
		return true
	}
	if !attempt.blockedUntil.IsZero() && now.Before(attempt.blockedUntil) {
		return false
	}
	if now.Sub(attempt.windowStart) > 10*time.Minute {
		delete(m.attempts, key)
	}
	return true
}

func (m *Manager) recordLoginFailure(key string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLoginAttemptsLocked(now)
	if _, exists := m.attempts[key]; !exists && len(m.attempts) >= maxLoginAttemptKeys {
		m.evictOldestLoginAttemptLocked()
	}
	attempt := m.attempts[key]
	if attempt.windowStart.IsZero() || now.Sub(attempt.windowStart) > 10*time.Minute {
		attempt = loginAttempt{windowStart: now}
	}
	attempt.failures++
	if attempt.failures >= 6 {
		attempt.blockedUntil = now.Add(15 * time.Minute)
	}
	m.attempts[key] = attempt
}

func (m *Manager) clearLoginFailures(key string) {
	m.mu.Lock()
	delete(m.attempts, key)
	m.mu.Unlock()
}

func (m *Manager) cleanupLoginAttemptsLocked(now time.Time) {
	for key, attempt := range m.attempts {
		if now.Sub(attempt.windowStart) > 30*time.Minute && now.After(attempt.blockedUntil) {
			delete(m.attempts, key)
		}
	}
}

func (m *Manager) evictOldestLoginAttemptLocked() {
	oldestKey := ""
	var oldestRiskEnds time.Time
	var oldestWindow time.Time
	for key, attempt := range m.attempts {
		riskEnds := attempt.blockedUntil
		if riskEnds.Before(attempt.windowStart) {
			riskEnds = attempt.windowStart
		}
		if oldestKey == "" || riskEnds.Before(oldestRiskEnds) || (riskEnds.Equal(oldestRiskEnds) && attempt.windowStart.Before(oldestWindow)) {
			oldestKey = key
			oldestRiskEnds = riskEnds
			oldestWindow = attempt.windowStart
		}
	}
	if oldestKey != "" {
		delete(m.attempts, oldestKey)
	}
}

func normalizeSessionMetadata(metadata SessionMetadata) SessionMetadata {
	metadata.IPAddress = boundedSessionText(metadata.IPAddress, 64)
	metadata.UserAgent = boundedSessionText(metadata.UserAgent, 512)
	return metadata
}

func boundedSessionText(value string, maximum int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func constantTimeStringEqual(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}
