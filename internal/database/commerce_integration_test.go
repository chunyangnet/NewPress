package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/payment"
	"newpress.local/newpress/internal/virtualcurrency"
)

type commercePGProvider struct{ creates atomic.Int32 }

func (p *commercePGProvider) Create(_ context.Context, r payment.Request) (payment.Checkout, error) {
	p.creates.Add(1)
	return payment.Checkout{ProviderOrderID: "pg-" + r.OrderID, URL: "https://checkout.example/" + r.OrderID}, nil
}
func (*commercePGProvider) Query(context.Context, string, string) (payment.Notification, error) {
	return payment.Notification{}, payment.ErrUnsupported
}
func (*commercePGProvider) Verify(_ context.Context, h http.Header, b []byte) (payment.Notification, error) {
	if h.Get("X-Test-Verified") != "true" {
		return payment.Notification{}, payment.ErrSignature
	}
	var n payment.Notification
	err := json.Unmarshal(b, &n)
	return n, err
}

func commercePostgres(t *testing.T) (*commerce.Service, *CurrencyRepository, commerce.Settings, []int64) {
	t.Helper()
	coin, _, ids := currencyPostgres(t)
	vault, err := commerce.NewVault(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := vault.Seal(payment.Config{Provider: "epay", Secret: "postgres-test-only"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &commercePGProvider{}
	s := &commerce.Service{Backend: NewCommerceBackend(coin.pool), Vault: vault, Currencies: NewCurrencyCatalog(coin.pool), Members: NewProgressionStore(coin.pool), Users: NewUserRepository(coin.pool), PublicURL: "https://site.example", ProviderFactory: func(payment.Config) (payment.Provider, error) { return provider, nil }}
	memberConfig, err := s.Members.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	memberConfig.Membership.Enabled = true
	if _, err = s.Members.UpdateSettings(t.Context(), memberConfig.Version, memberConfig); err != nil {
		t.Fatal(err)
	}
	cfg := commerce.Defaults()
	cfg.Enabled = true
	cfg.RequireVerifiedEmail = false
	cfg.Channels = []commerce.Channel{{Key: "gateway", Name: "Postgres test gateway", Provider: "epay", Currency: "CNY", Enabled: true, SealedConfig: sealed}}
	cfg.Products = []commerce.Product{{Key: "credit", Title: "Credit balance", Kind: "recharge", Enabled: true, Currency: "CNY", Price: 1250, TargetCurrency: "main", Credit: 12345}, {Key: "member", Title: "Monthly membership", Kind: "membership", Enabled: true, Currency: "CNY", Price: 3000, Plan: "member"}}
	cfg, err = s.UpdateSettings(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, coin, cfg, ids
}
func commercePGNotify(ctx context.Context, s *commerce.Service, o commerce.Order) error {
	n := payment.Notification{OrderID: o.ID, ProviderOrderID: o.Checkout.ProviderOrderID, TransactionID: "tx-" + o.ID, Amount: o.Amount, Currency: o.Currency, Status: payment.Paid}
	b, _ := json.Marshal(n)
	_, err := s.Notify(ctx, o.ID, http.Header{"X-Test-Verified": []string{"true"}}, b)
	return err
}

func TestPostgreSQLCommerceConcurrentCallbacksAndMembershipRenewal(t *testing.T) {
	s, coin, cfg, ids := commercePostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	for _, product := range []string{"credit", "member"} {
		o, err := s.Create(ctx, commerce.Purchase{UserID: ids[1], Version: cfg.Version, Product: product, Channel: "gateway", RequestID: "postgres-order-" + product, Accepted: true})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 24 {
			wg.Go(func() {
				other := *s
				other.Backend = NewCommerceBackend(coin.pool)
				if err := commercePGNotify(ctx, &other, o); err != nil {
					t.Error("concurrent callback", err)
				}
			})
		}
		wg.Wait()
		stored, err := s.Order(ctx, ids[1], o.ID)
		if err != nil || stored.Status != "completed" {
			t.Fatal(stored, err)
		}
	}
	if amount, err := coin.Balance(ctx, ids[1]); err != nil || amount != 12345 {
		t.Fatal("concurrent credits changed balance", amount, err)
	}
	first, err := s.Members.Snapshot(ctx, ids[1], 0)
	if err != nil || first.Account.Membership.Version != 1 {
		t.Fatal(first, err)
	}
	renew, err := s.Create(ctx, commerce.Purchase{UserID: ids[1], Version: cfg.Version, Product: "member", Channel: "gateway", RequestID: "postgres-member-renew", Accepted: true})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := commercePGNotify(ctx, s, renew); err != nil {
			t.Fatal(err)
		}
	}
	last, err := s.Members.Snapshot(ctx, ids[1], 0)
	if err != nil || last.Account.Membership.Version != 2 || !last.Account.Membership.Until.Equal(first.Account.Membership.Until.Add(30*24*time.Hour)) {
		t.Fatal("membership renewal duplicated", last, err)
	}
	if _, err = s.Order(ctx, ids[2], renew.ID); !errors.Is(err, commerce.ErrNotFound) {
		t.Fatal("cross-user order exposed", err)
	}
}

type commercePGInterrupted struct {
	commerce.Backend
	armed atomic.Bool
}

var errCommercePGCommit = errors.New("interrupt PostgreSQL order commit after wallet credit")

func (b *commercePGInterrupted) Update(ctx context.Context, fn func(*commerce.State) error) error {
	return b.Backend.Update(ctx, func(st *commerce.State) error {
		if err := fn(st); err != nil {
			return err
		}
		for _, o := range st.Orders {
			if o.Status == "completed" && b.armed.CompareAndSwap(true, false) {
				return errCommercePGCommit
			}
		}
		return nil
	})
}

func TestPostgreSQLCommerceIndependentLedgerCommitRecovers(t *testing.T) {
	s, coin, cfg, ids := commercePostgres(t)
	o, err := s.Create(t.Context(), commerce.Purchase{UserID: ids[1], Version: cfg.Version, Product: "credit", Channel: "gateway", RequestID: "postgres-credit-interrupt", Accepted: true})
	if err != nil {
		t.Fatal(err)
	}
	broken := &commercePGInterrupted{Backend: s.Backend}
	broken.armed.Store(true)
	s.Backend = broken
	if err := commercePGNotify(t.Context(), s, o); !errors.Is(err, errCommercePGCommit) {
		t.Fatal("commit interruption missing", err)
	}
	if amount, err := coin.Balance(t.Context(), ids[1]); err != nil || amount != 12345 {
		t.Fatal("wallet did not commit", amount, err)
	}
	s.Backend = NewCommerceBackend(coin.pool)
	s.Members = NewProgressionStore(coin.pool)
	s.Currencies = NewCurrencyCatalog(coin.pool)
	result, err := s.Sync(t.Context(), ids[1], o.ID)
	if err != nil || result.Status != "completed" {
		t.Fatal(result, err)
	}
	if amount, err := coin.Balance(t.Context(), ids[1]); err != nil || amount != 12345 {
		t.Fatal("recovery duplicated ledger credit", amount, err)
	}
}

func TestPostgreSQLCommerceExchangeUsesExactUnitsAndSealedCancellation(t *testing.T) {
	s, coin, cfg, ids := commercePostgres(t)
	_, _, err := s.Currencies.Create(t.Context(), virtualcurrency.CreateCurrency{Key: "bonus", Name: "奖励币", Symbol: "点", Precision: 0, ActorID: ids[0], RequestID: "postgres-commerce-bonus"})
	if err != nil {
		t.Fatal(err)
	}
	bonus, err := s.Currencies.Store(t.Context(), "bonus")
	if err != nil {
		t.Fatal(err)
	}
	bc, err := bonus.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	bc.Enabled = true
	if _, err = bonus.UpdateSettings(t.Context(), bc.Version, bc); err != nil {
		t.Fatal(err)
	}
	cfg.Exchanges = []commerce.Exchange{{Key: "main-bonus", Title: "兑换奖励币", Enabled: true, From: "main", To: "bonus", FromUnits: 100, ToUnits: 3, FeeBPS: 250, Minimum: 100, Maximum: 100000}}
	cfg, err = s.UpdateSettings(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	in := commerce.ExchangeInput{UserID: ids[1], Version: cfg.Version, Rule: "main-bonus", Amount: 10000, RequestID: "postgres-exchange-cancel", Accepted: true}
	o, err := s.Exchange(t.Context(), in)
	if err != nil || o.Status != "cancelled" {
		t.Fatal(o, err)
	}
	cc, err := coin.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	currencyPGSeed(t, coin, cc, ids[0], ids[1], 20000)
	if o, err = s.Exchange(t.Context(), in); err != nil || o.Status != "cancelled" {
		t.Fatal("cancelled debit resurrected", o, err)
	}
	in.RequestID = "postgres-exchange-complete"
	if o, err = s.Exchange(t.Context(), in); err != nil || o.Status != "completed" || o.Credit != 292 {
		t.Fatal(o, err)
	}
	for range 3 {
		if _, err = s.Sync(t.Context(), ids[1], o.ID); err != nil {
			t.Fatal(err)
		}
	}
	if amount, err := coin.Balance(t.Context(), ids[1]); err != nil || amount != 10000 {
		t.Fatal("source charged again", amount, err)
	}
	if amount, err := bonus.Balance(t.Context(), ids[1]); err != nil || amount != 292 {
		t.Fatal("target credited wrong precision", amount, err)
	}
	before, err := NewUserRepository(coin.pool).GetUser(t.Context(), ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if before.HasRole("administrator") {
		t.Fatal("commerce changed administrative role")
	}
}
