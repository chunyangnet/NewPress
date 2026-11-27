package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/messaging"
)

type MessagingRepository struct{ pool *pgxpool.Pool }

func NewMessagingRepository(pool *pgxpool.Pool) *MessagingRepository {
	return &MessagingRepository{pool: pool}
}

func scanMessage(row pgx.Row) (messaging.Message, error) {
	var m messaging.Message
	err := row.Scan(&m.ID, &m.SenderID, &m.RecipientID, &m.Content, &m.Nonce, &m.CreatedAt, &m.ReadAt)
	return m, err
}

const messageColumns = `id,sender_id,recipient_id,content,nonce,created_at,read_at`

func (s *MessagingRepository) Conversations(ctx context.Context, user, before int64) (messaging.Inbox, error) {
	result := messaging.Inbox{Items: []messaging.Conversation{}}
	if user <= 0 || before < 0 {
		return result, messaging.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM np_messages WHERE recipient_id=$1 AND read_at IS NULL`, user).Scan(&result.Unread); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `WITH peers AS (
		SELECT CASE WHEN sender_id=$1 THEN recipient_id ELSE sender_id END peer,
		max(id) last_id,count(*) FILTER (WHERE recipient_id=$1 AND read_at IS NULL) unread
		FROM np_messages WHERE sender_id=$1 OR recipient_id=$1 GROUP BY peer)
		SELECT p.peer,p.unread,m.id,m.sender_id,m.recipient_id,m.content,m.nonce,m.created_at,m.read_at
		FROM peers p JOIN np_messages m ON m.id=p.last_id WHERE ($2::bigint=0 OR m.id<$2) ORDER BY m.id DESC LIMIT 51`, user, before)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var c messaging.Conversation
		m := &c.Last
		if err := rows.Scan(&c.PeerID, &c.Unread, &m.ID, &m.SenderID, &m.RecipientID, &m.Content, &m.Nonce, &m.CreatedAt, &m.ReadAt); err != nil {
			return result, err
		}
		if len(result.Items) == messaging.PageSize {
			result.HasMore = true
		} else {
			result.Items = append(result.Items, c)
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
func (s *MessagingRepository) Messages(ctx context.Context, user, peer int64, query messaging.MessageQuery) (messaging.MessagePage, error) {
	result := messaging.MessagePage{Items: []messaging.Message{}}
	if !messaging.ValidQuery(user, peer, query) {
		return result, messaging.ErrInvalid
	}
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM np_messages WHERE sender_id=$1 AND recipient_id=$2 AND read_at IS NOT NULL`, user, peer).Scan(&result.ReadThrough); err != nil {
		return result, err
	}
	order := "DESC"
	if query.After > 0 {
		order = "ASC"
	}
	rows, err := s.pool.Query(ctx, `SELECT `+messageColumns+` FROM np_messages
		WHERE ((sender_id=$1 AND recipient_id=$2) OR (sender_id=$2 AND recipient_id=$1)) AND ($3::bigint=0 OR id<$3) AND ($4::bigint=0 OR id>$4)
		ORDER BY id `+order+` LIMIT 51`, user, peer, query.Before, query.After)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return result, err
		}
		if len(result.Items) == messaging.PageSize {
			result.HasMore = true
		} else {
			result.Items = append(result.Items, m)
		}
	}
	return result, rows.Err()
}
func (s *MessagingRepository) Send(ctx context.Context, input messaging.Input) (messaging.Message, error) {
	input, err := messaging.Normalize(input)
	if err != nil {
		return messaging.Message{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return messaging.Message{}, err
	}
	defer tx.Rollback(ctx)
	// Serializing per sender makes both nonce replay and the rolling send limit atomic.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('message-sender-' || $1::bigint::text,0))`, input.SenderID); err != nil {
		return messaging.Message{}, err
	}
	m, err := scanMessage(tx.QueryRow(ctx, `SELECT `+messageColumns+` FROM np_messages WHERE sender_id=$1 AND nonce=$2`, input.SenderID, input.Nonce))
	if err == nil {
		if m.RecipientID != input.RecipientID || m.Content != input.Content {
			return messaging.Message{}, messaging.ErrConflict
		}
		return m, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return messaging.Message{}, err
	}
	var recent int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM np_messages WHERE sender_id=$1 AND created_at > now()-interval '10 minutes'`, input.SenderID).Scan(&recent); err != nil {
		return messaging.Message{}, err
	}
	if recent >= messaging.SendLimit {
		return messaging.Message{}, messaging.ErrRateLimited
	}
	m, err = scanMessage(tx.QueryRow(ctx, `INSERT INTO np_messages(sender_id,recipient_id,content,nonce) VALUES($1,$2,$3,$4) RETURNING `+messageColumns, input.SenderID, input.RecipientID, input.Content, input.Nonce))
	if err != nil {
		return messaging.Message{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO np_message_notifications(id,user_id,peer_id,created_at) VALUES($1,$2,$3,$4)`, m.ID, m.RecipientID, m.SenderID, m.CreatedAt); err != nil {
		return messaging.Message{}, err
	}
	return m, tx.Commit(ctx)
}
func (s *MessagingRepository) Read(ctx context.Context, user, peer, through int64) error {
	if !messaging.ValidQuery(user, peer, messaging.MessageQuery{}) || through <= 0 {
		return messaging.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM np_messages WHERE id=$3 AND ((recipient_id=$1 AND sender_id=$2) OR (sender_id=$1 AND recipient_id=$2)))`, user, peer, through).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return messaging.ErrInvalid
	}
	if _, err = tx.Exec(ctx, `UPDATE np_messages SET read_at=GREATEST(now(),created_at) WHERE recipient_id=$1 AND sender_id=$2 AND id<=$3 AND read_at IS NULL`, user, peer, through); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE np_message_notifications SET read_at=GREATEST(now(),created_at) WHERE user_id=$1 AND peer_id=$2 AND id<=$3 AND read_at IS NULL`, user, peer, through); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *MessagingRepository) Notices(ctx context.Context, user int64, query messaging.NoticeQuery) (messaging.NoticePage, error) {
	result := messaging.NoticePage{Items: []messaging.Notice{}}
	if user <= 0 || query.Before < 0 {
		return result, messaging.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE read_at IS NULL),COALESCE(max(id),0) FROM np_message_notifications WHERE user_id=$1`, user).Scan(&result.Unread, &result.Through); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT id,user_id,peer_id,created_at,read_at FROM np_message_notifications WHERE user_id=$1 AND ($2::bigint=0 OR id<$2) AND (NOT $3::boolean OR read_at IS NULL) ORDER BY id DESC LIMIT 51`, user, query.Before, query.UnreadOnly)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var n messaging.Notice
		if err := rows.Scan(&n.ID, &n.UserID, &n.PeerID, &n.CreatedAt, &n.ReadAt); err != nil {
			return result, err
		}
		n.MessageID = n.ID
		if len(result.Items) == messaging.PageSize {
			result.HasMore = true
		} else {
			result.Items = append(result.Items, n)
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
func (s *MessagingRepository) UpdateNotices(ctx context.Context, user, through int64, action string) error {
	if user <= 0 || through <= 0 {
		return messaging.ErrInvalid
	}
	query := ""
	switch action {
	case "read":
		query = `UPDATE np_message_notifications SET read_at=COALESCE(read_at,GREATEST(now(),created_at)) WHERE user_id=$1 AND id=$2`
	case "read_all":
		query = `UPDATE np_message_notifications SET read_at=COALESCE(read_at,GREATEST(now(),created_at)) WHERE user_id=$1 AND id<=$2`
	case "clear":
		query = `DELETE FROM np_message_notifications WHERE user_id=$1 AND id<=$2`
	default:
		return messaging.ErrInvalid
	}
	_, err := s.pool.Exec(ctx, query, user, through)
	return err
}
