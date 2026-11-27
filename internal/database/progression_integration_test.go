package database

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/progression"
)

func TestPostgreSQLProgressionPurchaseAndCancellation(t *testing.T) {
	coin, c, ids := currencyPostgres(t)
	store := NewProgressionStore(coin.pool)
	cfg, err := store.Settings(t.Context())
	if err != nil || !reflect.DeepEqual(cfg, progression.DefaultSettings()) {
		t.Fatal("default settings", cfg, err)
	}
	cfg.Experience.Enabled = true
	cfg.Experience.RequireVerifiedEmail = false
	cfg.CheckIn.Enabled = true
	cfg.Levels.Enabled = true
	cfg.Membership.Enabled = true
	cfg.Membership.Plans[0].PurchaseEnabled = true
	cfg.Membership.Plans[0].Price = "1.25"
	cfg, err = store.UpdateSettings(t.Context(), 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	currencyPGSeed(t, coin, c, ids[0], ids[1], 10000)
	service := &progression.Service{Store: store, Users: NewUserRepository(coin.pool), Currencies: NewCurrencyCatalog(coin.pool)}
	in := progression.PurchaseInput{UserID: ids[1], ExpectedVersion: cfg.Version, Plan: "member", Mode: "grant", RequestID: "postgres-membership-purchase"}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			other := &progression.Service{Store: NewProgressionStore(coin.pool), Users: service.Users, Currencies: service.Currencies}
			o, err := other.Purchase(t.Context(), in)
			if err != nil || o.Status != "completed" {
				t.Error(o, err)
			}
		})
	}
	wg.Wait()
	v, err := store.Snapshot(t.Context(), ids[1], 0)
	if err != nil || v.Account.Membership.Version != 1 || !v.MembershipActive {
		t.Fatal(v, err)
	}
	if b, _ := coin.Balance(t.Context(), ids[1]); b != 9875 {
		t.Fatal("duplicate debit", b)
	}
	r, err := service.CheckIn(t.Context(), ids[1], cfg.Version, "pg-member-experience-checkin")
	if err != nil || r.Entry.Delta != 12 {
		t.Fatal(r, err)
	}
	in.Mode = "renew"
	in.MemberVersion = 1
	in.RequestID = "postgres-membership-race"
	o, err := store.BeginPurchase(t.Context(), in, progression.CurrencyQuote{Key: "main", Symbol: c.Symbol, Version: c.Version, Precision: c.Precision, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		wg.Go(func() {
			if _, err := service.Purchase(t.Context(), in); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if _, err := service.Cancel(t.Context(), ids[1], in.RequestID); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	final, found, err := store.ExistingOrder(t.Context(), in)
	if err != nil || !found || final.Status == "pending" {
		t.Fatal(final, err)
	}
	payment, found, err := coin.SpendReceipt(t.Context(), o.Payment())
	if err != nil || !found || payment.ID != final.PaymentEntry {
		t.Fatal(payment, err)
	}
	if err = service.ValidatePayments(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE np_progress_entries SET data=data`, `DELETE FROM np_progress_receipts`, `TRUNCATE np_progress_entries CASCADE`, `UPDATE np_progress_orders SET data=data`, `DELETE FROM np_progress_orders`} {
		if _, err = coin.pool.Exec(t.Context(), query); err == nil {
			t.Fatal("immutable records modified", query)
		}
	}
}
func TestPostgreSQLProgressionRecoveryWithSingleConnection(t *testing.T) {
	coin, c, ids := currencyPostgres(t)
	config := coin.pool.Config().Copy()
	config.MaxConns = 1
	config.MinConns = 0
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	store := NewProgressionStore(pool)
	cfg := progression.DefaultSettings()
	cfg.Membership.Enabled = true
	cfg.Membership.Plans[0].PurchaseEnabled = true
	cfg.Membership.Plans[0].Price = "2.50"
	cfg, err = store.UpdateSettings(ctx, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	currencyPGSeed(t, coin, c, ids[0], ids[1], 1000)
	service := &progression.Service{Store: store, Users: NewUserRepository(pool), Currencies: NewCurrencyCatalog(pool)}
	in := progression.PurchaseInput{UserID: ids[1], ExpectedVersion: cfg.Version, Plan: "member", Mode: "grant", RequestID: "pg-recovery-membership-one"}
	order, err := store.BeginPurchase(ctx, in, progression.CurrencyQuote{Key: "main", Symbol: c.Symbol, Version: c.Version, Precision: c.Precision, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, done, err := service.Recover(ctx, ids[1]); err != nil || done {
		t.Fatal(done, err)
	}
	if _, _, err = coin.Spend(ctx, order.Payment()); err != nil {
		t.Fatal(err)
	}
	// Simulate the process stopping after currency commit, before membership commit.
	service.Store = NewProgressionStore(pool)
	done, settled, err := service.Recover(ctx, ids[1])
	if err != nil || !settled || done.Status != "completed" {
		t.Fatal(done, settled, err)
	}
	if _, err = service.Purchase(ctx, in); err != nil {
		t.Fatal(err)
	}
	if b, _ := coin.Balance(ctx, ids[1]); b != 750 {
		t.Fatal(b)
	}
	cfg.Membership.Plans = cfg.Membership.Plans[1:]
	if _, err = store.UpdateSettings(ctx, cfg.Version, cfg); !errors.Is(err, progression.ErrUsedPlan) {
		t.Fatal(err)
	}
}
