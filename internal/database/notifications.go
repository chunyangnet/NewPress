package database

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/notification"
	"sort"
	"time"
)

type NotificationRepository struct{ pool *pgxpool.Pool }

var _ notification.Store = (*NotificationRepository)(nil)

func NewNotificationRepository(pool *pgxpool.Pool) *NotificationRepository {
	return &NotificationRepository{pool}
}
func (s *NotificationRepository) Ready(ctx context.Context) error { return s.pool.Ping(ctx) }

const preferenceColumns = `user_id,version,reply,mention,publication,moderation,email,weekly,COALESCE(email_since,'0001-01-01T00:00:00Z'::timestamptz),COALESCE(weekly_since,'0001-01-01T00:00:00Z'::timestamptz),unsubscribe_key,updated_at`

func scanPreferences(row scanner) (notification.Preferences, error) {
	var p notification.Preferences
	err := row.Scan(&p.UserID, &p.Version, &p.Reply, &p.Mention, &p.Publication, &p.Moderation, &p.Email, &p.Weekly, &p.EmailSince, &p.WeeklySince, &p.UnsubscribeKey, &p.UpdatedAt)
	return p, err
}
func queryPreferences(ctx context.Context, q postQuerier, user int64) (notification.Preferences, error) {
	if user <= 0 {
		return notification.Preferences{}, notification.ErrInvalid
	}
	p, err := scanPreferences(q.QueryRow(ctx, `SELECT `+preferenceColumns+` FROM np_notification_preferences WHERE user_id=$1`, user))
	if errors.Is(err, pgx.ErrNoRows) {
		return notification.Defaults(user), nil
	}
	return p, err
}
func (s *NotificationRepository) Preferences(ctx context.Context, user int64) (notification.Preferences, error) {
	return queryPreferences(ctx, s.pool, user)
}
func (s *NotificationRepository) SavePreferences(ctx context.Context, input notification.Preferences, now time.Time) (notification.Preferences, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return notification.Preferences{}, err
	}
	defer tx.Rollback(ctx)
	// The lock also serializes first-time preferences where no row exists yet.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('np-notification-pref:' || $1::bigint::text,0))`, input.UserID); err != nil {
		return notification.Preferences{}, err
	}
	old, err := queryPreferences(ctx, tx, input.UserID)
	if err != nil {
		return notification.Preferences{}, err
	}
	p, err := notification.UpdatedPreferences(old, input, now)
	if err != nil {
		return p, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO np_notification_preferences(user_id,version,reply,mention,publication,moderation,email,weekly,email_since,weekly_since,unsubscribe_key,updated_at)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(user_id) DO UPDATE SET version=EXCLUDED.version,reply=EXCLUDED.reply,mention=EXCLUDED.mention,publication=EXCLUDED.publication,moderation=EXCLUDED.moderation,email=EXCLUDED.email,weekly=EXCLUDED.weekly,email_since=EXCLUDED.email_since,weekly_since=EXCLUDED.weekly_since,unsubscribe_key=EXCLUDED.unsubscribe_key,updated_at=EXCLUDED.updated_at`, p.UserID, p.Version, p.Reply, p.Mention, p.Publication, p.Moderation, p.Email, p.Weekly, nullableTime(p.EmailSince), nullableTime(p.WeeklySince), p.UnsubscribeKey, p.UpdatedAt)
	if err != nil {
		return notification.Preferences{}, err
	}
	if err = cancelDisabledNotificationMail(ctx, tx, p, now); err != nil {
		return notification.Preferences{}, err
	}
	return p, tx.Commit(ctx)
}
func (s *NotificationRepository) Unsubscribe(ctx context.Context, user int64, key string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('np-notification-pref:' || $1::bigint::text,0))`, user); err != nil {
		return err
	}
	p, err := queryPreferences(ctx, tx, user)
	if err != nil {
		return err
	}
	if p.UnsubscribeKey == "" || subtle.ConstantTimeCompare([]byte(key), []byte(p.UnsubscribeKey)) != 1 {
		return notification.ErrInvalid
	}
	if _, err = tx.Exec(ctx, `UPDATE np_notification_preferences SET email=false,weekly=false,version=version+1,updated_at=$2 WHERE user_id=$1 AND (email OR weekly)`, user, now); err != nil {
		return err
	}
	p.Email, p.Weekly = false, false
	if err = cancelDisabledNotificationMail(ctx, tx, p, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func cancelDisabledNotificationMail(ctx context.Context, tx pgx.Tx, p notification.Preferences, now time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE np_notification_mail SET state='cancelled',updated_at=$2 WHERE user_id=$1 AND state IN ('pending','failed') AND (
	(kind='weekly' AND NOT $3) OR (kind<>'weekly' AND (NOT $4 OR (event->>'due')::timestamptz<$9 OR
	(kind='reply' AND NOT $5) OR (kind='mention' AND NOT $6) OR (kind='publication' AND NOT $7) OR (kind='moderation' AND NOT $8))))`, p.UserID, now, p.Weekly, p.Email, p.Reply, p.Mention, p.Publication, p.Moderation, p.EmailSince)
	return err
}
func (s *NotificationRepository) Project(ctx context.Context, receipt string, deliveries []notification.Delivery, now time.Time) error {
	if err := notification.ValidateProjection(receipt, deliveries, now); err != nil {
		return err
	}
	deliveries = append([]notification.Delivery(nil), deliveries...)
	sort.SliceStable(deliveries, func(i, j int) bool { return deliveries[i].UserID < deliveries[j].UserID })
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `INSERT INTO np_notification_receipts(event_key,created_at) VALUES($1,$2) ON CONFLICT DO NOTHING`, receipt, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	seen := map[string]bool{}
	for _, d := range deliveries {
		key := notification.JobKey(receipt, d)
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('np-notification-pref:' || $1::bigint::text,0))`, d.UserID); err != nil {
			return err
		}
		p, err := queryPreferences(ctx, tx, d.UserID)
		if err != nil {
			return err
		}
		if !p.Allows(d.Kind) {
			continue
		}
		raw, err := json.Marshal(d.Event)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO np_notifications(user_id,kind,event,created_at) VALUES($1,$2,$3,$4)`, d.UserID, d.Kind, raw, now); err != nil {
			return err
		}
		if notification.WantsEmail(p, d) {
			if err = insertMailJob(ctx, tx, notification.NewJob(key, d, now)); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
func insertMailJob(ctx context.Context, tx pgx.Tx, j notification.Job) error {
	raw, err := json.Marshal(j.Event)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO np_notification_mail(key,user_id,kind,event,state,available_at,created_at,updated_at) VALUES($1,$2,$3,$4,'pending',$5,$6,$7) ON CONFLICT DO NOTHING`, j.Key, j.UserID, j.Kind, raw, j.AvailableAt, j.CreatedAt, j.UpdatedAt)
	return err
}
func (s *NotificationRepository) Notices(ctx context.Context, user int64, query notification.Query) (notification.Page, error) {
	if user <= 0 || query.Before < 0 {
		return notification.Page{}, notification.ErrInvalid
	}
	p := notification.Page{Items: []notification.Notice{}}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(id),0),count(*) FILTER(WHERE read_at IS NULL) FROM np_notifications WHERE user_id=$1 AND cleared_at IS NULL`, user).Scan(&p.Through, &p.Unread); err != nil {
		return p, err
	}
	rows, err := tx.Query(ctx, `SELECT id,user_id,kind,event,created_at,read_at FROM np_notifications WHERE user_id=$1 AND cleared_at IS NULL AND ($2::bigint=0 OR id<$2) AND (NOT $3 OR read_at IS NULL) AND ($4::text='' OR kind=$4) ORDER BY id DESC LIMIT 51`, user, query.Before, query.UnreadOnly, query.Kind)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var n notification.Notice
		var raw []byte
		if err = rows.Scan(&n.ID, &n.UserID, &n.Kind, &raw, &n.CreatedAt, &n.ReadAt); err != nil {
			rows.Close()
			return p, err
		}
		if err = json.Unmarshal(raw, &n.Event); err != nil {
			rows.Close()
			return p, err
		}
		p.Items = append(p.Items, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	p.HasMore = len(p.Items) > 50
	if p.HasMore {
		p.Items = p.Items[:50]
	}
	return p, tx.Commit(ctx)
}
func (s *NotificationRepository) UpdateNotices(ctx context.Context, user, through int64, action string, now time.Time) error {
	if user <= 0 || through <= 0 || action != "read" && action != "read_all" && action != "clear" {
		return notification.ErrInvalid
	}
	column := `read_at=COALESCE(read_at,$3)`
	if action == "clear" {
		column = `cleared_at=$3`
	}
	_, err := s.pool.Exec(ctx, `UPDATE np_notifications SET `+column+` WHERE user_id=$1 AND id<=$2 AND ($4::text<>'read' OR id=$2)`, user, through, now, action)
	return err
}
func (s *NotificationRepository) Subscribers(ctx context.Context, after int64, limit int) ([]notification.Preferences, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT `+preferenceColumns+` FROM np_notification_preferences WHERE weekly AND user_id>$1 ORDER BY user_id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []notification.Preferences{}
	for rows.Next() {
		p, err := scanPreferences(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}
func (s *NotificationRepository) QueueWeekly(ctx context.Context, user int64, end, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	p, err := queryPreferences(ctx, tx, user)
	if err != nil {
		return err
	}
	if !p.Weekly || !p.WeeklySince.Before(end) {
		return nil
	}
	j := notification.NewJob(notification.WeeklyKey(user, end), notification.Delivery{UserID: user, Kind: notification.Weekly, Event: activity.Event{At: end.AddDate(0, 0, -7), Due: end}}, now)
	if err = insertMailJob(ctx, tx, j); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const mailColumns = `key,user_id,kind,event,state,attempts,available_at,lease_until,created_at,updated_at`

func scanMail(row scanner) (notification.Job, error) {
	var j notification.Job
	var raw []byte
	err := row.Scan(&j.Key, &j.UserID, &j.Kind, &raw, &j.State, &j.Attempts, &j.AvailableAt, &j.LeaseUntil, &j.CreatedAt, &j.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(raw, &j.Event)
	}
	return j, err
}
func (s *NotificationRepository) ClaimMail(ctx context.Context, now time.Time) (notification.Job, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return notification.Job{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE np_notification_mail SET state='uncertain',lease_until=NULL,updated_at=$1 WHERE state='sending' AND lease_until<=$1`, now); err != nil {
		return notification.Job{}, false, err
	}
	j, err := scanMail(tx.QueryRow(ctx, `SELECT `+mailColumns+` FROM np_notification_mail WHERE state='pending' AND available_at<=$1 ORDER BY available_at,key FOR UPDATE SKIP LOCKED LIMIT 1`, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return notification.Job{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return j, false, err
	}
	lease := now.Add(time.Minute)
	j.State = "sending"
	j.Attempts++
	j.LeaseUntil = &lease
	j.UpdatedAt = now
	if _, err = tx.Exec(ctx, `UPDATE np_notification_mail SET state='sending',attempts=$2,lease_until=$3,updated_at=$4 WHERE key=$1`, j.Key, j.Attempts, lease, now); err != nil {
		return notification.Job{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return notification.Job{}, false, err
	}
	return j, true, nil
}
func (s *NotificationRepository) FinishMail(ctx context.Context, job notification.Job, state string, now time.Time) error {
	j, err := notification.Finished(job, state, now)
	if err != nil {
		return err
	}
	command, err := s.pool.Exec(ctx, `UPDATE np_notification_mail SET state=$2,available_at=$3,lease_until=NULL,updated_at=$4 WHERE key=$1 AND state='sending' AND attempts=$5 AND lease_until=$6 AND updated_at=$7 AND lease_until>$4 AND updated_at<=$4`, j.Key, j.State, j.AvailableAt, j.UpdatedAt, j.Attempts, job.LeaseUntil, job.UpdatedAt)
	if err == nil && command.RowsAffected() == 0 {
		return notification.ErrConflict
	}
	return err
}
func (s *NotificationRepository) MailStatus(ctx context.Context, user int64) (notification.MailStatus, error) {
	var status notification.MailStatus
	err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state IN ('pending','sending')),count(*) FILTER(WHERE state='failed'),count(*) FILTER(WHERE state='uncertain') FROM np_notification_mail WHERE user_id=$1`, user).Scan(&status.Pending, &status.Failed, &status.Uncertain)
	return status, err
}
func (s *NotificationRepository) RetryFailed(ctx context.Context, user int64, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE np_notification_mail SET state='pending',attempts=0,available_at=$2,updated_at=$2 WHERE user_id=$1 AND state='failed'`, user, now)
	return err
}
