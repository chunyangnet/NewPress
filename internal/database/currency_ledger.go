package database

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/virtualcurrency"
)

func reserveCurrencyOperation(ctx context.Context, tx pgx.Tx, key string, actor int64, requestID, fp string) (int64, []virtualcurrency.Entry, error) {
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO np_currency_operations(actor_id,request_id,fingerprint,currency_key) VALUES($1,$2,$3,$4) ON CONFLICT(currency_key,actor_id,request_id) DO NOTHING RETURNING id`, actor, requestID, fp, key).Scan(&id)
	if err == nil {
		return id, nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, err
	}
	var existingFP string
	if err = tx.QueryRow(ctx, `SELECT id,fingerprint FROM np_currency_operations WHERE actor_id=$1 AND request_id=$2 AND currency_key=$3`, actor, requestID, key).Scan(&id, &existingFP); err != nil {
		return 0, nil, err
	}
	if existingFP != fp {
		return 0, nil, virtualcurrency.ErrIdempotencyConflict
	}
	rows, err := tx.Query(ctx, `SELECT `+currencyEntryColumns+` FROM np_currency_entries WHERE operation_id=$1 AND currency_key=$2 ORDER BY id LIMIT 3`, id, key)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	entries := []virtualcurrency.Entry{}
	for rows.Next() {
		e, err := scanCurrencyEntry(rows)
		if err != nil {
			return 0, nil, err
		}
		entries = append(entries, e)
	}
	if err = rows.Err(); err != nil {
		return 0, nil, err
	}
	if len(entries) < 1 || len(entries) > 2 {
		return 0, nil, virtualcurrency.ErrStoreUnavailable
	}
	return id, entries, nil
}

func (s *CurrencyRepository) Receipt(ctx context.Context, actor int64, requestID string) ([]virtualcurrency.Entry, string, error) {
	if actor < 0 || !virtualcurrency.ValidRequestID(requestID) {
		return nil, "", virtualcurrency.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT `+currencyEntryColumns+`,(SELECT fingerprint FROM np_currency_operations WHERE actor_id=$1 AND request_id=$2 AND currency_key=$3) FROM np_currency_entries WHERE currency_key=$3 AND operation_id=(SELECT id FROM np_currency_operations WHERE actor_id=$1 AND request_id=$2 AND currency_key=$3) ORDER BY id LIMIT 3`, actor, requestID, s.currencyKey())
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var entries []virtualcurrency.Entry
	var fp string
	for rows.Next() {
		var e virtualcurrency.Entry
		if err = rows.Scan(&e.ID, &e.OperationID, &e.UserID, &e.PeerID, &e.ActorID, &e.SourceID, &e.Delta, &e.Balance, &e.Kind, &e.Reason, &e.CreatedAt, &e.PurchaseAmount, &fp); err != nil {
			return nil, "", err
		}
		entries = append(entries, e)
	}
	if len(entries) > 2 {
		return nil, "", virtualcurrency.ErrStoreUnavailable
	}
	return entries, fp, rows.Err()
}

func lockCurrencyBalance(ctx context.Context, tx pgx.Tx, key string, userID int64) (int64, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO np_currency_balances(user_id,balance,currency_key) VALUES($1,0,$2) ON CONFLICT(currency_key,user_id) DO NOTHING`, userID, key); err != nil {
		return 0, err
	}
	var balance int64
	err := tx.QueryRow(ctx, `SELECT balance FROM np_currency_balances WHERE user_id=$1 AND currency_key=$2 FOR UPDATE`, userID, key).Scan(&balance)
	return balance, err
}

func insertCurrencyEntry(ctx context.Context, tx pgx.Tx, key string, op, actor, user, peer, source, delta, balance int64, kind, reason string, now time.Time) (virtualcurrency.Entry, error) {
	return scanCurrencyEntry(tx.QueryRow(ctx, `INSERT INTO np_currency_entries(operation_id,actor_id,user_id,peer_id,source_id,delta,balance,kind,reason,created_at,currency_key) VALUES($1,$2,$3,NULLIF($4,0),$5,$6,$7,$8,$9,$10,$11) RETURNING `+currencyEntryColumns, op, actor, user, peer, source, delta, balance, kind, reason, now, key))
}
func saveCurrencyBalance(ctx context.Context, tx pgx.Tx, key string, user, balance int64) error {
	_, err := tx.Exec(ctx, `UPDATE np_currency_balances SET balance=$2,updated_at=clock_timestamp() WHERE user_id=$1 AND currency_key=$3`, user, balance, key)
	return err
}
func currencyNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

func (s *CurrencyRepository) Adjust(ctx context.Context, in virtualcurrency.Adjustment) (virtualcurrency.Entry, error) {
	if in.ActorID < 1 || in.UserID < 1 || in.Delta == 0 || in.Delta == math.MinInt64 || in.ExpectedVersion < 1 || !virtualcurrency.ValidRequestID(in.RequestID) || !virtualcurrency.ValidReason(in.Reason) {
		return virtualcurrency.Entry{}, virtualcurrency.ErrInvalid
	}
	kind := "admin_credit"
	if in.Delta < 0 {
		kind = "admin_debit"
	}
	return s.applyCurrencySingle(ctx, in.ActorID, in.UserID, in.Delta, in.ExpectedVersion, in.RequestID, strings.TrimSpace(in.Reason), kind, virtualcurrency.AdjustmentFingerprint(in.UserID, in.Delta, in.Reason), false)
}
func (s *CurrencyRepository) Credit(ctx context.Context, in virtualcurrency.CreditInput) (virtualcurrency.Entry, error) {
	if in.UserID < 1 || in.Amount < 1 || in.ExpectedVersion < 1 || !virtualcurrency.ValidRequestID(in.RequestID) || !virtualcurrency.ValidReason(in.Source) {
		return virtualcurrency.Entry{}, virtualcurrency.ErrInvalid
	}
	return s.applyCurrencySingle(ctx, 0, in.UserID, in.Amount, in.ExpectedVersion, in.RequestID, strings.TrimSpace(in.Source), "credit", virtualcurrency.CreditFingerprint(in.UserID, in.Amount, in.Source), true)
}

func (s *CurrencyRepository) applyCurrencySingle(ctx context.Context, actor, user, delta, version int64, requestID, reason, kind, fp string, requireEnabled bool) (virtualcurrency.Entry, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return virtualcurrency.Entry{}, err
	}
	defer tx.Rollback(ctx)
	op, prior, err := reserveCurrencyOperation(ctx, tx, s.currencyKey(), actor, requestID, fp)
	if err != nil {
		return virtualcurrency.Entry{}, err
	}
	if prior != nil {
		if len(prior) != 1 {
			return virtualcurrency.Entry{}, virtualcurrency.ErrStoreUnavailable
		}
		return prior[0], tx.Commit(ctx)
	}
	cfg, err := scanCurrencySettings(tx.QueryRow(ctx, `SELECT `+currencySettingsColumns+` FROM np_currency_settings WHERE id=1 AND currency_key=$1 FOR SHARE`, s.currencyKey()))
	if err != nil {
		return virtualcurrency.Entry{}, err
	}
	if version != cfg.Version {
		return virtualcurrency.Entry{}, virtualcurrency.ErrConflict
	}
	if requireEnabled && !cfg.Enabled {
		return virtualcurrency.Entry{}, virtualcurrency.ErrDisabled
	}
	current, err := lockCurrencyBalance(ctx, tx, s.currencyKey(), user)
	if err != nil {
		return virtualcurrency.Entry{}, err
	}
	if delta < 0 && current < -delta {
		return virtualcurrency.Entry{}, virtualcurrency.ErrInsufficient
	}
	if delta > 0 && current > cfg.MaxBalance-delta {
		return virtualcurrency.Entry{}, virtualcurrency.ErrBalanceLimit
	}
	now, err := currencyNow(ctx, tx)
	if err != nil {
		return virtualcurrency.Entry{}, err
	}
	entry, err := insertCurrencyEntry(ctx, tx, s.currencyKey(), op, actor, user, 0, 0, delta, current+delta, kind, reason, now)
	if err != nil {
		return virtualcurrency.Entry{}, err
	}
	if err = saveCurrencyBalance(ctx, tx, s.currencyKey(), user, entry.Balance); err != nil {
		return virtualcurrency.Entry{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return virtualcurrency.Entry{}, err
	}
	return entry, nil
}

func (s *CurrencyRepository) Transfer(ctx context.Context, in virtualcurrency.TransferInput) (virtualcurrency.TransferResult, error) {
	if in.UserID < 1 || in.RecipientID < 1 || in.UserID == in.RecipientID || in.Amount < 1 || in.ExpectedVersion < 1 || !virtualcurrency.ValidRequestID(in.RequestID) {
		return virtualcurrency.TransferResult{}, virtualcurrency.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return virtualcurrency.TransferResult{}, err
	}
	defer tx.Rollback(ctx)
	op, prior, err := reserveCurrencyOperation(ctx, tx, s.currencyKey(), in.UserID, in.RequestID, virtualcurrency.TransferFingerprint(in.RecipientID, in.Amount))
	if err != nil {
		return virtualcurrency.TransferResult{}, err
	}
	if prior != nil {
		if len(prior) != 2 || prior[0].Kind != "transfer_out" || prior[1].Kind != "transfer_in" {
			return virtualcurrency.TransferResult{}, virtualcurrency.ErrStoreUnavailable
		}
		return virtualcurrency.TransferResult{Sent: prior[0], Received: prior[1], Replayed: true}, tx.Commit(ctx)
	}
	cfg, err := scanCurrencySettings(tx.QueryRow(ctx, `SELECT `+currencySettingsColumns+` FROM np_currency_settings WHERE id=1 AND currency_key=$1 FOR SHARE`, s.currencyKey()))
	if err != nil {
		return virtualcurrency.TransferResult{}, err
	}
	if in.ExpectedVersion != cfg.Version {
		return virtualcurrency.TransferResult{}, virtualcurrency.ErrConflict
	}
	if !cfg.Enabled {
		return virtualcurrency.TransferResult{}, virtualcurrency.ErrDisabled
	}
	if !cfg.AllowTransfers {
		return virtualcurrency.TransferResult{}, virtualcurrency.ErrTransferDisabled
	}
	if in.Amount < cfg.MinTransfer || in.Amount > cfg.MaxTransfer {
		return virtualcurrency.TransferResult{}, virtualcurrency.ErrInvalid
	}
	// Both insertion and row locking follow the same total order, including
	// opposite-direction and three-account transfer cycles.
	first, second := min(in.UserID, in.RecipientID), max(in.UserID, in.RecipientID)
	firstBalance, err := lockCurrencyBalance(ctx, tx, s.currencyKey(), first)
	if err != nil {
		return virtualcurrency.TransferResult{}, err
	}
	secondBalance, err := lockCurrencyBalance(ctx, tx, s.currencyKey(), second)
	if err != nil {
		return virtualcurrency.TransferResult{}, err
	}
	from, to := firstBalance, secondBalance
	if in.UserID != first {
		from, to = secondBalance, firstBalance
	}
	if from < in.Amount {
		return virtualcurrency.TransferResult{}, virtualcurrency.ErrInsufficient
	}
	if to > cfg.MaxBalance-in.Amount {
		return virtualcurrency.TransferResult{}, virtualcurrency.ErrBalanceLimit
	}
	now, err := currencyNow(ctx, tx)
	if err != nil {
		return virtualcurrency.TransferResult{}, err
	}
	var within bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(-delta::numeric),0)+$3::numeric <= $1::numeric FROM np_currency_entries WHERE user_id=$2 AND currency_key=$5 AND kind='transfer_out' AND created_at>$4`, cfg.DailyTransferLimit, in.UserID, in.Amount, now.Add(-24*time.Hour), s.currencyKey()).Scan(&within)
	if err != nil {
		return virtualcurrency.TransferResult{}, err
	}
	if !within {
		return virtualcurrency.TransferResult{}, virtualcurrency.ErrRateLimit
	}
	sent, err := insertCurrencyEntry(ctx, tx, s.currencyKey(), op, in.UserID, in.UserID, in.RecipientID, 0, -in.Amount, from-in.Amount, "transfer_out", "", now)
	if err != nil {
		return virtualcurrency.TransferResult{}, err
	}
	received, err := insertCurrencyEntry(ctx, tx, s.currencyKey(), op, in.UserID, in.RecipientID, in.UserID, 0, in.Amount, to+in.Amount, "transfer_in", "", now)
	if err != nil {
		return virtualcurrency.TransferResult{}, err
	}
	if err = saveCurrencyBalance(ctx, tx, s.currencyKey(), in.UserID, sent.Balance); err != nil {
		return virtualcurrency.TransferResult{}, err
	}
	if err = saveCurrencyBalance(ctx, tx, s.currencyKey(), in.RecipientID, received.Balance); err != nil {
		return virtualcurrency.TransferResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return virtualcurrency.TransferResult{}, err
	}
	return virtualcurrency.TransferResult{Sent: sent, Received: received}, nil
}

func (s *CurrencyRepository) Reward(ctx context.Context, in virtualcurrency.RewardInput) (virtualcurrency.RewardResult, error) {
	actor, requestID, fp, err := virtualcurrency.RewardIdentity(in)
	if err != nil {
		return virtualcurrency.RewardResult{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return virtualcurrency.RewardResult{}, err
	}
	defer tx.Rollback(ctx)
	op, prior, err := reserveCurrencyOperation(ctx, tx, s.currencyKey(), actor, requestID, fp)
	if err != nil {
		return virtualcurrency.RewardResult{}, err
	}
	if prior != nil {
		if len(prior) != 1 {
			return virtualcurrency.RewardResult{}, virtualcurrency.ErrStoreUnavailable
		}
		return virtualcurrency.RewardResult{Entry: prior[0], Replayed: true}, tx.Commit(ctx)
	}
	// Rewards serialize on the settings row so the site-wide issuance budget
	// remains exact across accounts, processes, and reward sources.
	cfg, err := scanCurrencySettings(tx.QueryRow(ctx, `SELECT `+currencySettingsColumns+` FROM np_currency_settings WHERE id=1 AND currency_key=$1 FOR UPDATE`, s.currencyKey()))
	if err != nil {
		return virtualcurrency.RewardResult{}, err
	}
	now, err := currencyNow(ctx, tx)
	if err != nil {
		return virtualcurrency.RewardResult{}, err
	}
	amount, err := virtualcurrency.RewardAmount(cfg, in, now)
	if err != nil {
		return virtualcurrency.RewardResult{}, err
	}
	current, err := lockCurrencyBalance(ctx, tx, s.currencyKey(), in.UserID)
	if err != nil {
		return virtualcurrency.RewardResult{}, err
	}
	if in.Kind == virtualcurrency.RewardCheckIn {
		last, err := scanCurrencyEntry(tx.QueryRow(ctx, `SELECT `+currencyEntryColumns+` FROM np_currency_entries WHERE user_id=$1 AND currency_key=$2 AND kind='reward_check_in' ORDER BY created_at DESC,id DESC LIMIT 1`, in.UserID, s.currencyKey()))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return virtualcurrency.RewardResult{}, err
		}
		if err == nil && now.Before(cfg.CheckInRule().Next(last.CreatedAt)) {
			// Roll back the unused request reservation; a different request during
			// the same interval returns the existing receipt, never another credit.
			return virtualcurrency.RewardResult{Entry: last, Replayed: true}, nil
		}
	}
	if current > cfg.MaxBalance-amount {
		return virtualcurrency.RewardResult{}, virtualcurrency.ErrBalanceLimit
	}
	var userWithin, globalWithin bool
	var publications int64
	err = tx.QueryRow(ctx, `SELECT
  COALESCE(SUM(delta::numeric) FILTER(WHERE user_id=$1),0)+$2::numeric <= $3::numeric,
  COALESCE(SUM(delta::numeric),0)+$2::numeric <= $4::numeric,
  COUNT(*) FILTER(WHERE user_id=$1 AND kind='reward_publication')
  FROM np_currency_entries WHERE currency_key=$6 AND kind IN ('reward_check_in','reward_registration','reward_publication') AND created_at>$5`, in.UserID, amount, cfg.DailyRewardLimit, cfg.DailyIssuanceLimit, now.Add(-24*time.Hour), s.currencyKey()).Scan(&userWithin, &globalWithin, &publications)
	if err != nil {
		return virtualcurrency.RewardResult{}, err
	}
	if !userWithin || !globalWithin || in.Kind == virtualcurrency.RewardPublication && publications >= int64(cfg.PublicationDailyLimit) {
		return virtualcurrency.RewardResult{}, virtualcurrency.ErrRewardLimit
	}
	entry, err := insertCurrencyEntry(ctx, tx, s.currencyKey(), op, actor, in.UserID, 0, in.SourceID, amount, current+amount, in.Kind, virtualcurrency.RewardReason(in.Kind), now)
	if err != nil {
		return virtualcurrency.RewardResult{}, err
	}
	if err = saveCurrencyBalance(ctx, tx, s.currencyKey(), in.UserID, entry.Balance); err != nil {
		return virtualcurrency.RewardResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return virtualcurrency.RewardResult{}, err
	}
	return virtualcurrency.RewardResult{Entry: entry}, nil
}
