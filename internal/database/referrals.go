package database

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/auth"
)

func (r *UserRepository) GenerateReferral(ctx context.Context, id int64, p auth.ReferralPolicy, now time.Time) (auth.ReferralData, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return auth.ReferralData{}, err
	}
	defer tx.Rollback(ctx)
	u, err := queryAuthUserByID(ctx, tx, id, true)
	if err != nil {
		return auth.ReferralData{}, err
	}
	users := []auth.User{u}
	if err = hydrateUserRoles(ctx, tx, users); err != nil {
		return auth.ReferralData{}, err
	}
	d, err := auth.GenerateReferral(users[0], p, now)
	if err != nil {
		return d, err
	}
	if err = saveReferral(ctx, tx, id, d); err != nil {
		return d, err
	}
	return d, tx.Commit(ctx)
}
func saveReferral(ctx context.Context, tx pgx.Tx, id int64, d auth.ReferralData) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE np_users SET referral=$2 WHERE id=$1`, id, b)
	return err
}
func (r *UserRepository) SetReferralCode(ctx context.Context, id int64, code string, enabled bool) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	u, err := queryAuthUserByID(ctx, tx, id, true)
	if err != nil {
		return err
	}
	found := false
	for i := range u.Referral.Codes {
		if u.Referral.Codes[i].ID == code {
			u.Referral.Codes[i].Enabled = enabled
			u.Referral.Codes[i].Version++
			u.Referral.Codes[i].UpdatedAt = time.Now().UTC()
			found = true
		}
	}
	if !found {
		return auth.ErrUserNotFound
	}
	if err = saveReferral(ctx, tx, id, u.Referral); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *UserRepository) ReferralUsers(ctx context.Context, id int64) ([]auth.User, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+userColumns+` FROM np_users u WHERE referral->>'inviter_id'=$1 ORDER BY id`, strconv.FormatInt(id, 10))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []auth.User{}
	for rows.Next() {
		u, err := scanAuthUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func consumeReferral(ctx context.Context, tx pgx.Tx, id int64, input auth.UserInput) error {
	if input.ReferralHash == "" {
		return nil
	}
	// Serialize attribution with generation/disable through the owner's row lock.
	rows, err := tx.Query(ctx, `SELECT `+userColumns+` FROM np_users u WHERE jsonb_array_length(COALESCE(referral->'codes','[]'::jsonb))>0 ORDER BY id`)
	if err != nil {
		return err
	}
	var owner auth.User
	for rows.Next() {
		u, e := scanAuthUser(rows)
		if e != nil {
			rows.Close()
			return e
		}
		for _, c := range u.Referral.Codes {
			if auth.RegistrationCodeHash(c.Code) == input.ReferralHash {
				owner = u
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if owner.ID == 0 || owner.ID == id {
		return &auth.UserValidationError{Fields: map[string]string{"invitation_code": "邀请码不存在，请检查分享链接"}}
	}
	owner, err = queryAuthUserByID(ctx, tx, owner.ID, true)
	if err != nil {
		return err
	}
	d, fact, err := auth.ReferralAttribution(owner, input.ReferralHash, input.ReferralPolicy, time.Now().UTC())
	if err != nil {
		return err
	}
	if err = saveReferral(ctx, tx, owner.ID, d); err != nil {
		return err
	}
	return saveReferral(ctx, tx, id, fact)
}
