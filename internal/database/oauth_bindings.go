package database

import (
	"context"
	"newpress.local/newpress/internal/auth"
)

func (r *UserRepository) UpdateOAuthBinding(ctx context.Context, id, version int64, provider, key string, actors ...string) (auth.User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return auth.User{}, err
	}
	defer tx.Rollback(ctx)
	u, err := queryAuthUserByID(ctx, tx, id, true)
	if err != nil {
		return auth.User{}, err
	}
	if u.Status != auth.UserStatusActive && !(len(actors) > 0 && key == "" && u.Status == auth.UserStatusSuspended) {
		return auth.User{}, auth.ErrUserNotFound
	}
	if u.Version != version {
		return auth.User{}, auth.ErrUserVersionConflict
	}
	old := u.OAuthBindings[provider]
	if old == key {
		return u, nil
	}
	action := "user.oauth_bound"
	if key != "" {
		if old != "" {
			return auth.User{}, auth.ErrRegistrationConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO np_oauth_bindings(user_id,provider,external_key) VALUES($1,$2,$3)`, id, provider, key)
	} else {
		remaining := len(u.OAuthBindings) - 1
		if !u.PasswordLogin && remaining == 0 {
			return auth.User{}, auth.ErrLastLoginMethod
		}
		action = "user.oauth_unbound"
		_, err = tx.Exec(ctx, `DELETE FROM np_oauth_bindings WHERE user_id=$1 AND provider=$2`, id, provider)
	}
	if err != nil {
		return auth.User{}, mapAuthUserWriteError("update OAuth binding", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE np_users SET version=version+1,updated_at=now() WHERE id=$1`, id); err != nil {
		return auth.User{}, err
	}
	actor := u.Username
	if len(actors) > 0 {
		actor = actors[0]
	}
	if err = insertAuthAudit(ctx, tx, auth.UserMutation{Actor: actor}, action, id, map[string]any{"provider": provider, "version": version + 1}); err != nil {
		return auth.User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return auth.User{}, err
	}
	return r.GetUser(ctx, id)
}
