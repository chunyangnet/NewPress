package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/virtualcurrency"
)

type CurrencyCatalog struct{ pool *pgxpool.Pool }

func NewCurrencyCatalog(pool *pgxpool.Pool) *CurrencyCatalog {
	return &CurrencyCatalog{pool: pool}
}

func (c *CurrencyCatalog) Ready(ctx context.Context) error {
	items, err := c.List(ctx)
	if err != nil {
		return err
	}
	if len(items) < 1 || len(items) > virtualcurrency.MaxCurrencies || items[0].Key != virtualcurrency.DefaultCurrencyKey {
		return virtualcurrency.ErrStoreUnavailable
	}
	return nil
}

func (c *CurrencyCatalog) Store(ctx context.Context, key string) (virtualcurrency.Store, error) {
	if !virtualcurrency.ValidCurrencyKey(key) {
		return nil, virtualcurrency.ErrInvalid
	}
	var exists bool
	if err := c.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM np_currency_catalog WHERE key=$1)`, key).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, virtualcurrency.ErrCurrencyNotFound
	}
	return &CurrencyRepository{pool: c.pool, key: key}, nil
}

func (c *CurrencyCatalog) List(ctx context.Context) ([]virtualcurrency.Currency, error) {
	rows, err := c.pool.Query(ctx, `SELECT key,created_at FROM np_currency_catalog ORDER BY (key='main') DESC,created_at,key LIMIT $1`, virtualcurrency.MaxCurrencies+1)
	if err != nil {
		return nil, err
	}
	items := []virtualcurrency.Currency{}
	for rows.Next() {
		var item virtualcurrency.Currency
		if err = rows.Scan(&item.Key, &item.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(items) < 1 || len(items) > virtualcurrency.MaxCurrencies || items[0].Key != virtualcurrency.DefaultCurrencyKey {
		return nil, virtualcurrency.ErrStoreUnavailable
	}
	// Release the catalog cursor before using the pool again, including with a
	// one-connection pool. A missing settings row is corruption, never defaults.
	for i := range items {
		store := &CurrencyRepository{pool: c.pool, key: items[i].Key}
		if items[i].Settings, err = store.Settings(ctx); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (c *CurrencyCatalog) Create(ctx context.Context, in virtualcurrency.CreateCurrency) (virtualcurrency.Currency, bool, error) {
	cfg, err := virtualcurrency.NewCurrencySettings(in)
	if err != nil {
		return virtualcurrency.Currency{}, false, err
	}
	fp := virtualcurrency.CreationFingerprint(in, cfg)
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return virtualcurrency.Currency{}, false, err
	}
	defer tx.Rollback(ctx)
	// Creation is rare. Serialize the namespace limit, uniqueness and receipt
	// across processes; ordinary ledger transactions keep their per-coin locks.
	if _, err = tx.Exec(ctx, `LOCK TABLE np_currency_catalog IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return virtualcurrency.Currency{}, false, err
	}
	var item virtualcurrency.Currency
	var previousFP string
	err = tx.QueryRow(ctx, `SELECT key,fingerprint,created_at FROM np_currency_catalog WHERE actor_id=$1 AND request_id=$2`, in.ActorID, in.RequestID).Scan(&item.Key, &previousFP, &item.CreatedAt)
	if err == nil {
		if !virtualcurrency.ValidCreationFingerprint(previousFP) || previousFP != fp {
			return virtualcurrency.Currency{}, false, virtualcurrency.ErrIdempotencyConflict
		}
		item.Settings, err = scanCurrencySettings(tx.QueryRow(ctx, `SELECT `+currencySettingsColumns+` FROM np_currency_settings WHERE id=1 AND currency_key=$1`, item.Key))
		if err != nil {
			return virtualcurrency.Currency{}, false, err
		}
		return item, true, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return virtualcurrency.Currency{}, false, err
	}
	var count int
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT COUNT(*),COALESCE(bool_or(key=$1),false) FROM np_currency_catalog`, in.Key).Scan(&count, &exists); err != nil {
		return virtualcurrency.Currency{}, false, err
	}
	if exists {
		return virtualcurrency.Currency{}, false, virtualcurrency.ErrCurrencyExists
	}
	if count >= virtualcurrency.MaxCurrencies {
		return virtualcurrency.Currency{}, false, virtualcurrency.ErrCurrencyLimit
	}
	item.Key, item.Settings = in.Key, cfg
	if err = tx.QueryRow(ctx, `INSERT INTO np_currency_catalog(key,actor_id,request_id,fingerprint) VALUES($1,$2,$3,$4) RETURNING created_at`, in.Key, in.ActorID, in.RequestID, fp).Scan(&item.CreatedAt); err != nil {
		return virtualcurrency.Currency{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO np_currency_settings(id,currency_key,version,enabled,name,symbol,precision,max_balance,allow_transfers,min_transfer,max_transfer,daily_transfer_limit,
	check_in_enabled,check_in_reward,check_in_interval_hours,registration_enabled,registration_reward,publication_enabled,publication_reward,publication_min_length,publication_daily_limit,require_verified_email,daily_reward_limit,daily_issuance_limit)
	VALUES(1,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
		in.Key, cfg.Version, cfg.Enabled, cfg.Name, cfg.Symbol, cfg.Precision, cfg.MaxBalance, cfg.AllowTransfers, cfg.MinTransfer, cfg.MaxTransfer, cfg.DailyTransferLimit,
		cfg.CheckInEnabled, cfg.CheckInReward, cfg.CheckInIntervalHours, cfg.RegistrationEnabled, cfg.RegistrationReward, cfg.PublicationEnabled, cfg.PublicationReward, cfg.PublicationMinLength, cfg.PublicationDailyLimit, cfg.RequireVerifiedEmail, cfg.DailyRewardLimit, cfg.DailyIssuanceLimit)
	if err != nil {
		return virtualcurrency.Currency{}, false, err
	}
	return item, false, tx.Commit(ctx)
}
