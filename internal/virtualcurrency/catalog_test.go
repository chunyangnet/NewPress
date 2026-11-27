package virtualcurrency

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func createTestCurrency(t *testing.T, catalog Catalog, key string) Currency {
	t.Helper()
	item, replayed, err := catalog.Create(t.Context(), CreateCurrency{Key: key, Name: "金币", Symbol: "枚", Precision: 2, ActorID: 1, RequestID: "create-currency-" + key})
	if err != nil || replayed || item.Key != key || item.Settings.Enabled || item.Settings.PrecisionLocked {
		t.Fatalf("create: %+v, replayed %v, %v", item, replayed, err)
	}
	return item
}

func TestFileCatalogPreservesMainAndIsolatesLedgers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "currency.json")
	main, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultSettings()
	cfg.Enabled, cfg.AllowTransfers, cfg.CheckInEnabled = true, true, true
	cfg, err = main.UpdateSettings(t.Context(), 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	seed := Adjustment{ActorID: 1, UserID: 1, Delta: 1000, ExpectedVersion: cfg.Version, RequestID: "same-seed-request-across-coins", Reason: "初始发放"}
	old, err := main.Adjust(t.Context(), seed)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	c, err := NewFileCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	item := createTestCurrency(t, c, "gold")
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("creating catalog rewrote the old main ledger")
	}
	mainStore, _ := c.Store(t.Context(), DefaultCurrencyKey)
	gold, _ := c.Store(t.Context(), item.Key)
	goldCfg := item.Settings
	goldCfg.Enabled, goldCfg.AllowTransfers, goldCfg.CheckInEnabled = true, true, true
	goldCfg.Precision, goldCfg.CheckInReward = 3, 5
	goldCfg, err = gold.UpdateSettings(t.Context(), goldCfg.Version, goldCfg)
	if err != nil {
		t.Fatal("main activity locked another coin's precision", err)
	}
	if receipt, _, err := gold.Receipt(t.Context(), 1, seed.RequestID); err != nil || len(receipt) != 0 {
		t.Fatal("main receipt escaped namespace", receipt, err)
	}
	seed.Delta, seed.ExpectedVersion = 600, goldCfg.Version
	if _, err = gold.Adjust(t.Context(), seed); err != nil {
		t.Fatal("same request id should be independent across coins", err)
	}
	for _, tc := range []struct {
		store            Store
		cfg              Settings
		amount, expected int64
	}{{mainStore, cfg, 10, 1090}, {gold, goldCfg, 25, 580}} {
		in := TransferInput{UserID: 1, RecipientID: 2, Amount: tc.amount, ExpectedVersion: tc.cfg.Version, RequestID: "same-transfer-request-across-coins"}
		for range 2 {
			if _, err = tc.store.Transfer(t.Context(), in); err != nil {
				t.Fatal(err)
			}
		}
		for range 2 {
			if _, err = tc.store.Reward(t.Context(), RewardInput{UserID: 1, Kind: RewardCheckIn, ExpectedVersion: tc.cfg.Version, VerifiedEmail: true, RequestID: "same-check-in-request-across-coins"}); err != nil {
				t.Fatal(err)
			}
		}
		wallet, err := tc.store.Wallet(t.Context(), 1, 0, 30)
		if err != nil || wallet.Balance != tc.expected || wallet.TransferredToday != tc.amount || wallet.RewardedToday != tc.cfg.CheckInReward || len(wallet.Entries) != 3 {
			t.Fatalf("wallet isolation: %+v %v", wallet, err)
		}
	}
	c, err = NewFileCatalog(dir)
	if err != nil || c.Ready(t.Context()) != nil {
		t.Fatal("reload", err)
	}
	mainStore, _ = c.Store(t.Context(), DefaultCurrencyKey)
	receipt, _, err := mainStore.Receipt(t.Context(), 1, seed.RequestID)
	if err != nil || len(receipt) != 1 || receipt[0] != old {
		t.Fatal("old receipt changed", receipt, err)
	}
	if _, err = c.Store(t.Context(), "missing"); !errors.Is(err, ErrCurrencyNotFound) {
		t.Fatal("unknown coin fell back to main", err)
	}
}

func TestCurrencyCatalogCreationReplayAndLimit(t *testing.T) {
	c := NewMemoryCatalog(nil)
	in := CreateCurrency{Key: "gold", Name: " 金币 ", Symbol: "枚", Precision: 2, ActorID: 1, RequestID: "stable-create-currency-request"}
	var first atomic.Int64
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			_, replayed, err := c.Create(t.Context(), in)
			if err != nil {
				t.Error(err)
			} else if !replayed {
				first.Add(1)
			}
		})
	}
	wg.Wait()
	if first.Load() != 1 {
		t.Fatal("created more than once", first.Load())
	}
	changed := in
	changed.Name = "另一个名称"
	if _, _, err := c.Create(t.Context(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	changed = in
	changed.RequestID = "another-create-currency-request"
	if _, _, err := c.Create(t.Context(), changed); !errors.Is(err, ErrCurrencyExists) {
		t.Fatal(err)
	}
	var created atomic.Int64
	for i := range 48 {
		wg.Go(func() {
			input := in
			input.Key, input.RequestID = fmt.Sprintf("coin-%d", i), fmt.Sprintf("create-currency-number-%d", i)
			_, _, err := c.Create(t.Context(), input)
			if err == nil {
				created.Add(1)
			} else if !errors.Is(err, ErrCurrencyLimit) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	items, err := c.List(t.Context())
	if err != nil || len(items) != MaxCurrencies || created.Load() != MaxCurrencies-2 {
		t.Fatal(len(items), created.Load(), err)
	}
	if _, replayed, err := c.Create(t.Context(), in); err != nil || !replayed {
		t.Fatal("receipt unavailable at limit", err)
	}
}

func TestCurrencyCatalogRejectsInvalidKeys(t *testing.T) {
	for _, key := range []string{"", "../gold", "gold/coin", "Gold", "main ", "0gold", "gold-", "金", "a12345678901234567890123456789012", "x\\y", "gold:1"} {
		if ValidCurrencyKey(key) {
			t.Fatal("accepted", key)
		}
		c := NewMemoryCatalog(nil)
		if _, err := c.Store(t.Context(), key); !errors.Is(err, ErrInvalid) {
			t.Fatal(key, err)
		}
	}
	for _, key := range []string{"a", "gold", "gold-1", "a1234567890123456789012345678901", "con"} {
		if !ValidCurrencyKey(key) {
			t.Fatal("rejected", key)
		}
	}
}

func TestFileCatalogRejectsMissingLedgerAndExternalChange(t *testing.T) {
	for _, missing := range []string{"currency.json", filepath.Join("currencies", "currency-gold.json")} {
		t.Run(missing, func(t *testing.T) {
			dir := t.TempDir()
			c, err := NewFileCatalog(dir)
			if err != nil {
				t.Fatal(err)
			}
			createTestCurrency(t, c, "gold")
			if err = os.Remove(filepath.Join(dir, missing)); err != nil {
				t.Fatal(err)
			}
			if err = c.Ready(t.Context()); err == nil {
				t.Fatal("missing ledger reported ready")
			}
			if _, err = NewFileCatalog(dir); err == nil {
				t.Fatal("missing ledger was reset to zero")
			}
		})
	}
	dir := t.TempDir()
	c, _ := NewFileCatalog(dir)
	createTestCurrency(t, c, "gold")
	path := filepath.Join(dir, "currency-catalog.json")
	body, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store(t.Context(), "gold"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatal("external catalog replacement accepted", err)
	}
	if _, _, err := c.Create(t.Context(), CreateCurrency{Key: "silver", Name: "银币", Symbol: "枚", ActorID: 1, RequestID: "create-silver-after-tamper"}); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatal(err)
	}
}

func TestFileCatalogAdoptsOnlyEmptyMatchingOrphan(t *testing.T) {
	for _, funded := range []bool{false, true} {
		t.Run(fmt.Sprint(funded), func(t *testing.T) {
			c, err := NewFileCatalog(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			in := CreateCurrency{Key: "gold", Name: "金币", Symbol: "枚", Precision: 2, ActorID: 1, RequestID: "orphan-creation-request"}
			cfg, _ := NewCurrencySettings(in)
			orphan, err := NewFileStore(c.ledgerPath(in.Key))
			if err != nil {
				t.Fatal(err)
			}
			state := cloneState(orphan.data)
			state.Settings = cfg
			if err = orphan.persist(t.Context(), state); err != nil {
				t.Fatal(err)
			}
			if funded {
				if _, err = orphan.Adjust(t.Context(), Adjustment{ActorID: 1, UserID: 1, Delta: 1, ExpectedVersion: 1, RequestID: "orphan-existing-money", Reason: "已有余额"}); err != nil {
					t.Fatal(err)
				}
			} else {
				c, err = NewFileCatalog(c.root)
				if err != nil {
					t.Fatal("empty interrupted creation cannot reopen", err)
				}
			}
			_, _, err = c.Create(t.Context(), in)
			if funded && !errors.Is(err, ErrCurrencyExists) || !funded && err != nil {
				t.Fatal("unsafe orphan recovery", err)
			}
		})
	}
}

func TestFileCatalogRejectsLostCatalogWithCommittedCurrency(t *testing.T) {
	for _, funded := range []bool{false, true} {
		t.Run(fmt.Sprint(funded), func(t *testing.T) {
			dir := t.TempDir()
			c, err := NewFileCatalog(dir)
			if err != nil {
				t.Fatal(err)
			}
			item, _, err := c.Create(t.Context(), CreateCurrency{Key: "gold", Name: "金币", Symbol: "G", Precision: 2, ActorID: 1, RequestID: "lost-catalog-created-gold"})
			if err != nil {
				t.Fatal(err)
			}
			gold, err := c.Store(t.Context(), item.Key)
			if err != nil {
				t.Fatal(err)
			}
			if funded {
				_, err = gold.Adjust(t.Context(), Adjustment{ActorID: 1, UserID: 1, Delta: 1, ExpectedVersion: 1, RequestID: "lost-catalog-existing-funds", Reason: "保留余额"})
			} else {
				cfg := item.Settings
				cfg.Enabled = true
				_, err = gold.UpdateSettings(t.Context(), cfg.Version, cfg)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(c.path); err != nil {
				t.Fatal(err)
			}
			if _, err = NewFileCatalog(dir); !errors.Is(err, ErrStoreUnavailable) {
				t.Fatal("missing catalog hid a committed currency", err)
			}
		})
	}
}

func TestFileCatalogRejectsOlderCatalogHidingUsedLedger(t *testing.T) {
	dir := t.TempDir()
	c, err := NewFileCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	createTestCurrency(t, c, "silver")
	older, err := os.ReadFile(c.path)
	if err != nil {
		t.Fatal(err)
	}
	createTestCurrency(t, c, "gold")
	gold, err := c.Store(t.Context(), "gold")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gold.Adjust(t.Context(), Adjustment{ActorID: 1, UserID: 1, Delta: 10, ExpectedVersion: 1, RequestID: "older-catalog-existing-funds", Reason: "保留账本"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(c.ledgerPath("gold"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(c.path, older, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewFileCatalog(dir); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatal("an older catalog hid a funded ledger", err)
	}
	after, err := os.ReadFile(c.ledgerPath("gold"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed startup changed the existing ledger", err)
	}
}
