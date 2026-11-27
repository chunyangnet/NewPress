package database

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/auth"
)

const registrationCodeColumns = `id,code,enabled,max_uses,used,starts_at,expires_at,note,version,created_at,updated_at,last_used_at`

type registrationCodeQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func listRegistrationCodes(ctx context.Context, q registrationCodeQuerier) ([]auth.RegistrationCode, error) {
	rows, err := q.Query(ctx, `SELECT `+registrationCodeColumns+` FROM np_registration_codes ORDER BY created_at DESC,id LIMIT $1`, auth.MaxRegistrationCodes+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []auth.RegistrationCode{}
	for rows.Next() {
		var c auth.RegistrationCode
		if err = rows.Scan(&c.ID, &c.Code, &c.Enabled, &c.MaxUses, &c.Used, &c.StartsAt, &c.ExpiresAt, &c.Note, &c.Version, &c.CreatedAt, &c.UpdatedAt, &c.LastUsedAt); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

func (r *UserRepository) ListRegistrationCodes(ctx context.Context) ([]auth.RegistrationCode, error) {
	return listRegistrationCodes(ctx, r.pool)
}

func (r *UserRepository) MutateRegistrationCodes(ctx context.Context, input auth.RegistrationCodeMutation, actor string, now time.Time) ([]auth.RegistrationCode, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Shared with consumption, so batch edits cannot overwrite usage counters.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(914037)`); err != nil {
		return nil, err
	}
	current, err := listRegistrationCodes(ctx, tx)
	if err != nil {
		return nil, err
	}
	_, affected, err := auth.ApplyRegistrationCodeMutation(current, input, now)
	if err != nil {
		return nil, err
	}
	for _, c := range affected {
		if input.Action == "delete" {
			_, err = tx.Exec(ctx, `DELETE FROM np_registration_codes WHERE id=$1`, c.ID)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO np_registration_codes (`+registrationCodeColumns+`,code_hash) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			 ON CONFLICT(id) DO UPDATE SET enabled=EXCLUDED.enabled,max_uses=EXCLUDED.max_uses,starts_at=EXCLUDED.starts_at,expires_at=EXCLUDED.expires_at,note=EXCLUDED.note,version=EXCLUDED.version,updated_at=EXCLUDED.updated_at`, c.ID, c.Code, c.Enabled, c.MaxUses, c.Used, c.StartsAt, c.ExpiresAt, c.Note, c.Version, c.CreatedAt, c.UpdatedAt, c.LastUsedAt, auth.RegistrationCodeHash(c.Code))
		}
		if err != nil {
			return nil, err
		}
	}
	if err = insertAuthAudit(ctx, tx, auth.UserMutation{Actor: actor}, "registration_codes."+input.Action, 0, map[string]any{"count": len(affected)}); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return affected, nil
}

func consumeRegistrationCode(ctx context.Context, tx pgx.Tx, hash string) error {
	if hash == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(914037)`); err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	command, err := tx.Exec(ctx, `UPDATE np_registration_codes SET used=used+1,version=version+1,updated_at=$2,last_used_at=$2
	 WHERE code_hash=$1 AND enabled AND (max_uses=0 OR used<max_uses) AND (starts_at IS NULL OR starts_at<=$2) AND (expires_at IS NULL OR expires_at>$2)`, hash, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return auth.ErrRegistrationCodeInvalid
	}
	return nil
}
