package auth

import (
	"context"
	"fmt"
	"newpress.local/newpress/internal/audit"
	"time"
)

func (s *FileUserStore) UpdateProfile(ctx context.Context, id int64, mutation UserMutation, input ProfileInput) (User, error) {
	input, fields := NormalizeProfile(input)
	if len(fields) > 0 {
		return User{}, &UserValidationError{Fields: fields}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	u, ok := s.users[id]
	if !ok || u.Status != UserStatusActive {
		return User{}, ErrUserNotFound
	}
	if mutation.ExpectedVersion <= 0 || u.Version != mutation.ExpectedVersion {
		return User{}, ErrUserVersionConflict
	}
	u.DisplayName, u.Locale, u.Bio, u.AvatarMediaID = input.DisplayName, input.Locale, input.Bio, input.AvatarMediaID
	u.Version++
	u.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	candidate := cloneStoredUsers(s.users)
	candidate[id] = u
	event := audit.Event{Action: "user.profile_updated", ObjectType: "user", ObjectID: fmt.Sprint(id), Actor: normalizedAuditActor(mutation.Actor, "system"), Metadata: map[string]any{"from_version": mutation.ExpectedVersion, "version": u.Version, "avatar_media_id": u.AvatarMediaID}}
	if err := s.persistCandidate(candidate, s.roles, s.nextUserID, s.nextRoleID, u.UpdatedAt, &event); err != nil {
		return User{}, err
	}
	s.users, s.updatedAt = candidate, u.UpdatedAt
	return s.userLocked(u), nil
}
