package database

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"newpress.local/newpress/internal/virtualcurrency"
	"newpress.local/newpress/migrations"
)

func TestPostgreSQLCurrencyCatalogIsolationAndConcurrentCreation(t *testing.T) {
	main, mainCfg, ids := currencyPostgres(t)
	catalog := NewCurrencyCatalog(main.pool)
	in := virtualcurrency.CreateCurrency{Key: "gold", Name: "金币", Symbol: "枚", Precision: 2, ActorID: ids[0], RequestID: "pg-catalog-stable-create"}
	var fresh atomic.Int64
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			// Distinct catalog instances exercise database locking, not a Go mutex.
			_, replayed, err := NewCurrencyCatalog(main.pool).Create(t.Context(), in)
			if err != nil {
				t.Error(err)
			} else if !replayed {
				fresh.Add(1)
			}
		})
	}
	wg.Wait()
	if fresh.Load() != 1 {
		t.Fatal("creation count", fresh.Load())
	}
	changed := in
	changed.Name = "另一个金币"
	if _, _, err := catalog.Create(t.Context(), changed); !errors.Is(err, virtualcurrency.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	changed = in
	changed.RequestID = "pg-catalog-conflicting-key"
	if _, _, err := catalog.Create(t.Context(), changed); !errors.Is(err, virtualcurrency.ErrCurrencyExists) {
		t.Fatal(err)
	}
	goldStore, err := catalog.Store(t.Context(), "gold")
	if err != nil {
		t.Fatal(err)
	}
	gold := goldStore.(*CurrencyRepository)
	goldCfg, err := gold.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if goldCfg.Enabled || goldCfg.PrecisionLocked || goldCfg.Precision != 2 {
		t.Fatal(goldCfg)
	}
	goldCfg.Enabled, goldCfg.AllowTransfers, goldCfg.CheckInEnabled = true, true, true
	goldCfg.MaxTransfer, goldCfg.DailyTransferLimit = 10, 10
	goldCfg.DailyRewardLimit, goldCfg.DailyIssuanceLimit = 10, 10
	goldCfg, err = gold.UpdateSettings(t.Context(), 1, goldCfg)
	if err != nil {
		t.Fatal(err)
	}
	mainCfg.CheckInEnabled = true
	mainCfg, err = main.UpdateSettings(t.Context(), mainCfg.Version, mainCfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		store  *CurrencyRepository
		cfg    virtualcurrency.Settings
		amount int64
	}{{main, mainCfg, 500}, {gold, goldCfg, 800}} {
		currencyPGSeed(t, tc.store, tc.cfg, ids[0], ids[0], tc.amount)
		_, err = tc.store.Transfer(t.Context(), virtualcurrency.TransferInput{UserID: ids[0], RecipientID: ids[1], Amount: 10, ExpectedVersion: tc.cfg.Version, RequestID: "pg-same-transfer-across-coins"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = tc.store.Reward(t.Context(), virtualcurrency.RewardInput{UserID: ids[0], Kind: virtualcurrency.RewardCheckIn, ExpectedVersion: tc.cfg.Version, RequestID: "pg-same-checkin-across-coins", VerifiedEmail: true})
		if err != nil {
			t.Fatal(err)
		}
		wallet, err := tc.store.Wallet(t.Context(), ids[0], 0, 30)
		if err != nil || wallet.Balance != tc.amount || len(wallet.Entries) != 3 || wallet.TransferredToday != 10 || wallet.RewardedToday != 10 {
			t.Fatal(wallet, err)
		}
	}
	if _, err = gold.Transfer(t.Context(), virtualcurrency.TransferInput{UserID: ids[0], RecipientID: ids[1], Amount: 1, ExpectedVersion: goldCfg.Version, RequestID: "pg-gold-daily-limit-check"}); !errors.Is(err, virtualcurrency.ErrRateLimit) {
		t.Fatal(err)
	}
	if _, err = main.Transfer(t.Context(), virtualcurrency.TransferInput{UserID: ids[0], RecipientID: ids[1], Amount: 1, ExpectedVersion: mainCfg.Version, RequestID: "pg-main-still-can-transfer"}); err != nil {
		t.Fatal(err)
	}
	if _, err = gold.Reward(t.Context(), virtualcurrency.RewardInput{UserID: ids[2], Kind: virtualcurrency.RewardCheckIn, ExpectedVersion: goldCfg.Version, RequestID: "pg-gold-global-budget-check", VerifiedEmail: true}); !errors.Is(err, virtualcurrency.ErrRewardLimit) {
		t.Fatal(err)
	}
	if _, err = main.Reward(t.Context(), virtualcurrency.RewardInput{UserID: ids[2], Kind: virtualcurrency.RewardCheckIn, ExpectedVersion: mainCfg.Version, RequestID: "pg-main-global-budget-check", VerifiedEmail: true}); err != nil {
		t.Fatal(err)
	}
	mainReceipt, _, err := main.Receipt(t.Context(), ids[0], "pg-same-transfer-across-coins")
	if err != nil {
		t.Fatal(err)
	}
	goldReceipt, _, err := gold.Receipt(t.Context(), ids[0], "pg-same-transfer-across-coins")
	if err != nil || len(mainReceipt) != 2 || len(goldReceipt) != 2 || mainReceipt[0].OperationID == goldReceipt[0].OperationID {
		t.Fatal(mainReceipt, goldReceipt, err)
	}
	_, err = main.pool.Exec(t.Context(), `INSERT INTO np_currency_entries(operation_id,actor_id,user_id,source_id,delta,balance,kind,reason,currency_key) VALUES($1,$2,$3,0,1,1,'admin_credit','foreign operation','gold')`, mainReceipt[0].OperationID, ids[0], ids[7])
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "np_currency_entry_scoped_operation" {
		t.Fatal("cross-currency operation FK", err)
	}
	if _, err = catalog.Store(t.Context(), "missing"); !errors.Is(err, virtualcurrency.ErrCurrencyNotFound) {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE np_currency_catalog SET key=key`, `DELETE FROM np_currency_catalog`, `TRUNCATE np_currency_catalog CASCADE`} {
		if _, err = main.pool.Exec(t.Context(), query); err == nil {
			t.Fatal("catalog mutation accepted", query)
		}
	}
	var created atomic.Int64
	for i := range 45 {
		wg.Go(func() {
			input := in
			input.Key = fmt.Sprintf("coin-%d", i)
			input.RequestID = fmt.Sprintf("pg-create-extra-coin-%d", i)
			_, _, err := catalog.Create(t.Context(), input)
			if err == nil {
				created.Add(1)
			} else if !errors.Is(err, virtualcurrency.ErrCurrencyLimit) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	items, err := catalog.List(t.Context())
	if err != nil || len(items) != virtualcurrency.MaxCurrencies || created.Load() != virtualcurrency.MaxCurrencies-2 {
		t.Fatal(len(items), created.Load(), err)
	}
	if err = catalog.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := catalog.Create(t.Context(), in); err != nil || !replayed {
		t.Fatal("full catalog lost receipt", err)
	}
}

func TestPostgreSQLCurrencyCatalogUpgradePreservesLegacyRows(t *testing.T) {
	store, _, _ := currencyPostgres(t)
	tx, err := store.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	name := pgx.Identifier{fmt.Sprintf("currency_upgrade_%d", time.Now().UnixNano())}.Sanitize()
	if _, err = tx.Exec(t.Context(), "CREATE SCHEMA "+name); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), "SET LOCAL search_path TO "+name); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `CREATE TABLE np_users(id bigint PRIMARY KEY); INSERT INTO np_users(id) VALUES(1),(2)`); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"0029_virtual_currency.sql", "0030_currency_rewards_and_integrity.sql"} {
		body, err := migrations.Files.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(t.Context(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	fp := virtualcurrency.AdjustmentFingerprint(1, 12345, "旧账本")
	if _, err = tx.Exec(t.Context(), `UPDATE np_currency_settings SET name='旧积分',precision=2,version=7; INSERT INTO np_currency_balances(user_id,balance) VALUES(1,12345)`); err != nil {
		t.Fatal(err)
	}
	var op, id int64
	if err = tx.QueryRow(t.Context(), `INSERT INTO np_currency_operations(actor_id,request_id,fingerprint) VALUES(1,'legacy-currency-operation',$1) RETURNING id`, fp).Scan(&op); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(t.Context(), `INSERT INTO np_currency_entries(operation_id,actor_id,user_id,delta,balance,kind,reason) VALUES($1,1,1,12345,12345,'admin_credit','旧账本') RETURNING id`, op).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var oldEntry, oldOp string
	if err = tx.QueryRow(t.Context(), `SELECT row_to_json(e)::text FROM np_currency_entries e`).Scan(&oldEntry); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(t.Context(), `SELECT row_to_json(o)::text FROM np_currency_operations o`).Scan(&oldOp); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.Files.ReadFile("0031_currency_catalog.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), string(body)); err != nil {
		t.Fatal(err)
	}
	var unchanged bool
	if err = tx.QueryRow(t.Context(), `SELECT (to_jsonb(e)-'currency_key')=$1::jsonb AND e.currency_key='main' AND e.id=$2 AND e.operation_id=$3 FROM np_currency_entries e`, oldEntry, id, op).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("legacy entry changed", err)
	}
	if err = tx.QueryRow(t.Context(), `SELECT (to_jsonb(o)-'currency_key')=$1::jsonb AND o.currency_key='main' FROM np_currency_operations o`, oldOp).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("legacy receipt changed", err)
	}
	// This fixture ends at 0031; later settings columns are intentionally absent.
	var cfg virtualcurrency.Settings
	err = tx.QueryRow(t.Context(), `SELECT name,version,precision,EXISTS(SELECT 1 FROM np_currency_entries WHERE currency_key='main') FROM np_currency_settings WHERE currency_key='main'`).Scan(&cfg.Name, &cfg.Version, &cfg.Precision, &cfg.PrecisionLocked)
	if err != nil || cfg.Name != "旧积分" || cfg.Version != 7 || cfg.Precision != 2 || !cfg.PrecisionLocked {
		t.Fatal(cfg, err)
	}
	if balance, err := currencyBalance(t.Context(), tx, "main", 1); err != nil || balance != 12345 {
		t.Fatal(balance, err)
	}
}
