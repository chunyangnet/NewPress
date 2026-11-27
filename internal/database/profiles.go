package database

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/auth"
)

func (s *UserRepository) UpdateProfile(ctx context.Context, id int64, mutation auth.UserMutation, input auth.ProfileInput) (auth.User, error) {
	input, fields := auth.NormalizeProfile(input)
	if len(fields) > 0 {
		return auth.User{}, &auth.UserValidationError{Fields: fields}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return auth.User{}, err
	}
	defer tx.Rollback(ctx)
	u, err := queryAuthUserByID(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && u.Status != auth.UserStatusActive {
		return auth.User{}, auth.ErrUserNotFound
	}
	if err != nil {
		return auth.User{}, err
	}
	if mutation.ExpectedVersion <= 0 || u.Version != mutation.ExpectedVersion {
		return auth.User{}, auth.ErrUserVersionConflict
	}
	_, err = tx.Exec(ctx, `UPDATE np_users SET display_name=$2,locale=$3,bio=$4,avatar_media_id=$5,version=version+1,updated_at=date_trunc('second',now()) WHERE id=$1`, id, input.DisplayName, input.Locale, input.Bio, input.AvatarMediaID)
	if err != nil {
		return auth.User{}, err
	}
	if err := insertAuthAudit(ctx, tx, mutation, "user.profile_updated", id, map[string]any{"from_version": u.Version, "version": u.Version + 1, "avatar_media_id": input.AvatarMediaID}); err != nil {
		return auth.User{}, err
	}
	u, err = queryAuthUserByID(ctx, tx, id, false)
	if err != nil {
		return auth.User{}, err
	}
	users := []auth.User{u}
	if err := hydrateUserRoles(ctx, tx, users); err != nil {
		return auth.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return auth.User{}, err
	}
	return users[0], nil
}
