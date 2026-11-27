package database

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/progression"
	"time"
)

type progressionBackend struct{ pool *pgxpool.Pool }
type progressionTx struct {
	ctx   context.Context
	tx    pgx.Tx
	write bool
}

func NewProgressionStore(pool *pgxpool.Pool) *progression.Store {
	return progression.NewStore(&progressionBackend{pool: pool})
}
func (b *progressionBackend) Ready(ctx context.Context) error {
	return b.Read(ctx, func(t progression.Transaction) error { _, err := t.Settings(); return err })
}
func (b *progressionBackend) Read(ctx context.Context, fn func(progression.Transaction) error) error {
	return b.run(ctx, false, fn)
}
func (b *progressionBackend) Write(ctx context.Context, fn func(progression.Transaction) error) error {
	return b.run(ctx, true, fn)
}
func (b *progressionBackend) run(ctx context.Context, write bool, fn func(progression.Transaction) error) error {
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if write {
		options = pgx.TxOptions{}
	}
	tx, err := b.pool.BeginTx(ctx, options)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if write {
		var id int
		if err = tx.QueryRow(ctx, `SELECT id FROM np_progress_settings WHERE id=1 FOR UPDATE`).Scan(&id); err != nil {
			return err
		}
	}
	if err = fn(&progressionTx{ctx: ctx, tx: tx, write: write}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (t *progressionTx) Settings() (progression.Settings, error) {
	var s progression.Settings
	var data []byte
	err := t.tx.QueryRow(t.ctx, `SELECT data FROM np_progress_settings WHERE id=1`).Scan(&data)
	if err == nil {
		err = json.Unmarshal(data, &s)
	}
	if err == nil {
		err = progression.ValidateSettings(s)
	}
	return progression.CloneSettings(s), err
}
func (t *progressionTx) SetSettings(s progression.Settings) error {
	if !t.write {
		return progression.ErrUnavailable
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tag, err := t.tx.Exec(t.ctx, `UPDATE np_progress_settings SET data=$1 WHERE id=1`, data)
	if err == nil && tag.RowsAffected() != 1 {
		return progression.ErrUnavailable
	}
	return err
}
func (t *progressionTx) Account(id int64) (progression.Account, error) {
	a := progression.Account{UserID: id}
	if id < 1 {
		return a, progression.ErrInvalid
	}
	var data []byte
	err := t.tx.QueryRow(t.ctx, `SELECT experience,membership,COALESCE(pending_request,'') FROM np_progress_accounts WHERE user_id=$1`, id).Scan(&a.Experience, &data, &a.PendingRequest)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &a.Membership)
	}
	return a, err
}
func (t *progressionTx) PutAccount(a progression.Account) error {
	if !t.write {
		return progression.ErrUnavailable
	}
	data, err := json.Marshal(a.Membership)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO np_progress_accounts(user_id,experience,membership,pending_request) VALUES($1,$2,$3,NULLIF($4,'')) ON CONFLICT(user_id) DO UPDATE SET experience=EXCLUDED.experience,membership=EXCLUDED.membership,pending_request=EXCLUDED.pending_request`, a.UserID, a.Experience, data, a.PendingRequest)
	return err
}
func (t *progressionTx) Entry(id int64) (progression.Entry, error) {
	var e progression.Entry
	var data []byte
	err := t.tx.QueryRow(t.ctx, `SELECT data FROM np_progress_entries WHERE id=$1`, id).Scan(&data)
	if err == nil {
		err = json.Unmarshal(data, &e)
	}
	return e, err
}
func (t *progressionTx) Append(e progression.Entry) (progression.Entry, error) {
	if !t.write {
		return e, progression.ErrUnavailable
	}
	err := t.tx.QueryRow(t.ctx, `SELECT nextval(pg_get_serial_sequence('np_progress_entries','id'))`).Scan(&e.ID)
	if err != nil {
		return e, err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO np_progress_entries(id,user_id,actor_id,kind,created_at,order_request,data) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7)`, e.ID, e.UserID, e.ActorID, e.Kind, e.CreatedAt, e.OrderRequest, data)
	return e, err
}
func (t *progressionTx) entries(query string, args ...any) ([]progression.Entry, error) {
	rows, err := t.tx.Query(t.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []progression.Entry{}
	for rows.Next() {
		var data []byte
		var e progression.Entry
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
func (t *progressionTx) History(user, before int64, limit int) ([]progression.Entry, error) {
	return t.entries(`SELECT data FROM np_progress_entries WHERE user_id=$1 AND ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT $3`, user, before, limit)
}
func (t *progressionTx) RewardsSince(user int64, since time.Time) ([]progression.Entry, error) {
	return t.entries(`SELECT data FROM np_progress_entries WHERE user_id=$1 AND created_at>$2 AND kind IN ('reward_check_in','reward_registration','reward_publication') ORDER BY id`, user, since)
}
func (t *progressionTx) Receipt(actor int64, request string) (progression.Receipt, bool, error) {
	r := progression.Receipt{ActorID: actor, RequestID: request}
	err := t.tx.QueryRow(t.ctx, `SELECT fingerprint,entry_id FROM np_progress_receipts WHERE actor_id=$1 AND request_id=$2`, actor, request).Scan(&r.Fingerprint, &r.EntryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}
func (t *progressionTx) PutReceipt(r progression.Receipt) error {
	if !t.write {
		return progression.ErrUnavailable
	}
	_, err := t.tx.Exec(t.ctx, `INSERT INTO np_progress_receipts(actor_id,request_id,fingerprint,entry_id) VALUES($1,$2,$3,$4)`, r.ActorID, r.RequestID, r.Fingerprint, r.EntryID)
	return err
}
func (t *progressionTx) Order(user int64, request string) (progression.Order, bool, error) {
	var o progression.Order
	var data []byte
	err := t.tx.QueryRow(t.ctx, `SELECT data FROM np_progress_orders WHERE user_id=$1 AND request_id=$2`, user, request).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, false, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &o)
	}
	return o, err == nil, err
}
func (t *progressionTx) PutOrder(o progression.Order) error {
	if !t.write {
		return progression.ErrUnavailable
	}
	data, err := json.Marshal(o)
	if err != nil {
		return err
	}
	if o.Status == "pending" {
		_, err = t.tx.Exec(t.ctx, `INSERT INTO np_progress_orders(user_id,request_id,status,currency_key,data) VALUES($1,$2,$3,$4,$5)`, o.UserID, o.RequestID, o.Status, o.Currency, data)
	} else {
		result, e := t.tx.Exec(t.ctx, `UPDATE np_progress_orders SET status=$3,payment_entry=$4,entry_id=NULLIF($5,0),data=$6 WHERE user_id=$1 AND request_id=$2 AND status='pending'`, o.UserID, o.RequestID, o.Status, o.PaymentEntry, o.EntryID, data)
		err = e
		if err == nil && result.RowsAffected() != 1 {
			err = progression.ErrUnavailable
		}
	}
	return err
}
func (t *progressionTx) Orders() ([]progression.Order, error) {
	rows, err := t.tx.Query(t.ctx, `SELECT data FROM np_progress_orders`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := []progression.Order{}
	for rows.Next() {
		var data []byte
		var o progression.Order
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &o); err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}
func (t *progressionTx) MaximumExperience() (int64, error) {
	var n int64
	err := t.tx.QueryRow(t.ctx, `SELECT COALESCE(max(experience),0) FROM np_progress_accounts`).Scan(&n)
	return n, err
}
func (t *progressionTx) PlanUsed(plan string) (bool, error) {
	var found bool
	err := t.tx.QueryRow(t.ctx, `SELECT EXISTS(SELECT 1 FROM np_progress_entries WHERE data->'membership'->>'plan'=$1) OR EXISTS(SELECT 1 FROM np_progress_orders WHERE data->>'plan'=$1)`, plan).Scan(&found)
	return found, err
}
