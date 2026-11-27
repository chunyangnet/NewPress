package auth

import (
	"context"
	"errors"
	"time"

	"newpress.local/newpress/internal/audit"
)

var ErrLastLoginMethod = errors.New("cannot remove last login method")

func initialOAuthBindings(input UserInput) map[string]string {
	bindings := map[string]string{}
	if input.ExternalKey != "" {
		bindings[input.OAuthProvider] = input.ExternalKey
	}
	return bindings
}

func validOAuthProvider(id string) bool {
	switch id {
	case "github", "google", "microsoft", "linuxdo", "qq", "wechat":
		return true
	}
	return false
}

func cloneOAuthBindings(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for id, key := range source {
		result[id] = key
	}
	return result
}

func storedHasExternal(u storedUser, key string) bool {
	if key == "" {
		return false
	}
	for _, value := range u.OAuthBindings {
		if value == key {
			return true
		}
	}
	return false
}

type OAuthBindingStore interface {
	UpdateOAuthBinding(context.Context, int64, int64, string, string, ...string) (User, error)
}

// An empty key removes a binding. Nonempty keys must come from a verified OAuth callback.
func (m *Manager) UpdateOAuthBinding(ctx context.Context, user User, provider, key string) (User, error) {
	if !validOAuthProvider(provider) || key != "" && !validRegistrationDigest(key) {
		return User{}, ErrAccessInvalid
	}
	store, ok := m.users.(OAuthBindingStore)
	if !ok {
		return User{}, ErrUserStoreNotAvailable
	}
	return store.UpdateOAuthBinding(ctx, user.ID, user.Version, provider, key)
}

func (m *Manager) AdminRemoveOAuthBinding(ctx context.Context, actor Principal, user User, provider string) (User, error) {
	if !actor.Can(CapabilityUsersManage) || !validOAuthProvider(provider) {
		return User{}, ErrAccessInvalid
	}
	store, ok := m.users.(OAuthBindingStore)
	if !ok {
		return User{}, ErrUserStoreNotAvailable
	}
	updated, err := store.UpdateOAuthBinding(ctx, user.ID, user.Version, provider, "", actor.Username)
	if err == nil {
		if err := m.RevokeUserSessionsContext(ctx, user.ID); err != nil {
			return updated, err
		}
	}
	return updated, err
}

func (s *FileUserStore) UpdateOAuthBinding(ctx context.Context, id, version int64, provider, key string, actors ...string) (User, error) {
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	if !validOAuthProvider(provider) || key != "" && !validRegistrationDigest(key) {
		return User{}, ErrAccessInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok || u.Status != UserStatusActive && !(len(actors) > 0 && key == "" && u.Status == UserStatusSuspended) {
		return User{}, ErrUserNotFound
	}
	if u.Version != version {
		return User{}, ErrUserVersionConflict
	}
	old := u.OAuthBindings[provider]
	if old == key {
		return s.userLocked(u), nil
	}
	if key != "" {
		if old != "" {
			return User{}, ErrRegistrationConflict
		}
		if err := s.registrationUniqueLocked(id, UserInput{ExternalKey: key}); err != nil {
			return User{}, err
		}
	} else {
		remaining := len(u.OAuthBindings) - 1
		if u.DisablePassword && remaining == 0 {
			return User{}, ErrLastLoginMethod
		}
	}
	u.OAuthBindings = cloneOAuthBindings(u.OAuthBindings)
	if key == "" {
		delete(u.OAuthBindings, provider)
	} else {
		u.OAuthBindings[provider] = key
	}
	now := time.Now().UTC().Truncate(time.Second)
	u.Version++
	u.UpdatedAt = now
	candidate := cloneStoredUsers(s.users)
	candidate[id] = u
	action := "user.oauth_bound"
	if key == "" {
		action = "user.oauth_unbound"
	}
	actor := u.Username
	if len(actors) > 0 {
		actor = actors[0]
	}
	event := audit.Event{Action: action, ObjectType: "user", Actor: actor, Metadata: map[string]any{"user_id": id, "provider": provider, "version": u.Version}}
	if err := s.persistCandidate(candidate, s.roles, s.nextUserID, s.nextRoleID, now, &event); err != nil {
		return User{}, err
	}
	s.users = candidate
	s.updatedAt = now
	return s.userLocked(u), nil
}
