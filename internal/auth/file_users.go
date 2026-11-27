package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/packagefs"
)

const (
	userDocumentSchema   = 1
	userDocumentKind     = "newpress/users"
	maxUserDocumentBytes = 16 << 20
)

type storedUser struct {
	Referral            ReferralData      `json:"referral,omitempty"`
	ID                  int64             `json:"id"`
	Username            string            `json:"username"`
	Email               string            `json:"email"`
	Phone               string            `json:"phone,omitempty"`
	PhoneVerifiedAt     *time.Time        `json:"phone_verified_at,omitempty"`
	OAuthBindings       map[string]string `json:"oauth_bindings,omitempty"`
	RegistrationKey     string            `json:"registration_key,omitempty"`
	RegistrationVersion int64             `json:"registration_version,omitempty"`
	RegistrationDigest  string            `json:"registration_digest,omitempty"`
	DisablePassword     bool              `json:"disable_password,omitempty"`
	PasswordHash        string            `json:"password_hash"`
	DisplayName         string            `json:"display_name"`
	Bio                 string            `json:"bio,omitempty"`
	AvatarMediaID       int64             `json:"avatar_media_id,omitempty"`
	Status              string            `json:"status"`
	Locale              string            `json:"locale"`
	Version             int64             `json:"version"`
	RoleSlugs           []string          `json:"role_slugs"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	PasswordChangedAt   time.Time         `json:"password_changed_at"`
	LastLoginAt         time.Time         `json:"last_login_at,omitempty"`
	EmailVerifiedAt     *time.Time        `json:"email_verified_at,omitempty"`
}

type userDocument struct {
	RegistrationCodes []RegistrationCode `json:"registration_codes,omitempty"`
	Schema            int                `json:"schema"`
	Kind              string             `json:"kind"`
	NextUserID        int64              `json:"next_user_id"`
	NextRoleID        int64              `json:"next_role_id"`
	UpdatedAt         time.Time          `json:"updated_at"`
	Roles             []Role             `json:"roles"`
	Users             []storedUser       `json:"users"`
	AccessTokens      []AccessToken      `json:"access_tokens,omitempty"`
}

// FileUserStore provides durable single-node identities for installations
// running without PostgreSQL. Passwords use Argon2id; administrator-retrievable
// registration codes live in the same private, atomically replaced document.
type FileUserStore struct {
	registrationCodes []RegistrationCode
	mu                sync.RWMutex
	path              string
	audit             *audit.Ledger
	auditFault        error
	nextUserID        int64
	nextRoleID        int64
	roles             map[string]Role
	users             map[int64]storedUser
	accessTokens      map[string]AccessToken
	updatedAt         time.Time
}

func NewFileUserStore(path string, ledgers ...*audit.Ledger) (*FileUserStore, error) {
	var ledger *audit.Ledger
	if len(ledgers) > 0 {
		ledger = ledgers[0]
	}
	store := &FileUserStore{
		path: path, audit: ledger, nextUserID: 1, nextRoleID: 1,
		roles: make(map[string]Role), users: make(map[int64]storedUser), accessTokens: make(map[string]AccessToken),
	}
	if path == "" {
		return store, nil
	}
	document, err := loadUserDocument(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := validateUserDocument(document); err != nil {
		return nil, fmt.Errorf("stored user document is invalid: %w", err)
	}
	store.registrationCodes = CloneRegistrationCodes(document.RegistrationCodes)
	store.nextUserID = document.NextUserID
	store.nextRoleID = document.NextRoleID
	store.updatedAt = document.UpdatedAt
	for _, role := range document.Roles {
		store.roles[role.Slug] = role
	}
	for _, user := range document.Users {
		store.users[user.ID] = user
	}
	for _, token := range document.AccessTokens {
		store.accessTokens[token.Hash] = token
	}
	return store, nil
}

func (store *FileUserStore) Ready(context.Context) error {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.path == "" {
		return nil
	}
	if store.auditFault != nil {
		return fmt.Errorf("user store audit failure: %w", store.auditFault)
	}
	if store.audit != nil {
		if err := store.audit.Ready(); err != nil {
			return fmt.Errorf("user store audit is not ready: %w", err)
		}
	}
	directory := filepath.Dir(store.path)
	info, err := packagefs.LstatNoSymlink(directory)
	if err != nil {
		return fmt.Errorf("inspect user store directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("user store parent is not a directory")
	}
	return nil
}

func (store *FileUserStore) EnsureRoles(_ context.Context, roles []Role) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	candidateRoles := cloneRoleMap(store.roles)
	nextRoleID := store.nextRoleID
	changed := false
	for _, input := range roles {
		role, err := normalizedRole(input)
		if err != nil {
			return err
		}
		if existing, ok := candidateRoles[role.Slug]; ok {
			role.ID = existing.ID
			if rolesEqual(existing, role) {
				continue
			}
		} else {
			role.ID = nextRoleID
			nextRoleID++
		}
		candidateRoles[role.Slug] = role
		changed = true
	}
	if !changed {
		return nil
	}
	updatedAt := time.Now().UTC().Truncate(time.Second)
	event := audit.Event{Action: "user.roles.ensure", ObjectType: "role_registry", ObjectID: "all", Actor: "system", Metadata: map[string]any{
		"changed_roles": changedRoleSlugs(store.roles, candidateRoles), "role_count": len(candidateRoles),
	}}
	if err := store.persistCandidate(store.users, candidateRoles, store.nextUserID, nextRoleID, updatedAt, &event); err != nil {
		return err
	}
	store.roles = candidateRoles
	store.nextRoleID = nextRoleID
	store.updatedAt = updatedAt
	return nil
}

func (store *FileUserStore) CountManagedUsers(context.Context) (int, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	count := 0
	for _, user := range store.users {
		if user.Status != UserStatusSystem {
			count++
		}
	}
	return count, nil
}

func (store *FileUserStore) FindUserByLogin(_ context.Context, login string) (User, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, user := range store.users {
		if user.Status == UserStatusSystem {
			continue
		}
		if strings.EqualFold(user.Username, login) || (login != "" && (strings.EqualFold(user.Email, login) || user.Phone == login)) {
			return store.userLocked(user), nil
		}
	}
	return User{}, ErrUserNotFound
}

func (store *FileUserStore) GetUser(_ context.Context, id int64) (User, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	user, ok := store.users[id]
	if !ok || user.Status == UserStatusSystem {
		return User{}, ErrUserNotFound
	}
	return store.userLocked(user), nil
}

func (store *FileUserStore) ListUsers(_ context.Context, query UserQuery) (UserList, error) {
	normalized, err := NormalizeUserQuery(query)
	if err != nil {
		return UserList{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	statusCounts := map[string]int{UserStatusActive: 0, UserStatusSuspended: 0}
	roleCounts := make(map[string]int, len(store.roles))
	matching := make([]storedUser, 0, len(store.users))
	search := strings.ToLower(normalized.Search)
	for _, user := range store.users {
		if user.Status == UserStatusSystem {
			continue
		}
		statusCounts[user.Status]++
		for _, role := range user.RoleSlugs {
			roleCounts[role]++
		}
		if normalized.Status != "" && user.Status != normalized.Status {
			continue
		}
		if filter := normalized.OAuthProvider; filter != "" {
			if filter == "bound" && len(user.OAuthBindings) == 0 || filter == "unbound" && len(user.OAuthBindings) > 0 || filter != "bound" && filter != "unbound" && user.OAuthBindings[filter] == "" {
				continue
			}
		}
		if normalized.Role != "" && !storedUserHasRole(user, normalized.Role) {
			continue
		}
		if normalized.ExcludeUserID > 0 && user.ID == normalized.ExcludeUserID {
			continue
		}
		if normalized.AvatarMediaID > 0 && user.AvatarMediaID != normalized.AvatarMediaID {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(user.Username), search) &&
			(normalized.PublicOnly || !strings.Contains(strings.ToLower(user.Email), search)) && !strings.Contains(strings.ToLower(user.DisplayName), search) {
			continue
		}
		matching = append(matching, user)
	}
	sort.Slice(matching, func(left, right int) bool {
		if matching[left].UpdatedAt.Equal(matching[right].UpdatedAt) {
			return matching[left].ID > matching[right].ID
		}
		return matching[left].UpdatedAt.After(matching[right].UpdatedAt)
	})
	total := len(matching)
	start := min(normalized.Offset, total)
	end := min(start+normalized.Limit, total)
	items := make([]User, 0, end-start)
	for _, user := range matching[start:end] {
		items = append(items, store.userLocked(user))
	}
	return UserList{Items: items, Total: total, StatusCounts: statusCounts, RoleCounts: roleCounts}, nil
}

func (store *FileUserStore) ListRoles(context.Context) ([]Role, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	counts := make(map[string]int, len(store.roles))
	for _, user := range store.users {
		if user.Status == UserStatusSystem {
			continue
		}
		for _, slug := range user.RoleSlugs {
			counts[slug]++
		}
	}
	roles := make([]Role, 0, len(store.roles))
	for _, role := range store.roles {
		role = cloneRoles([]Role{role})[0]
		role.UserCount = counts[role.Slug]
		roles = append(roles, role)
	}
	sortRoles(roles)
	return roles, nil
}

func (store *FileUserStore) CreateUser(_ context.Context, mutation UserMutation, input UserInput, passwordHash string) (User, error) {
	normalized, fields := NormalizeUserInput(input)
	if len(fields) > 0 {
		return User{}, &UserValidationError{Fields: fields}
	}
	if err := ValidatePasswordHash(passwordHash); err != nil {
		return User{}, &UserValidationError{Fields: map[string]string{"password": "密码哈希格式不正确"}}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.validateRolesLocked(normalized.RoleSlugs); err != nil {
		return User{}, err
	}
	if err := store.uniqueUserLocked(0, normalized.Username, normalized.Email); err != nil {
		return User{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	user := storedUser{
		ID: store.nextUserID, Username: normalized.Username, Email: normalized.Email, PasswordHash: passwordHash,
		DisplayName: normalized.DisplayName, Status: normalized.Status, Locale: normalized.Locale,
		Version: 1, RoleSlugs: append([]string(nil), normalized.RoleSlugs...),
		CreatedAt: now, UpdatedAt: now, PasswordChangedAt: now,
	}
	if err := store.registrationUniqueLocked(0, normalized); err != nil {
		return User{}, err
	}
	user.Phone = normalized.Phone
	user.OAuthBindings = initialOAuthBindings(normalized)
	user.RegistrationKey = normalized.RegistrationKey
	if normalized.RegistrationKey != "" {
		user.RegistrationVersion = 1
	}
	user.RegistrationDigest = normalized.RegistrationDigest
	user.DisablePassword = normalized.DisablePassword
	if normalized.Account != nil {
		user.Bio, user.AvatarMediaID = normalized.Account.Bio, normalized.Account.AvatarMediaID
	}
	if normalized.EmailVerified && normalized.Email != "" {
		user.EmailVerifiedAt = &now
	}
	if normalized.PhoneVerified && normalized.Phone != "" {
		user.PhoneVerifiedAt = &now
	}
	candidateUsers := cloneStoredUsers(store.users)
	candidateUsers[user.ID] = user
	if err := store.applyReferral(candidateUsers, user.ID, normalized, now); err != nil {
		return User{}, err
	}
	user = candidateUsers[user.ID]
	updatedAt := now
	actor := normalizedAuditActor(mutation.Actor, "system")
	event := audit.Event{Action: "user.created", ObjectType: "user", ObjectID: fmt.Sprint(user.ID), Actor: actor, Metadata: map[string]any{
		"version": user.Version, "status": user.Status, "role_count": len(user.RoleSlugs),
	}}
	candidateCodes, err := ConsumeRegistrationCode(store.registrationCodes, normalized.RegistrationCodeHash, now)
	if err != nil {
		return User{}, err
	}
	if err := store.persistCandidateWithCodes(candidateUsers, store.roles, store.nextUserID+1, store.nextRoleID, updatedAt, &event, store.accessTokens, candidateCodes); err != nil {
		return User{}, err
	}
	store.registrationCodes = candidateCodes
	store.users = candidateUsers
	store.nextUserID++
	store.updatedAt = updatedAt
	return store.userLocked(user), nil
}

func (store *FileUserStore) UpdateUser(_ context.Context, id int64, mutation UserMutation, input UserInput) (User, error) {
	normalized, fields := NormalizeUserInput(input)
	if len(fields) > 0 {
		return User{}, &UserValidationError{Fields: fields}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.users[id]
	if !ok || current.Status == UserStatusSystem {
		return User{}, ErrUserNotFound
	}
	if mutation.ExpectedVersion <= 0 || current.Version != mutation.ExpectedVersion {
		return User{}, ErrUserVersionConflict
	}
	if err := store.validateRolesLocked(normalized.RoleSlugs); err != nil {
		return User{}, err
	}
	if err := store.uniqueUserLocked(id, normalized.Username, normalized.Email); err != nil {
		return User{}, err
	}
	if normalized.Account != nil {
		if err := store.registrationUniqueLocked(id, UserInput{Phone: normalized.Account.Phone}); err != nil {
			return User{}, err
		}
	}
	losingAdministrator := current.Status == UserStatusActive && storedUserHasRole(current, "administrator") &&
		(normalized.Status != UserStatusActive || !stringSliceContains(normalized.RoleSlugs, "administrator"))
	if losingAdministrator && store.activeAdministratorCountLocked() <= 1 {
		return User{}, ErrLastAdministrator
	}
	now := time.Now().UTC().Truncate(time.Second)
	current.Username = normalized.Username
	if current.Email != normalized.Email {
		current.EmailVerifiedAt = nil
	}
	current.Email = normalized.Email
	current.DisplayName = normalized.DisplayName
	current.Status = normalized.Status
	current.Locale = normalized.Locale
	if account := normalized.Account; account != nil {
		if current.Phone != account.Phone {
			current.PhoneVerifiedAt = nil
		}
		current.Phone, current.Bio, current.AvatarMediaID = account.Phone, account.Bio, account.AvatarMediaID
		if !account.EmailVerified {
			current.EmailVerifiedAt = nil
		} else if current.EmailVerifiedAt == nil {
			current.EmailVerifiedAt = &now
		}
		if !account.PhoneVerified {
			current.PhoneVerifiedAt = nil
		} else if current.PhoneVerifiedAt == nil {
			current.PhoneVerifiedAt = &now
		}
	}
	current.RoleSlugs = append([]string(nil), normalized.RoleSlugs...)
	current.Version++
	current.UpdatedAt = now
	candidateUsers := cloneStoredUsers(store.users)
	candidateUsers[id] = current
	event := audit.Event{Action: "user.updated", ObjectType: "user", ObjectID: fmt.Sprint(id), Actor: normalizedAuditActor(mutation.Actor, "system"), Metadata: map[string]any{
		"from_version": mutation.ExpectedVersion, "version": current.Version, "status": current.Status, "role_count": len(current.RoleSlugs),
	}}
	if err := store.persistCandidate(candidateUsers, store.roles, store.nextUserID, store.nextRoleID, now, &event); err != nil {
		return User{}, err
	}
	store.users = candidateUsers
	store.updatedAt = now
	return store.userLocked(current), nil
}

func (store *FileUserStore) UpdateUserPassword(_ context.Context, id int64, mutation UserMutation, passwordHash string) (User, error) {
	if err := ValidatePasswordHash(passwordHash); err != nil {
		return User{}, &UserValidationError{Fields: map[string]string{"password": "密码哈希格式不正确"}}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.users[id]
	if !ok || current.Status == UserStatusSystem {
		return User{}, ErrUserNotFound
	}
	if mutation.ExpectedVersion <= 0 || current.Version != mutation.ExpectedVersion {
		return User{}, ErrUserVersionConflict
	}
	now := time.Now().UTC().Truncate(time.Second)
	current.DisablePassword = false
	current.PasswordHash = passwordHash
	current.PasswordChangedAt = now
	current.UpdatedAt = now
	current.Version++
	candidateUsers := cloneStoredUsers(store.users)
	candidateUsers[id] = current
	event := audit.Event{Action: "user.password_changed", ObjectType: "user", ObjectID: fmt.Sprint(id), Actor: normalizedAuditActor(mutation.Actor, "system"), Metadata: map[string]any{
		"from_version": mutation.ExpectedVersion, "version": current.Version,
	}}
	if err := store.persistCandidate(candidateUsers, store.roles, store.nextUserID, store.nextRoleID, now, &event); err != nil {
		return User{}, err
	}
	store.users = candidateUsers
	store.updatedAt = now
	return store.userLocked(current), nil
}

func (store *FileUserStore) RecordUserLogin(_ context.Context, id int64, at time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.users[id]
	if !ok || current.Status == UserStatusSystem {
		return ErrUserNotFound
	}
	if at.IsZero() {
		at = time.Now()
	}
	current.LastLoginAt = at.UTC().Truncate(time.Second)
	candidateUsers := cloneStoredUsers(store.users)
	candidateUsers[id] = current
	updatedAt := time.Now().UTC().Truncate(time.Second)
	event := audit.Event{Action: "user.login_recorded", ObjectType: "user", ObjectID: fmt.Sprint(id), Actor: normalizedAuditActor(current.Username, "system"), Metadata: map[string]any{
		"login_recorded": true,
	}}
	if err := store.persistCandidate(candidateUsers, store.roles, store.nextUserID, store.nextRoleID, updatedAt, &event); err != nil {
		return err
	}
	store.users = candidateUsers
	store.updatedAt = updatedAt
	return nil
}

func (store *FileUserStore) userLocked(user storedUser) User {
	roles := make([]Role, 0, len(user.RoleSlugs))
	for _, slug := range user.RoleSlugs {
		if role, ok := store.roles[slug]; ok {
			roles = append(roles, cloneRoles([]Role{role})[0])
		}
	}
	sortRoles(roles)
	return User{
		Referral: CloneReferral(user.Referral),
		Bio:      user.Bio, AvatarMediaID: user.AvatarMediaID,
		ID: user.ID, Username: user.Username, Email: user.Email, PasswordHash: user.PasswordHash,
		DisplayName: user.DisplayName, Status: user.Status, Locale: user.Locale, Version: user.Version,
		Roles: roles, CreatedAt: user.CreatedAt, UpdatedAt: user.UpdatedAt,
		PasswordChangedAt: user.PasswordChangedAt, LastLoginAt: user.LastLoginAt, EmailVerifiedAt: cloneTime(user.EmailVerifiedAt),
		Phone: user.Phone, PhoneVerifiedAt: cloneTime(user.PhoneVerifiedAt), OAuthBindings: cloneOAuthBindings(user.OAuthBindings), RegistrationKey: user.RegistrationKey, RegistrationDigest: user.RegistrationDigest, RegistrationVersion: user.RegistrationVersion, PasswordLogin: !user.DisablePassword,
	}
}

func (store *FileUserStore) validateRolesLocked(slugs []string) error {
	for _, slug := range slugs {
		if _, ok := store.roles[slug]; !ok {
			return fmt.Errorf("%w: %s", ErrRoleNotFound, slug)
		}
	}
	return nil
}

func (store *FileUserStore) uniqueUserLocked(exceptID int64, username, email string) error {
	for id, user := range store.users {
		if id == exceptID || user.Status == UserStatusSystem {
			continue
		}
		if strings.EqualFold(user.Username, username) {
			return ErrUsernameConflict
		}
		if email != "" && strings.EqualFold(user.Email, email) {
			return ErrEmailConflict
		}
	}
	return nil
}

func (store *FileUserStore) activeAdministratorCountLocked() int {
	count := 0
	for _, user := range store.users {
		if user.Status == UserStatusActive && storedUserHasRole(user, "administrator") {
			count++
		}
	}
	return count
}

func (store *FileUserStore) persistCandidate(users map[int64]storedUser, roles map[string]Role, nextUserID, nextRoleID int64, updatedAt time.Time, event *audit.Event) error {
	return store.persistCandidateWithAccess(users, roles, nextUserID, nextRoleID, updatedAt, event, store.accessTokens)
}

func (store *FileUserStore) persistCandidateWithAccess(users map[int64]storedUser, roles map[string]Role, nextUserID, nextRoleID int64, updatedAt time.Time, event *audit.Event, tokens map[string]AccessToken) error {
	return store.persistCandidateWithCodes(users, roles, nextUserID, nextRoleID, updatedAt, event, tokens, store.registrationCodes)
}

func (store *FileUserStore) persistCandidateWithCodes(users map[int64]storedUser, roles map[string]Role, nextUserID, nextRoleID int64, updatedAt time.Time, event *audit.Event, tokens map[string]AccessToken, codes []RegistrationCode) error {
	if store.path == "" {
		return nil
	}
	document := userDocument{
		RegistrationCodes: CloneRegistrationCodes(codes),
		Schema:            userDocumentSchema, Kind: userDocumentKind, NextUserID: nextUserID, NextRoleID: nextRoleID,
		UpdatedAt: updatedAt, Roles: make([]Role, 0, len(roles)), Users: make([]storedUser, 0, len(users)),
	}
	for _, role := range roles {
		document.Roles = append(document.Roles, cloneRoles([]Role{role})[0])
	}
	sortRoles(document.Roles)
	for _, user := range users {
		user.RoleSlugs = append([]string(nil), user.RoleSlugs...)
		user.OAuthBindings = cloneOAuthBindings(user.OAuthBindings)
		document.Users = append(document.Users, user)
	}
	sort.Slice(document.Users, func(left, right int) bool { return document.Users[left].ID < document.Users[right].ID })
	for _, token := range tokens {
		document.AccessTokens = append(document.AccessTokens, token)
	}
	sort.Slice(document.AccessTokens, func(i, j int) bool { return document.AccessTokens[i].Hash < document.AccessTokens[j].Hash })
	encoded, err := encodeUserDocument(document)
	if err != nil {
		return err
	}
	return store.persistEncoded(encoded, event)
}

func normalizedRole(role Role) (Role, error) {
	role.Slug = strings.ToLower(strings.TrimSpace(role.Slug))
	role.Name = strings.TrimSpace(role.Name)
	role.Description = strings.TrimSpace(role.Description)
	if !validRoleSlug(role.Slug) || !validUserText(role.Name, 1, 120) || !validUserText(role.Description, 0, 500) {
		return Role{}, errors.New("role metadata is invalid")
	}
	set := make(map[string]struct{}, len(role.Capabilities))
	capabilities := make([]string, 0, len(role.Capabilities))
	for _, capability := range role.Capabilities {
		capability = strings.ToLower(strings.TrimSpace(capability))
		if capability == "" || len(capability) > 120 || containsUserControl(capability) {
			return Role{}, errors.New("role capability is invalid")
		}
		if _, exists := set[capability]; exists {
			continue
		}
		set[capability] = struct{}{}
		capabilities = append(capabilities, capability)
	}
	sort.Strings(capabilities)
	role.Capabilities = capabilities
	return role, nil
}

func sortRoles(roles []Role) {
	priority := map[string]int{"administrator": 0, "editor": 1, "author": 2, "subscriber": 3}
	sort.Slice(roles, func(left, right int) bool {
		leftPriority, leftKnown := priority[roles[left].Slug]
		rightPriority, rightKnown := priority[roles[right].Slug]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftKnown && leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return roles[left].Slug < roles[right].Slug
	})
}

func rolesEqual(left, right Role) bool {
	if left.ID != right.ID || left.Slug != right.Slug || left.Name != right.Name || left.Description != right.Description || left.BuiltIn != right.BuiltIn || len(left.Capabilities) != len(right.Capabilities) {
		return false
	}
	for index := range left.Capabilities {
		if left.Capabilities[index] != right.Capabilities[index] {
			return false
		}
	}
	return true
}

func storedUserHasRole(user storedUser, slug string) bool {
	return stringSliceContains(user.RoleSlugs, slug)
}

func stringSliceContains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func cloneStoredUsers(source map[int64]storedUser) map[int64]storedUser {
	result := make(map[int64]storedUser, len(source))
	for id, user := range source {
		user.Referral = CloneReferral(user.Referral)
		user.RoleSlugs = append([]string(nil), user.RoleSlugs...)
		user.EmailVerifiedAt = cloneTime(user.EmailVerifiedAt)
		user.PhoneVerifiedAt = cloneTime(user.PhoneVerifiedAt)
		result[id] = user
	}
	return result
}

func cloneRoleMap(source map[string]Role) map[string]Role {
	result := make(map[string]Role, len(source))
	for slug, role := range source {
		result[slug] = cloneRoles([]Role{role})[0]
	}
	return result
}

func validateUserDocument(document userDocument) error {
	if len(document.RegistrationCodes) > MaxRegistrationCodes {
		return errors.New("too many registration codes")
	}
	codeIDs := map[string]bool{}
	codeValues := map[string]bool{}
	for _, code := range document.RegistrationCodes {
		if !ValidRegistrationCode(code) || codeIDs[code.ID] || codeValues[code.Code] {
			return errors.New("invalid or duplicate registration code")
		}
		codeIDs[code.ID] = true
		codeValues[code.Code] = true
	}
	if len(document.AccessTokens) > MaxAccessTokens {
		return errors.New("too many account access records")
	}
	tokenHashes := map[string]bool{}
	tokenEmails := map[string]bool{}
	for _, token := range document.AccessTokens {
		key := token.Email + "|" + token.Kind
		if !ValidAccessRecord(token) || tokenHashes[token.Hash] || tokenEmails[key] {
			return errors.New("invalid or duplicate account access record")
		}
		tokenHashes[token.Hash], tokenEmails[key] = true, true
	}
	if document.Schema != userDocumentSchema || document.Kind != userDocumentKind {
		return errors.New("unsupported user document metadata")
	}
	if document.NextUserID <= 0 || document.NextRoleID <= 0 {
		return errors.New("invalid user document identity counters")
	}
	roles := make(map[string]Role, len(document.Roles))
	roleIDs := make(map[int64]struct{}, len(document.Roles))
	for _, input := range document.Roles {
		role, err := normalizedRole(input)
		if err != nil || role.ID <= 0 {
			return errors.New("invalid stored role")
		}
		if _, exists := roles[role.Slug]; exists {
			return fmt.Errorf("duplicate stored role %q", role.Slug)
		}
		if _, exists := roleIDs[role.ID]; exists {
			return fmt.Errorf("duplicate stored role id %d", role.ID)
		}
		roles[role.Slug] = role
		roleIDs[role.ID] = struct{}{}
		if role.ID >= document.NextRoleID {
			return errors.New("next role id does not exceed stored ids")
		}
	}
	userIDs := make(map[int64]struct{}, len(document.Users))
	usernames := make(map[string]struct{}, len(document.Users))
	emails := make(map[string]struct{}, len(document.Users))
	registrationValues := map[string]bool{}
	for _, user := range document.Users {
		profile, profileFields := NormalizeProfile(ProfileInput{DisplayName: user.DisplayName, Locale: user.Locale, Bio: user.Bio, AvatarMediaID: user.AvatarMediaID})
		if len(profileFields) > 0 || profile.Bio != user.Bio {
			return errors.New("invalid stored user profile")
		}
		if user.ID <= 0 || user.ID >= document.NextUserID || user.Version <= 0 || user.CreatedAt.IsZero() || user.UpdatedAt.IsZero() || user.PasswordChangedAt.IsZero() {
			return errors.New("invalid stored user metadata")
		}
		if user.EmailVerifiedAt != nil && (user.EmailVerifiedAt.IsZero() || user.EmailVerifiedAt.Before(user.CreatedAt)) {
			return errors.New("invalid email verification timestamp")
		}
		if _, exists := userIDs[user.ID]; exists {
			return fmt.Errorf("duplicate stored user id %d", user.ID)
		}
		input, fields := NormalizeUserInput(UserInput{
			Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Phone: user.Phone,
			Status: user.Status, Locale: user.Locale, RoleSlugs: user.RoleSlugs,
		})
		if len(fields) > 0 || input.Username != user.Username || input.Email != user.Email || input.Phone != user.Phone || input.DisplayName != user.DisplayName || input.Status != user.Status || input.Locale != user.Locale {
			return fmt.Errorf("stored user %d failed validation", user.ID)
		}
		if err := ValidatePasswordHash(user.PasswordHash); err != nil {
			return fmt.Errorf("stored user %d has invalid password hash", user.ID)
		}
		for _, slug := range input.RoleSlugs {
			if _, ok := roles[slug]; !ok {
				return fmt.Errorf("stored user %d references unknown role %q", user.ID, slug)
			}
		}
		usernameKey := strings.ToLower(user.Username)
		emailKey := strings.ToLower(user.Email)
		if _, exists := usernames[usernameKey]; exists {
			return fmt.Errorf("duplicate stored username %q", user.Username)
		}
		if _, exists := emails[emailKey]; exists && emailKey != "" {
			return fmt.Errorf("duplicate stored email %q", user.Email)
		}
		if user.PhoneVerifiedAt != nil && (user.Phone == "" || user.PhoneVerifiedAt.Before(user.CreatedAt)) {
			return errors.New("invalid phone verification timestamp")
		}
		for provider, key := range user.OAuthBindings {
			if !validOAuthProvider(provider) || !validRegistrationDigest(key) {
				return errors.New("invalid OAuth binding")
			}
			if registrationValues["external:"+key] {
				return errors.New("duplicate OAuth binding")
			}
			registrationValues["external:"+key] = true
		}
		for prefix, value := range map[string]string{"phone": user.Phone, "request": user.RegistrationKey} {
			if value != "" {
				if registrationValues[prefix+":"+value] {
					return errors.New("duplicate registration identity")
				}
				registrationValues[prefix+":"+value] = true
			}
		}
		for _, digest := range []string{user.RegistrationKey, user.RegistrationDigest} {
			if digest != "" && !validRegistrationDigest(digest) {
				return errors.New("invalid registration identity digest")
			}
		}
		if user.RegistrationVersion < 0 || user.RegistrationVersion > user.Version || (user.RegistrationKey == "") != (user.RegistrationDigest == "") {
			return errors.New("invalid registration receipt")
		}
		userIDs[user.ID] = struct{}{}
		usernames[usernameKey] = struct{}{}
		emails[emailKey] = struct{}{}
	}
	return nil
}

func loadUserDocument(path string) (userDocument, error) {
	resolvedPath := path
	file, info, err := packagefs.OpenRegularNoSymlink(resolvedPath)
	if errors.Is(err, os.ErrNotExist) {
		// A Windows interruption can leave the last committed document under
		// the rollback name between the two replacement renames.
		resolvedPath = path + ".bak"
		file, info, err = packagefs.OpenRegularNoSymlink(resolvedPath)
	}
	if err != nil {
		return userDocument{}, err
	}
	defer file.Close()
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxUserDocumentBytes {
		return userDocument{}, errors.New("user document size or type is invalid")
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxUserDocumentBytes+1))
	if err != nil {
		return userDocument{}, fmt.Errorf("read user document: %w", err)
	}
	if len(payload) == 0 || len(payload) > maxUserDocumentBytes {
		return userDocument{}, errors.New("user document size or type is invalid")
	}
	if err := validateUniqueUserJSON(payload); err != nil {
		return userDocument{}, fmt.Errorf("validate user document JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var document userDocument
	if err := decoder.Decode(&document); err != nil {
		return userDocument{}, fmt.Errorf("decode user document: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return userDocument{}, err
	}
	return document, nil
}

func writeUserDocument(path string, document userDocument) error {
	encoded, err := encodeUserDocument(document)
	if err != nil {
		return err
	}
	return writeUserDocumentBytes(path, encoded)
}

func encodeUserDocument(document userDocument) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(true)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode user document: %w", err)
	}
	if encoded.Len() > maxUserDocumentBytes {
		return nil, errors.New("user document exceeds size limit")
	}
	return encoded.Bytes(), nil
}

func (store *FileUserStore) persistEncoded(encoded []byte, event *audit.Event) error {
	write := func() error { return writeUserDocumentBytes(store.path, encoded) }
	if store.audit == nil || event == nil {
		return write()
	}
	if _, err := store.audit.ApplyFile(audit.FactUsers, *event, encoded, store.path, write); err != nil {
		store.auditFault = err
		return err
	}
	return nil
}

func writeUserDocumentBytes(path string, encoded []byte) error {
	directory := filepath.Dir(path)
	if err := packagefs.MkdirAllNoSymlink(directory, 0o750); err != nil {
		return fmt.Errorf("create user store directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".newpress-users-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary user document: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary user document: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary user document: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary user document: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary user document: %w", err)
	}
	if err := replaceUserFile(temporaryPath, path); err != nil {
		return err
	}
	if err := packagefs.ChmodRegularNoSymlink(path, 0o600); err != nil {
		return fmt.Errorf("protect user document: %w", err)
	}
	if runtime.GOOS != "windows" {
		if handle, err := packagefs.OpenDirectoryNoSymlink(directory); err == nil {
			_ = handle.Sync()
			_ = handle.Close()
		}
	}
	return nil
}

func changedRoleSlugs(before, after map[string]Role) []string {
	changed := make([]string, 0)
	for slug, role := range after {
		previous, ok := before[slug]
		if !ok || !rolesEqual(previous, role) {
			changed = append(changed, slug)
		}
	}
	sort.Strings(changed)
	return changed
}

func normalizedAuditActor(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || len([]rune(value)) > 160 || containsUserControl(value) {
		return fallback
	}
	return value
}

func replaceUserFile(temporaryPath, target string) error {
	return packagefs.ReplaceRegularFileNoSymlink(temporaryPath, target)
}

func validateUniqueUserJSON(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := consumeUniqueUserJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func consumeUniqueUserJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key must be a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeUniqueUserJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if value, ok := closing.(json.Delim); !ok || value != '}' {
			return errors.New("JSON object was not closed")
		}
		return nil
	case '[':
		for decoder.More() {
			if err := consumeUniqueUserJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if value, ok := closing.(json.Delim); !ok || value != ']' {
			return errors.New("JSON array was not closed")
		}
		return nil
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing JSON token %v", token)
	}
	return nil
}

var _ UserStore = (*FileUserStore)(nil)
