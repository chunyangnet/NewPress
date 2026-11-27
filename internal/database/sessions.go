package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/auth"
)

type SessionRepository struct {
	pool *pgxpool.Pool
}

var _ auth.SessionStore = (*SessionRepository)(nil)

func NewSessionRepository(pool *pgxpool.Pool) *SessionRepository {
	return &SessionRepository{pool: pool}
}

const sessionColumns = `id,COALESCE(user_id,0),username,display_name,role_name,credential_digest,csrf_token,created_at,last_seen_at,expires_at,ip_address,user_agent`

func (s *SessionRepository) Ready(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `SELECT `+sessionColumns+` FROM np_sessions LIMIT 0`)
	return err
}

func scanSession(row pgx.Row) (auth.SessionRecord, error) {
	var r auth.SessionRecord
	err := row.Scan(&r.ID, &r.UserID, &r.Username, &r.DisplayName, &r.RoleName, &r.CredentialDigest,
		&r.CSRFToken, &r.CreatedAt, &r.LastSeenAt, &r.ExpiresAt, &r.Metadata.IPAddress, &r.Metadata.UserAgent)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, auth.ErrSessionNotFound
	}
	if err != nil {
		return r, err
	}
	return r, r.Validate()
}

func (s *SessionRepository) Get(ctx context.Context, id string) (auth.SessionRecord, error) {
	return scanSession(s.pool.QueryRow(ctx, `SELECT `+sessionColumns+` FROM np_sessions WHERE id=$1`, id))
}

func (s *SessionRepository) beginMutation(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	// Serialize capacity decisions across connections and future replicas. This
	// is a transaction lock: cancellation/rollback cannot leak ownership.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-sessions',0))`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (s *SessionRepository) Create(ctx context.Context, r auth.SessionRecord, p auth.SessionPolicy) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if !r.Live(p.Now, p.IdleTTL) {
		return auth.ErrSessionNotFound
	}
	tx, err := s.beginMutation(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM np_sessions WHERE expires_at<=$1 OR last_seen_at<=$2`, p.Now, p.Now.Add(-p.IdleTTL)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM np_sessions WHERE id IN (
		SELECT id FROM np_sessions WHERE identity_key=$1 ORDER BY last_seen_at DESC,id DESC OFFSET $2
	)`, r.Identity(), p.MaxPerIdentity-1); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM np_sessions WHERE id IN (
		SELECT id FROM np_sessions ORDER BY last_seen_at DESC,id DESC OFFSET $1
	)`, p.MaxActive-1); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO np_sessions
		(id,user_id,identity_key,username,display_name,role_name,credential_digest,csrf_token,created_at,last_seen_at,expires_at,ip_address,user_agent)
		VALUES ($1,NULLIF($2::bigint,0),$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		r.ID, r.UserID, r.Identity(), r.Username, r.DisplayName, r.RoleName, r.CredentialDigest, r.CSRFToken,
		r.CreatedAt, r.LastSeenAt, r.ExpiresAt, r.Metadata.IPAddress, r.Metadata.UserAgent); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *SessionRepository) Touch(ctx context.Context, id string, now time.Time, idleTTL time.Duration) error {
	// There is no positive authentication cache and no UPSERT here. A deleted
	// session is never recreated by a request that was already validating it.
	updated, err := s.pool.Exec(ctx, `UPDATE np_sessions SET last_seen_at=GREATEST(last_seen_at,$2)
		WHERE id=$1 AND expires_at>$2 AND last_seen_at>$3 AND last_seen_at<=$4`,
		id, now, now.Add(-idleTTL), now.Add(-auth.SessionTouchInterval(idleTTL)))
	if err != nil {
		return err
	}
	if updated.RowsAffected() > 0 {
		return nil
	}
	var live bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM np_sessions WHERE id=$1 AND expires_at>$2 AND last_seen_at>$3)`,
		id, now, now.Add(-idleTTL)).Scan(&live); err != nil {
		return err
	}
	if !live {
		return auth.ErrSessionNotFound
	}
	return nil
}

func (s *SessionRepository) List(ctx context.Context, now time.Time, idleTTL time.Duration) ([]auth.SessionRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sessionColumns+` FROM np_sessions WHERE expires_at>$1 AND last_seen_at>$2 ORDER BY last_seen_at DESC,id`,
		now, now.Add(-idleTTL))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]auth.SessionRecord, 0)
	for rows.Next() {
		r, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

func (s *SessionRepository) Delete(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM np_sessions WHERE id=$1`, id)
	return err
}

func (s *SessionRepository) DeleteUser(ctx context.Context, userID int64, username, exceptID string, now time.Time, idleTTL time.Duration) (int, error) {
	if userID <= 0 && username == "" {
		return 0, errors.New("session identity is required")
	}
	tx, err := s.beginMutation(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	identity := (auth.SessionRecord{UserID: userID, Username: username}).Identity()
	var count int
	if err := tx.QueryRow(ctx, `WITH removed AS (
		DELETE FROM np_sessions WHERE identity_key=$1 AND id<>$2 RETURNING expires_at,last_seen_at
	) SELECT count(*) FROM removed WHERE expires_at>$3 AND last_seen_at>$4`,
		identity, exceptID, now, now.Add(-idleTTL)).Scan(&count); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}
