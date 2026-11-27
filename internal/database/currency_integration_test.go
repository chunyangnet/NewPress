package database

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/virtualcurrency"
)

func currencyPostgres(t *testing.T) (*CurrencyRepository, virtualcurrency.Settings, []int64) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	base, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("currency_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		base.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	config.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if _, e := base.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
		base.Close()
	})
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	users := NewUserRepository(pool)
	if err = users.EnsureRoles(ctx, auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("Isolated currency integration password")
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("currency-user-%d", i)
		user, e := users.CreateUser(ctx, auth.UserMutation{Actor: "integration"}, auth.UserInput{Username: name, Email: name + "@example.test", DisplayName: name, Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"subscriber"}}, hash)
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, user.ID)
	}
	store := NewCurrencyRepository(pool)
	cfg := virtualcurrency.DefaultSettings()
	cfg.Enabled = true
	cfg.AllowTransfers = true
	cfg.Precision = 2
	cfg.CheckInReward = 10
	cfg, err = store.UpdateSettings(ctx, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return store, cfg, ids
}
func currencyPGSeed(t *testing.T, s *CurrencyRepository, cfg virtualcurrency.Settings, actor, user, amount int64) {
	t.Helper()
	_, err := s.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: actor, UserID: user, Delta: amount, ExpectedVersion: cfg.Version, RequestID: fmt.Sprintf("seed-pg-user-%020d", user), Reason: "测试发放"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQLCurrencyAtomicityAndImmutableLedger(t *testing.T) {
	s, cfg, ids := currencyPostgres(t)
	currencyPGSeed(t, s, cfg, ids[0], ids[0], 1000)
	in := virtualcurrency.TransferInput{UserID: ids[0], RecipientID: ids[1], Amount: 125, ExpectedVersion: cfg.Version, RequestID: "pg-transfer-stable-request"}
	var first virtualcurrency.TransferResult
	var successes atomic.Int64
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			result, err := s.Transfer(t.Context(), in)
			if err != nil {
				t.Error(err)
			} else if !result.Replayed {
				successes.Add(1)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("non-replayed transfers", successes.Load())
	}
	first, err := s.Transfer(t.Context(), in)
	if err != nil || !first.Replayed || first.Sent.Balance != 875 || first.Received.Balance != 125 {
		t.Fatal(first, err)
	}
	wallet, err := s.Wallet(t.Context(), ids[0], 0, 30)
	if err != nil || wallet.Balance != 875 || len(wallet.Entries) != 2 || !wallet.Settings.PrecisionLocked || wallet.TransferredToday != 125 {
		t.Fatal(wallet, err)
	}
	other, _, err := s.Receipt(t.Context(), ids[1], in.RequestID)
	if err != nil || len(other) != 0 {
		t.Fatal("cross-account receipt", other, err)
	}
	in.Amount++
	if _, err = s.Transfer(t.Context(), in); !errors.Is(err, virtualcurrency.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE np_currency_entries SET delta=delta+1`, `DELETE FROM np_currency_entries`, `UPDATE np_currency_operations SET request_id=request_id`, `DELETE FROM np_currency_operations`} {
		if _, err = s.pool.Exec(t.Context(), query); err == nil {
			t.Fatal("ledger mutation accepted", query)
		}
	}
	if _, err = s.pool.Exec(t.Context(), `DELETE FROM np_users WHERE id=$1`, ids[0]); err == nil {
		t.Fatal("account deletion erased ledger")
	}
	changed := cfg
	changed.Precision = 3
	if _, err = s.UpdateSettings(t.Context(), cfg.Version, changed); !errors.Is(err, virtualcurrency.ErrPrecisionLocked) {
		t.Fatal(err)
	}
	changed = cfg
	changed.Enabled = false
	changed, err = s.UpdateSettings(t.Context(), cfg.Version, changed)
	if err != nil {
		t.Fatal(err)
	}
	in.Amount--
	if replay, err := s.Transfer(t.Context(), in); err != nil || !replay.Replayed {
		t.Fatal("replay rejected after configuration change", err)
	}
	changed.MaxBalance = math.MaxInt64
	changed, err = s.UpdateSettings(t.Context(), changed.Version, changed)
	if err != nil {
		t.Fatal(err)
	}
	currencyPGSeed(t, s, changed, ids[0], ids[2], math.MaxInt64)
	if _, err = s.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: ids[0], UserID: ids[2], Delta: 1, ExpectedVersion: changed.Version, RequestID: "pg-currency-overflow-request", Reason: "上溢检查"}); !errors.Is(err, virtualcurrency.ErrBalanceLimit) {
		t.Fatal(err)
	}
}

func TestPostgreSQLCurrencyLockOrderAndOverspend(t *testing.T) {
	s, cfg, ids := currencyPostgres(t)
	for _, id := range ids[:3] {
		currencyPGSeed(t, s, cfg, ids[0], id, 1000)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 90; i++ {
		wg.Go(func() {
			from, to := ids[i%3], ids[(i+1)%3]
			_, err := s.Transfer(ctx, virtualcurrency.TransferInput{UserID: from, RecipientID: to, Amount: 1, ExpectedVersion: cfg.Version, RequestID: fmt.Sprintf("cycle-transfer-%020d", i)})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, id := range ids[:3] {
		balance, err := s.Balance(ctx, id)
		if err != nil || balance != 1000 {
			t.Fatal(balance, err)
		}
	}
	currencyPGSeed(t, s, cfg, ids[0], ids[3], 100)
	var success atomic.Int64
	for i := 0; i < 35; i++ {
		wg.Go(func() {
			_, err := s.Transfer(ctx, virtualcurrency.TransferInput{UserID: ids[3], RecipientID: ids[4], Amount: 10, ExpectedVersion: cfg.Version, RequestID: fmt.Sprintf("overspend-transfer-%020d", i)})
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, virtualcurrency.ErrInsufficient) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if success.Load() != 10 {
		t.Fatal("overspending successes", success.Load())
	}
	if balance, err := s.Balance(ctx, ids[4]); err != nil || balance != 100 {
		t.Fatal(balance, err)
	}
}

func TestPostgreSQLCurrencyRewardLimitsAndReceipts(t *testing.T) {
	s, cfg, ids := currencyPostgres(t)
	cfg.CheckInEnabled = true
	cfg.DailyRewardLimit = 20
	cfg.DailyIssuanceLimit = 30
	var err error
	cfg, err = s.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var issued atomic.Int64
	for i := 0; i < 24; i++ {
		wg.Go(func() {
			r, e := s.Reward(t.Context(), virtualcurrency.RewardInput{UserID: ids[0], Kind: virtualcurrency.RewardCheckIn, ExpectedVersion: cfg.Version, RequestID: fmt.Sprintf("checkin-race-%020d", i), VerifiedEmail: true})
			if e != nil {
				t.Error(e)
			} else if !r.Replayed {
				issued.Add(1)
			}
		})
	}
	wg.Wait()
	if issued.Load() != 1 {
		t.Fatal("concurrent check-in rewards", issued.Load())
	}
	for i, id := range ids[1:] {
		wg.Go(func() {
			_, e := s.Reward(t.Context(), virtualcurrency.RewardInput{UserID: id, Kind: virtualcurrency.RewardCheckIn, ExpectedVersion: cfg.Version, RequestID: fmt.Sprintf("budget-race-%020d", i), VerifiedEmail: true})
			if e == nil {
				issued.Add(1)
			} else if !errors.Is(e, virtualcurrency.ErrRewardLimit) {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if issued.Load() != 3 {
		t.Fatal("global budget exceeded", issued.Load())
	}
	cfg.PublicationEnabled = true
	cfg.PublicationReward = 1
	cfg.PublicationMinLength = 1
	cfg.PublicationDailyLimit = 1
	cfg.DailyIssuanceLimit = 100
	cfg, err = s.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	in := virtualcurrency.RewardInput{UserID: ids[6], SourceID: 100, Kind: virtualcurrency.RewardPublication, OccurredAt: time.Now(), ContentLength: 1, VerifiedEmail: true}
	first, err := s.Reward(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.ContentLength = 0
	in.VerifiedEmail = false
	if replay, e := s.Reward(t.Context(), in); e != nil || !replay.Replayed || replay.Entry.ID != first.Entry.ID {
		t.Fatal("receipt changed with rule facts", replay, e)
	}
	in.UserID = ids[7]
	if _, err = s.Reward(t.Context(), in); !errors.Is(err, virtualcurrency.ErrIdempotencyConflict) {
		t.Fatal("reassignment rewarded twice", err)
	}
	in.UserID = ids[6]
	in.SourceID = 101
	in.ContentLength = 1
	in.VerifiedEmail = true
	if _, err = s.Reward(t.Context(), in); !errors.Is(err, virtualcurrency.ErrRewardLimit) {
		t.Fatal("publication count limit", err)
	}
	if err = s.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
}
