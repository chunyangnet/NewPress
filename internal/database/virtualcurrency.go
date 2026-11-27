package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/virtualcurrency"
)

type CurrencyRepository struct {
	pool *pgxpool.Pool
	key  string
}

func NewCurrencyRepository(pool *pgxpool.Pool) *CurrencyRepository {
	return &CurrencyRepository{pool: pool, key: virtualcurrency.DefaultCurrencyKey}
}

func (s *CurrencyRepository) currencyKey() string {
	if s.key == "" {
		return virtualcurrency.DefaultCurrencyKey
	}
	return s.key
}
func (s *CurrencyRepository) Ready(ctx context.Context) error { _, err := s.Settings(ctx); return err }

type currencyQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

const currencySettingsColumns = `version,enabled,name,symbol,precision,max_balance,allow_transfers,min_transfer,max_transfer,daily_transfer_limit,
 check_in_enabled,check_in_reward,check_in_interval_hours,registration_enabled,registration_reward,registration_since,
 publication_enabled,publication_reward,publication_since,publication_min_length,publication_daily_limit,require_verified_email,daily_reward_limit,daily_issuance_limit,
 EXISTS(SELECT 1 FROM np_currency_entries WHERE currency_key=np_currency_settings.currency_key),check_in_mode,check_in_timezone`

func scanCurrencySettings(row pgx.Row) (virtualcurrency.Settings, error) {
	var v virtualcurrency.Settings
	var registrationSince, publicationSince *time.Time
	err := row.Scan(&v.Version, &v.Enabled, &v.Name, &v.Symbol, &v.Precision, &v.MaxBalance, &v.AllowTransfers, &v.MinTransfer, &v.MaxTransfer, &v.DailyTransferLimit,
		&v.CheckInEnabled, &v.CheckInReward, &v.CheckInIntervalHours, &v.RegistrationEnabled, &v.RegistrationReward, &registrationSince,
		&v.PublicationEnabled, &v.PublicationReward, &publicationSince, &v.PublicationMinLength, &v.PublicationDailyLimit, &v.RequireVerifiedEmail, &v.DailyRewardLimit, &v.DailyIssuanceLimit, &v.PrecisionLocked, &v.CheckInMode, &v.CheckInTimezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, virtualcurrency.ErrStoreUnavailable
	}
	if err != nil {
		return v, err
	}
	if registrationSince != nil {
		v.RegistrationSince = *registrationSince
	}
	if publicationSince != nil {
		v.PublicationSince = *publicationSince
	}
	return virtualcurrency.NormalizeSettings(v)
}

func (s *CurrencyRepository) Settings(ctx context.Context) (virtualcurrency.Settings, error) {
	return scanCurrencySettings(s.pool.QueryRow(ctx, `SELECT `+currencySettingsColumns+` FROM np_currency_settings WHERE id=1 AND currency_key=$1`, s.currencyKey()))
}

func currencyNullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (s *CurrencyRepository) UpdateSettings(ctx context.Context, expected int64, value virtualcurrency.Settings) (virtualcurrency.Settings, error) {
	if _, err := virtualcurrency.NormalizeSettings(value); err != nil || expected < 1 {
		return virtualcurrency.Settings{}, virtualcurrency.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return virtualcurrency.Settings{}, err
	}
	defer tx.Rollback(ctx)
	current, err := scanCurrencySettings(tx.QueryRow(ctx, `SELECT `+currencySettingsColumns+` FROM np_currency_settings WHERE id=1 AND currency_key=$1 FOR UPDATE`, s.currencyKey()))
	if err != nil {
		return virtualcurrency.Settings{}, err
	}
	// This statement runs after obtaining the settings lock, so first issuance
	// cannot race a precision change even if the lock acquisition had to wait.
	var active, exceeds bool
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM np_currency_entries WHERE currency_key=$2),EXISTS(SELECT 1 FROM np_currency_balances WHERE currency_key=$2 AND balance>$1),clock_timestamp()`, value.MaxBalance, s.currencyKey()).Scan(&active, &exceeds, &now); err != nil {
		return virtualcurrency.Settings{}, err
	}
	value, err = virtualcurrency.PrepareSettings(current, value, expected, active, now)
	if err != nil {
		return virtualcurrency.Settings{}, err
	}
	if exceeds {
		return virtualcurrency.Settings{}, virtualcurrency.ErrBalanceLimit
	}
	_, err = tx.Exec(ctx, `UPDATE np_currency_settings SET version=$1,enabled=$2,name=$3,symbol=$4,precision=$5,max_balance=$6,allow_transfers=$7,min_transfer=$8,max_transfer=$9,daily_transfer_limit=$10,
  check_in_enabled=$11,check_in_reward=$12,check_in_interval_hours=$13,registration_enabled=$14,registration_reward=$15,registration_since=$16,
  publication_enabled=$17,publication_reward=$18,publication_since=$19,publication_min_length=$20,publication_daily_limit=$21,require_verified_email=$22,daily_reward_limit=$23,daily_issuance_limit=$24,check_in_mode=$26,check_in_timezone=$27,updated_at=clock_timestamp() WHERE id=1 AND currency_key=$25`,
		value.Version, value.Enabled, value.Name, value.Symbol, value.Precision, value.MaxBalance, value.AllowTransfers, value.MinTransfer, value.MaxTransfer, value.DailyTransferLimit,
		value.CheckInEnabled, value.CheckInReward, value.CheckInIntervalHours, value.RegistrationEnabled, value.RegistrationReward, currencyNullableTime(value.RegistrationSince),
		value.PublicationEnabled, value.PublicationReward, currencyNullableTime(value.PublicationSince), value.PublicationMinLength, value.PublicationDailyLimit, value.RequireVerifiedEmail, value.DailyRewardLimit, value.DailyIssuanceLimit, s.currencyKey(), value.CheckInMode, value.CheckInTimezone)
	if err != nil {
		return virtualcurrency.Settings{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return virtualcurrency.Settings{}, err
	}
	return value, nil
}

func currencyBalance(ctx context.Context, q currencyQuery, key string, userID int64) (int64, error) {
	var balance int64
	err := q.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM np_currency_balances WHERE user_id=$1 AND currency_key=$2),0)`, userID, key).Scan(&balance)
	return balance, err
}
func (s *CurrencyRepository) Balance(ctx context.Context, userID int64) (int64, error) {
	if userID < 1 {
		return 0, virtualcurrency.ErrInvalid
	}
	return currencyBalance(ctx, s.pool, s.currencyKey(), userID)
}

const currencyEntryColumns = `id,operation_id,user_id,COALESCE(peer_id,0),actor_id,source_id,delta,balance,kind,reason,created_at,purchase_amount`

func scanCurrencyEntry(row interface{ Scan(...any) error }) (virtualcurrency.Entry, error) {
	var e virtualcurrency.Entry
	err := row.Scan(&e.ID, &e.OperationID, &e.UserID, &e.PeerID, &e.ActorID, &e.SourceID, &e.Delta, &e.Balance, &e.Kind, &e.Reason, &e.CreatedAt, &e.PurchaseAmount)
	return e, err
}
func currencyHistory(ctx context.Context, q currencyQuery, key string, userID, before int64, limit int) ([]virtualcurrency.Entry, error) {
	rows, err := q.Query(ctx, `SELECT `+currencyEntryColumns+` FROM np_currency_entries WHERE user_id=$1 AND currency_key=$4 AND ($2=0 OR id<$2) ORDER BY id DESC LIMIT $3`, userID, before, limit, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []virtualcurrency.Entry{}
	for rows.Next() {
		e, err := scanCurrencyEntry(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
func (s *CurrencyRepository) History(ctx context.Context, userID, before int64, limit int) ([]virtualcurrency.Entry, error) {
	if userID < 1 || before < 0 || limit < 1 || limit > virtualcurrency.MaxHistoryPage {
		return nil, virtualcurrency.ErrInvalid
	}
	return currencyHistory(ctx, s.pool, s.currencyKey(), userID, before, limit)
}

func (s *CurrencyRepository) Wallet(ctx context.Context, userID, before int64, limit int) (virtualcurrency.Wallet, error) {
	if userID < 1 || before < 0 || limit < 1 || limit > virtualcurrency.MaxHistoryPage {
		return virtualcurrency.Wallet{}, virtualcurrency.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return virtualcurrency.Wallet{}, err
	}
	defer tx.Rollback(ctx)
	var v virtualcurrency.Wallet
	if v.Settings, err = scanCurrencySettings(tx.QueryRow(ctx, `SELECT `+currencySettingsColumns+` FROM np_currency_settings WHERE id=1 AND currency_key=$1`, s.currencyKey())); err != nil {
		return v, err
	}
	if v.Balance, err = currencyBalance(ctx, tx, s.currencyKey(), userID); err != nil {
		return v, err
	}
	if v.Entries, err = currencyHistory(ctx, tx, s.currencyKey(), userID, before, limit); err != nil {
		return v, err
	}
	var lastCheckIn *time.Time
	err = tx.QueryRow(ctx, `SELECT
  LEAST(COALESCE(SUM(-delta::numeric) FILTER (WHERE kind='transfer_out' AND created_at>now()-interval '24 hours'),0),9223372036854775807)::bigint,
  LEAST(COALESCE(SUM(delta::numeric) FILTER (WHERE kind IN ('reward_check_in','reward_registration','reward_publication') AND created_at>now()-interval '24 hours'),0),9223372036854775807)::bigint,
  MAX(created_at) FILTER (WHERE kind='reward_check_in') FROM np_currency_entries WHERE user_id=$1 AND currency_key=$2`, userID, s.currencyKey()).Scan(&v.TransferredToday, &v.RewardedToday, &lastCheckIn)
	if err != nil {
		return v, err
	}
	if lastCheckIn != nil {
		v.NextCheckInAt = v.Settings.CheckInRule().Next(*lastCheckIn)
	}
	if err = tx.Commit(ctx); err != nil {
		return v, err
	}
	return v, nil
}
