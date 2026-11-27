package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/auth"
)

const accessColumns = `token_hash,kind,email,user_id,user_version,created_at,expires_at,consumed_at,window_at,requests`

func scanAccess(row scanner) (auth.AccessToken, error) {
	var t auth.AccessToken
	err := row.Scan(&t.Hash, &t.Kind, &t.Email, &t.UserID, &t.UserVersion, &t.CreatedAt, &t.ExpiresAt, &t.ConsumedAt, &t.WindowAt, &t.Requests)
	return t, err
}

func (r *UserRepository) IssueAccess(ctx context.Context, input auth.AccessToken, actor string) error {
	if !auth.ValidAccessRecord(input) || input.ConsumedAt != nil || input.Requests != 1 || !input.WindowAt.Equal(input.CreatedAt) {
		return auth.ErrAccessInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// A single bounded registry lock covers the global cap and per-email window.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(914014)`); err != nil {
		return err
	}
	previous := []auth.AccessToken{}
	rows, err := tx.Query(ctx, `SELECT `+accessColumns+` FROM np_account_access WHERE email=$1`, input.Email)
	if err != nil {
		return err
	}
	for rows.Next() {
		old, scanErr := scanAccess(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		previous = append(previous, old)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	input, err = auth.AccessWindow(input, previous)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM np_account_access WHERE expires_at <= $1 AND window_at + interval '1 hour' <= $1`, input.CreatedAt); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM np_account_access WHERE NOT(email=$1 AND kind=$2)`, input.Email, input.Kind).Scan(&count); err != nil {
		return err
	}
	if count >= auth.MaxAccessTokens {
		return auth.ErrRateLimited
	}
	_, err = tx.Exec(ctx, `INSERT INTO np_account_access (`+accessColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,NULL,$8,$9)
	 ON CONFLICT(email,kind) DO UPDATE SET token_hash=EXCLUDED.token_hash,user_id=EXCLUDED.user_id,user_version=EXCLUDED.user_version,created_at=EXCLUDED.created_at,expires_at=EXCLUDED.expires_at,consumed_at=NULL,window_at=EXCLUDED.window_at,requests=EXCLUDED.requests`, input.Hash, input.Kind, input.Email, input.UserID, input.UserVersion, input.CreatedAt, input.ExpiresAt, input.WindowAt, input.Requests)
	if err != nil {
		return err
	}
	if err = insertAuthAudit(ctx, tx, auth.UserMutation{Actor: actor}, "user.access_issued", input.UserID, map[string]any{"kind": input.Kind}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *UserRepository) LookupAccess(ctx context.Context, hash, kind string, now time.Time) (auth.AccessToken, error) {
	t, err := scanAccess(r.pool.QueryRow(ctx, `SELECT `+accessColumns+` FROM np_account_access WHERE token_hash=$1`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.AccessToken{}, auth.ErrAccessInvalid
	}
	if err != nil {
		return auth.AccessToken{}, err
	}
	if !auth.AccessUsable(t, kind, now) {
		return auth.AccessToken{}, auth.ErrAccessInvalid
	}
	if kind == auth.AccessRecover {
		u, err := r.GetUser(ctx, t.UserID)
		if err != nil && !errors.Is(err, auth.ErrUserNotFound) {
			return auth.AccessToken{}, err
		}
		if err != nil || u.Status != auth.UserStatusActive || u.Email != t.Email || u.Version != t.UserVersion {
			return auth.AccessToken{}, auth.ErrAccessInvalid
		}
	}
	return t, nil
}

func (r *UserRepository) RedeemAccess(ctx context.Context, hash, kind string, input auth.UserInput, passwordHash string, now time.Time) (auth.User, error) {
	if err := auth.ValidatePasswordHash(passwordHash); err != nil {
		return auth.User{}, auth.ErrAccessInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return auth.User{}, err
	}
	defer tx.Rollback(ctx)
	t, err := scanAccess(tx.QueryRow(ctx, `SELECT `+accessColumns+` FROM np_account_access WHERE token_hash=$1 FOR UPDATE`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrAccessInvalid
	}
	if err != nil {
		return auth.User{}, err
	}
	if !auth.AccessUsable(t, kind, now) {
		return auth.User{}, auth.ErrAccessInvalid
	}
	now = now.UTC().Truncate(time.Second)
	var id int64
	action := "user.registered"
	if kind == auth.AccessRecover {
		u, err := queryAuthUserByID(ctx, tx, t.UserID, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.User{}, auth.ErrAccessInvalid
		}
		if err != nil {
			return auth.User{}, err
		}
		if u.Status != auth.UserStatusActive || u.Email != t.Email || u.Version != t.UserVersion {
			return auth.User{}, auth.ErrAccessInvalid
		}
		id = u.ID
		_, err = tx.Exec(ctx, `UPDATE np_users SET password_hash=$2,password_login=true,password_changed_at=$3,updated_at=$3,email_verified_at=$3,version=version+1 WHERE id=$1`, id, passwordHash, now)
		if err != nil {
			return auth.User{}, err
		}
		action = "user.password_recovered"
	} else {
		input.Email, input.Status, input.RoleSlugs = t.Email, auth.UserStatusActive, []string{"subscriber"}
		normalized, fields := auth.NormalizeUserInput(input)
		if len(fields) > 0 {
			return auth.User{}, &auth.UserValidationError{Fields: fields}
		}
		roles, err := resolveAuthRoleIDs(ctx, tx, normalized.RoleSlugs)
		if err != nil {
			return auth.User{}, err
		}
		err = tx.QueryRow(ctx, `INSERT INTO np_users(username,email,password_hash,display_name,status,locale,version,created_at,updated_at,password_changed_at,email_verified_at,phone,phone_verified_at,registration_key,registration_digest,password_login,registration_version) VALUES ($1,$2,$3,$4,'active',$5,1,$6,$6,$6,$6,$7,CASE WHEN $8 THEN $6::timestamptz ELSE NULL END,$9,$10,$11,CASE WHEN $9='' THEN 0 ELSE 1 END) RETURNING id`, normalized.Username, normalized.Email, passwordHash, normalized.DisplayName, normalized.Locale, now, normalized.Phone, normalized.PhoneVerified, normalized.RegistrationKey, normalized.RegistrationDigest, !normalized.DisablePassword).Scan(&id)
		if err != nil {
			return auth.User{}, mapAuthUserWriteError("register user", err)
		}
		if err = consumeRegistrationCode(ctx, tx, normalized.RegistrationCodeHash); err != nil {
			return auth.User{}, err
		}
		if normalized.ExternalKey != "" {
			if _, err = tx.Exec(ctx, `INSERT INTO np_oauth_bindings(user_id,provider,external_key) VALUES($1,$2,$3)`, id, normalized.OAuthProvider, normalized.ExternalKey); err != nil {
				return auth.User{}, mapAuthUserWriteError("register OAuth binding", err)
			}
		}
		if err = consumeReferral(ctx, tx, id, normalized); err != nil {
			return auth.User{}, err
		}
		if err = replaceAuthUserRoles(ctx, tx, id, roles); err != nil {
			return auth.User{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE np_account_access SET consumed_at=$2 WHERE token_hash=$1`, hash, now); err != nil {
		return auth.User{}, err
	}
	if err = insertAuthAudit(ctx, tx, auth.UserMutation{Actor: "self-service"}, action, id, map[string]any{"kind": kind}); err != nil {
		return auth.User{}, err
	}
	u, err := queryAuthUserByID(ctx, tx, id, false)
	if err != nil {
		return auth.User{}, err
	}
	users := []auth.User{u}
	if err = hydrateUserRoles(ctx, tx, users); err != nil {
		return auth.User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return auth.User{}, err
	}
	return users[0], nil
}

func (r *UserRepository) ListInvitations(ctx context.Context, now time.Time) ([]auth.AccessToken, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+accessColumns+` FROM np_account_access WHERE kind='invite' AND (expires_at>$1 OR window_at + interval '1 hour'>$1) ORDER BY created_at DESC,token_hash LIMIT $2`, now, auth.MaxAccessTokens)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []auth.AccessToken{}
	for rows.Next() {
		t, err := scanAccess(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}

func (r *UserRepository) RevokeInvitation(ctx context.Context, hash, actor string, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	t, err := scanAccess(tx.QueryRow(ctx, `SELECT `+accessColumns+` FROM np_account_access WHERE token_hash=$1 AND kind='invite' FOR UPDATE`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrAccessInvalid
	}
	if err != nil {
		return err
	}
	if t.ConsumedAt != nil {
		return nil
	}
	if _, err = tx.Exec(ctx, `UPDATE np_account_access SET consumed_at=$2 WHERE token_hash=$1`, hash, now); err != nil {
		return err
	}
	if err = insertAuthAudit(ctx, tx, auth.UserMutation{Actor: actor}, "user.invitation_revoked", 0, map[string]any{"kind": auth.AccessInvite}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
