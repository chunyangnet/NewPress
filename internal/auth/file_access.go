package auth

import (
	"context"
	"sort"
	"strconv"
	"time"

	"newpress.local/newpress/internal/audit"
)

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	copy := *t
	return &copy
}

func RetainAccess(t AccessToken, now time.Time) bool {
	return now.Before(t.ExpiresAt) || now.Before(t.WindowAt.Add(time.Hour))
}

func cloneAccess(source map[string]AccessToken) map[string]AccessToken {
	result := make(map[string]AccessToken, len(source))
	for key, t := range source {
		t.ConsumedAt = cloneTime(t.ConsumedAt)
		result[key] = t
	}
	return result
}

func (s *FileUserStore) IssueAccess(ctx context.Context, input AccessToken, actor string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidAccessRecord(input) || input.ConsumedAt != nil || input.Requests != 1 || !input.WindowAt.Equal(input.CreatedAt) {
		return ErrAccessInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := []AccessToken{}
	candidate := map[string]AccessToken{}
	for key, old := range s.accessTokens {
		if old.Email == input.Email {
			previous = append(previous, old)
		}
		if RetainAccess(old, input.CreatedAt) && !(old.Email == input.Email && old.Kind == input.Kind) {
			candidate[key] = old
		}
	}
	input, err := AccessWindow(input, previous)
	if err != nil {
		return err
	}
	if len(candidate) >= MaxAccessTokens {
		return ErrRateLimited
	}
	candidate[input.Hash] = input
	event := audit.Event{Action: "user.access_issued", ObjectType: "user", ObjectID: strconv.FormatInt(input.UserID, 10), Actor: normalizedAuditActor(actor, "self-service"), Metadata: map[string]any{"kind": input.Kind}}
	if err := s.persistCandidateWithAccess(s.users, s.roles, s.nextUserID, s.nextRoleID, input.CreatedAt, &event, candidate); err != nil {
		return err
	}
	s.accessTokens, s.updatedAt = candidate, input.CreatedAt
	return nil
}

func (s *FileUserStore) LookupAccess(ctx context.Context, hash, kind string, now time.Time) (AccessToken, error) {
	if err := ctx.Err(); err != nil {
		return AccessToken{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.accessTokens[hash]
	if !ok || !AccessUsable(t, kind, now) {
		return AccessToken{}, ErrAccessInvalid
	}
	if kind == AccessRecover {
		u, ok := s.users[t.UserID]
		if !ok || u.Status != UserStatusActive || u.Version != t.UserVersion || u.Email != t.Email {
			return AccessToken{}, ErrAccessInvalid
		}
	}
	t.ConsumedAt = cloneTime(t.ConsumedAt)
	return t, nil
}

func (s *FileUserStore) RedeemAccess(ctx context.Context, hash, kind string, input UserInput, passwordHash string, now time.Time) (User, error) {
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	if err := ValidatePasswordHash(passwordHash); err != nil {
		return User{}, ErrAccessInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.accessTokens[hash]
	if !ok || !AccessUsable(t, kind, now) {
		return User{}, ErrAccessInvalid
	}
	now = now.UTC().Truncate(time.Second)
	nextID := s.nextUserID
	var u storedUser
	action := "user.registered"
	if kind == AccessRecover {
		u, ok = s.users[t.UserID]
		if !ok || u.Status != UserStatusActive || u.Version != t.UserVersion || u.Email != t.Email {
			return User{}, ErrAccessInvalid
		}
		u.DisablePassword = false
		u.PasswordHash, u.PasswordChangedAt, u.UpdatedAt = passwordHash, now, now
		u.Version++
		action = "user.password_recovered"
	} else {
		input.Email, input.Status, input.RoleSlugs = t.Email, UserStatusActive, []string{"subscriber"}
		normalized, fields := NormalizeUserInput(input)
		if len(fields) > 0 {
			return User{}, &UserValidationError{Fields: fields}
		}
		if err := s.validateRolesLocked(normalized.RoleSlugs); err != nil {
			return User{}, err
		}
		if err := s.uniqueUserLocked(0, normalized.Username, normalized.Email); err != nil {
			return User{}, err
		}
		if err := s.registrationUniqueLocked(0, normalized); err != nil {
			return User{}, err
		}
		u = storedUser{Phone: normalized.Phone, OAuthBindings: initialOAuthBindings(normalized), RegistrationKey: normalized.RegistrationKey, RegistrationDigest: normalized.RegistrationDigest, DisablePassword: normalized.DisablePassword, ID: nextID, Username: normalized.Username, Email: normalized.Email, DisplayName: normalized.DisplayName, Locale: normalized.Locale, Status: UserStatusActive, RoleSlugs: []string{"subscriber"}, PasswordHash: passwordHash, Version: 1, CreatedAt: now, UpdatedAt: now, PasswordChangedAt: now}
		if normalized.RegistrationKey != "" {
			u.RegistrationVersion = 1
		}
		if normalized.PhoneVerified && normalized.Phone != "" {
			u.PhoneVerifiedAt = &now
		}
		nextID++
	}
	u.EmailVerifiedAt = &now
	candidateUsers := cloneStoredUsers(s.users)
	candidateUsers[u.ID] = u
	if kind != AccessRecover {
		if err := s.applyReferral(candidateUsers, u.ID, input, now); err != nil {
			return User{}, err
		}
		u = candidateUsers[u.ID]
	}
	candidateTokens := cloneAccess(s.accessTokens)
	t.ConsumedAt = &now
	candidateTokens[hash] = t
	event := audit.Event{Action: action, ObjectType: "user", ObjectID: strconv.FormatInt(u.ID, 10), Actor: "self-service", Metadata: map[string]any{"version": u.Version, "kind": kind}}
	candidateCodes := s.registrationCodes
	if kind != AccessRecover {
		var err error
		candidateCodes, err = ConsumeRegistrationCode(s.registrationCodes, input.RegistrationCodeHash, now)
		if err != nil {
			return User{}, err
		}
	}
	if err := s.persistCandidateWithCodes(candidateUsers, s.roles, nextID, s.nextRoleID, now, &event, candidateTokens, candidateCodes); err != nil {
		return User{}, err
	}
	s.registrationCodes = candidateCodes
	s.users, s.accessTokens, s.nextUserID, s.updatedAt = candidateUsers, candidateTokens, nextID, now
	return s.userLocked(u), nil
}

func (s *FileUserStore) ListInvitations(ctx context.Context, now time.Time) ([]AccessToken, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := []AccessToken{}
	for _, t := range s.accessTokens {
		if t.Kind == AccessInvite && RetainAccess(t, now) {
			t.ConsumedAt = cloneTime(t.ConsumedAt)
			items = append(items, t)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items, nil
}

func (s *FileUserStore) RevokeInvitation(ctx context.Context, hash, actor string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.accessTokens[hash]
	if !ok || t.Kind != AccessInvite {
		return ErrAccessInvalid
	}
	if t.ConsumedAt != nil {
		return nil
	}
	now = now.UTC().Truncate(time.Second)
	candidate := cloneAccess(s.accessTokens)
	t.ConsumedAt = &now
	candidate[hash] = t
	event := audit.Event{Action: "user.invitation_revoked", ObjectType: "user", ObjectID: "0", Actor: normalizedAuditActor(actor, "system"), Metadata: map[string]any{"kind": AccessInvite}}
	if err := s.persistCandidateWithAccess(s.users, s.roles, s.nextUserID, s.nextRoleID, now, &event, candidate); err != nil {
		return err
	}
	s.accessTokens, s.updatedAt = candidate, now
	return nil
}
