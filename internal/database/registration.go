package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"newpress.local/newpress/internal/auth"
)

func (r *UserRepository) FindRegistration(ctx context.Context, kind, key string) (auth.User, error) {
	column := map[string]string{"external": "external_key", "request": "registration_key", "phone": "phone", "email": "email"}[kind]
	if column == "" || key == "" {
		return auth.User{}, auth.ErrUserNotFound
	}
	condition := `u.` + column + `=$1`
	if kind == "external" {
		condition = `EXISTS (SELECT 1 FROM np_oauth_bindings b WHERE b.user_id=u.id AND b.external_key=$1)`
	}
	u, err := scanAuthUser(r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM np_users u WHERE `+condition, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrUserNotFound
	}
	if err != nil {
		return auth.User{}, err
	}
	users := []auth.User{u}
	if err = hydrateUserRoles(ctx, r.pool, users); err != nil {
		return auth.User{}, err
	}
	return users[0], nil
}

func (r *UserRepository) BindRegistration(ctx context.Context, id, version int64, input auth.UserInput, passwordHash string) (auth.User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return auth.User{}, err
	}
	defer tx.Rollback(ctx)
	u, err := queryAuthUserByID(ctx, tx, id, true)
	if err != nil {
		return auth.User{}, err
	}
	if u.Version != version {
		return auth.User{}, auth.ErrUserVersionConflict
	}
	matched := false
	for _, key := range u.OAuthBindings {
		if key == input.ExternalKey {
			matched = true
		}
	}
	if u.Status != auth.UserStatusActive || !matched {
		return auth.User{}, auth.ErrUserNotFound
	}
	input.Status = u.Status
	input.RoleSlugs = []string{"subscriber"}
	n, fields := auth.NormalizeUserInput(input)
	if len(fields) > 0 {
		return auth.User{}, &auth.UserValidationError{Fields: fields}
	}
	if passwordHash != "" {
		if err = auth.ValidatePasswordHash(passwordHash); err != nil {
			return auth.User{}, err
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	// Reused parameters need one explicit type: email's varchar assignment and
	// its text equality operator otherwise infer incompatible types for $4.
	_, err = tx.Exec(ctx, `UPDATE np_users SET
		username=$2, display_name=$3, email=$4::text, phone=$5::text,
		email_verified_at=CASE WHEN $6 THEN $8::timestamptz WHEN email=$4::text THEN email_verified_at ELSE NULL END,
		phone_verified_at=CASE WHEN $7 THEN $8::timestamptz WHEN phone=$5::text THEN phone_verified_at ELSE NULL END,
		password_hash=CASE WHEN $9::text='' THEN password_hash ELSE $9::text END,
		password_login=CASE WHEN $9::text='' THEN password_login ELSE true END,
		password_changed_at=CASE WHEN $9::text='' THEN password_changed_at ELSE $8::timestamptz END,
		registration_key=$10, registration_digest=$11, registration_version=version+1,
		version=version+1, updated_at=$8::timestamptz WHERE id=$1`,
		id, n.Username, n.DisplayName, n.Email, n.Phone, n.EmailVerified, n.PhoneVerified, now, passwordHash, n.RegistrationKey, n.RegistrationDigest)
	if err != nil {
		return auth.User{}, mapAuthUserWriteError("bind user", err)
	}
	if err = insertAuthAudit(ctx, tx, auth.UserMutation{Actor: "self-service"}, "user.identity_bound", id, map[string]any{"version": version + 1}); err != nil {
		return auth.User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return auth.User{}, err
	}
	return r.GetUser(ctx, id)
}
