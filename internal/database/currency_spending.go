package database

import (
	"context"
	"newpress.local/newpress/internal/virtualcurrency"
	"strings"
)

var _ virtualcurrency.SpendingStore = (*CurrencyRepository)(nil)

func (s *CurrencyRepository) PurchaseHistory(ctx context.Context, user int64) ([]virtualcurrency.Entry, error) {
	if user < 1 {
		return nil, virtualcurrency.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT `+currencyEntryColumns+` FROM np_currency_entries WHERE currency_key=$1 AND user_id=$2 AND kind=$3 ORDER BY id DESC`, s.currencyKey(), user, virtualcurrency.PurchasePayment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []virtualcurrency.Entry{}
	for rows.Next() {
		e, err := scanCurrencyEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *CurrencyRepository) SpendReceipt(ctx context.Context, in virtualcurrency.SpendInput) (virtualcurrency.Entry, bool, error) {
	if !virtualcurrency.ValidSpend(in) {
		return virtualcurrency.Entry{}, false, virtualcurrency.ErrInvalid
	}
	entries, fp, err := s.Receipt(ctx, in.UserID, in.RequestID)
	if err != nil || len(entries) == 0 {
		return virtualcurrency.Entry{}, false, err
	}
	if fp != virtualcurrency.SpendFingerprint(in.UserID, in.Amount, in.Reason) {
		return virtualcurrency.Entry{}, false, virtualcurrency.ErrIdempotencyConflict
	}
	if len(entries) != 1 || !virtualcurrency.ValidateSpendReceipt(entries[0], in) {
		return virtualcurrency.Entry{}, false, virtualcurrency.ErrStoreUnavailable
	}
	return entries[0], true, nil
}

func (s *CurrencyRepository) Spend(ctx context.Context, in virtualcurrency.SpendInput) (virtualcurrency.Entry, bool, error) {
	return s.spend(ctx, in, false)
}
func (s *CurrencyRepository) CancelSpend(ctx context.Context, in virtualcurrency.SpendInput) (virtualcurrency.Entry, bool, error) {
	return s.spend(ctx, in, true)
}

func (s *CurrencyRepository) spend(ctx context.Context, in virtualcurrency.SpendInput, cancel bool) (virtualcurrency.Entry, bool, error) {
	if !virtualcurrency.ValidSpend(in) {
		return virtualcurrency.Entry{}, false, virtualcurrency.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return virtualcurrency.Entry{}, false, err
	}
	defer tx.Rollback(ctx)
	op, prior, err := reserveCurrencyOperation(ctx, tx, s.currencyKey(), in.UserID, in.RequestID, virtualcurrency.SpendFingerprint(in.UserID, in.Amount, in.Reason))
	if err != nil {
		return virtualcurrency.Entry{}, false, err
	}
	if prior != nil {
		if len(prior) != 1 || !virtualcurrency.ValidateSpendReceipt(prior[0], in) {
			return virtualcurrency.Entry{}, false, virtualcurrency.ErrStoreUnavailable
		}
		return prior[0], true, tx.Commit(ctx)
	}
	cfg, err := scanCurrencySettings(tx.QueryRow(ctx, `SELECT `+currencySettingsColumns+` FROM np_currency_settings WHERE id=1 AND currency_key=$1 FOR SHARE`, s.currencyKey()))
	if err != nil {
		return virtualcurrency.Entry{}, false, err
	}
	if !cancel && cfg.Version != in.ExpectedVersion {
		return virtualcurrency.Entry{}, false, virtualcurrency.ErrConflict
	}
	if !cancel && !cfg.Enabled {
		return virtualcurrency.Entry{}, false, virtualcurrency.ErrDisabled
	}
	balance, err := lockCurrencyBalance(ctx, tx, s.currencyKey(), in.UserID)
	if err != nil {
		return virtualcurrency.Entry{}, false, err
	}
	if !cancel && balance < in.Amount {
		return virtualcurrency.Entry{}, false, virtualcurrency.ErrInsufficient
	}
	delta, kind := -in.Amount, virtualcurrency.PurchasePayment
	if cancel {
		delta, kind = 0, virtualcurrency.PurchaseCanceled
	}
	now, err := currencyNow(ctx, tx)
	if err != nil {
		return virtualcurrency.Entry{}, false, err
	}
	e, err := scanCurrencyEntry(tx.QueryRow(ctx, `INSERT INTO np_currency_entries(operation_id,actor_id,user_id,delta,balance,kind,reason,created_at,currency_key,purchase_amount) VALUES($1,$2,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+currencyEntryColumns, op, in.UserID, delta, balance+delta, kind, strings.TrimSpace(in.Reason), now, s.currencyKey(), in.Amount))
	if err == nil {
		err = saveCurrencyBalance(ctx, tx, s.currencyKey(), in.UserID, e.Balance)
	}
	if err != nil {
		return virtualcurrency.Entry{}, false, err
	}
	return e, false, tx.Commit(ctx)
}
