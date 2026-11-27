package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/payment"
	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/virtualcurrency"
)

type commerceUsers map[int64]auth.User

func (u commerceUsers) GetUser(_ context.Context, id int64) (auth.User, error) {
	if user, ok := u[id]; ok {
		return user, nil
	}
	return auth.User{}, auth.ErrUserNotFound
}

type fakeProvider struct {
	creates, queries atomic.Int32
	mu               sync.Mutex
	latest           map[string]payment.Notification
}

func (p *fakeProvider) Create(_ context.Context, in payment.Request) (payment.Checkout, error) {
	p.creates.Add(1)
	return payment.Checkout{ProviderOrderID: "gateway-" + in.OrderID, URL: "https://checkout.example/" + in.OrderID}, nil
}
func (p *fakeProvider) Query(_ context.Context, _, id string) (payment.Notification, error) {
	p.queries.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	if n, ok := p.latest[id]; ok {
		return n, nil
	}
	return payment.Notification{}, payment.ErrResponse
}
func (*fakeProvider) Verify(_ context.Context, h http.Header, body []byte) (payment.Notification, error) {
	if h.Get("X-Fixture-Verified") != "true" {
		return payment.Notification{}, payment.ErrSignature
	}
	var n payment.Notification
	if err := json.Unmarshal(body, &n); err != nil {
		return n, payment.ErrRequest
	}
	return n, nil
}

type commerceFixture struct {
	service  *Service
	provider *fakeProvider
	dir      string
	cfg      Settings
}

func newCommerceFixture(t *testing.T) commerceFixture {
	t.Helper()
	dir := t.TempDir()
	backend, err := NewFileBackend(filepath.Join(dir, "commerce.json"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := OpenVault(filepath.Join(dir, "payment.key"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := virtualcurrency.NewFileCatalog(filepath.Join(dir, "currencies"))
	if err != nil {
		t.Fatal(err)
	}
	main, err := catalog.Store(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	coin, err := main.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	coin.Enabled, coin.Precision = true, 2
	coin, err = main.UpdateSettings(t.Context(), coin.Version, coin)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = catalog.Create(t.Context(), virtualcurrency.CreateCurrency{Key: "bonus", Name: "奖励币", Symbol: "点", Precision: 0, ActorID: 1, RequestID: "commerce-bonus-currency"}); err != nil {
		t.Fatal(err)
	}
	bonus, err := catalog.Store(t.Context(), "bonus")
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
	members, err := progression.NewFileStore(filepath.Join(dir, "members.json"))
	if err != nil {
		t.Fatal(err)
	}
	memberConfig, err := members.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	memberConfig.Membership.Enabled = true
	if _, err = members.UpdateSettings(t.Context(), memberConfig.Version, memberConfig); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	users := commerceUsers{2: {ID: 2, Status: auth.UserStatusActive, EmailVerifiedAt: &now}, 3: {ID: 3, Status: auth.UserStatusActive, EmailVerifiedAt: &now}}
	provider := &fakeProvider{latest: map[string]payment.Notification{}}
	s := &Service{Backend: backend, Vault: vault, Currencies: catalog, Members: members, Users: users, PublicURL: "https://site.example", ProviderFactory: func(payment.Config) (payment.Provider, error) { return provider, nil }}
	sealed, err := vault.Seal(payment.Config{Provider: "epay", Secret: "fixture-merchant-secret"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Defaults()
	cfg.Enabled = true
	cfg.Channels = []Channel{{Key: "gateway", Name: "测试网关", Provider: "epay", Currency: "CNY", Enabled: true, SealedConfig: sealed}}
	cfg.Products = []Product{{Key: "credit", Title: "充值积分", Kind: "recharge", Enabled: true, Currency: "CNY", Price: 1250, TargetCurrency: "main", Credit: 12345}, {Key: "member", Title: "月度会员", Kind: "membership", Enabled: true, Currency: "CNY", Price: 3000, Plan: "member"}}
	cfg.Exchanges = []Exchange{{Key: "main-bonus", Title: "积分兑换奖励币", Enabled: true, From: "main", To: "bonus", FromUnits: 100, ToUnits: 3, FeeBPS: 250, Minimum: 100, Maximum: 100000}}
	cfg, err = s.UpdateSettings(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return commerceFixture{s, provider, dir, cfg}
}
func (f commerceFixture) purchase(t *testing.T, product, request string) Order {
	t.Helper()
	o, err := f.service.Create(t.Context(), Purchase{UserID: 2, Version: f.cfg.Version, Product: product, Channel: "gateway", RequestID: request, Accepted: true})
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func paidNotification(o Order) payment.Notification {
	return payment.Notification{OrderID: o.ID, ProviderOrderID: o.Checkout.ProviderOrderID, TransactionID: "transaction-" + o.ID, Amount: o.Amount, Currency: o.Currency, Status: payment.Paid}
}
func notify(t *testing.T, s *Service, o Order, n payment.Notification) error {
	t.Helper()
	body, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Notify(t.Context(), o.ID, http.Header{"X-Fixture-Verified": []string{"true"}}, body)
	return err
}
func balance(t *testing.T, s *Service, key string, user int64) int64 {
	t.Helper()
	coin, err := s.Currencies.Store(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := coin.Balance(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	return amount
}
func fund(t *testing.T, s *Service, key string, amount int64, request string) {
	t.Helper()
	coin, err := s.Currencies.Store(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := coin.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: 1, UserID: 2, Delta: amount, ExpectedVersion: cfg.Version, RequestID: request, Reason: "支付测试余额"}); err != nil {
		t.Fatal(err)
	}
}
func (f commerceFixture) restart(t *testing.T) *Service {
	t.Helper()
	next := *f.service
	var err error
	next.Backend, err = NewFileBackend(filepath.Join(f.dir, "commerce.json"))
	if err != nil {
		t.Fatal(err)
	}
	next.Vault, err = OpenVault(filepath.Join(f.dir, "payment.key"))
	if err != nil {
		t.Fatal(err)
	}
	next.Currencies, err = virtualcurrency.NewFileCatalog(filepath.Join(f.dir, "currencies"))
	if err != nil {
		t.Fatal(err)
	}
	next.Members, err = progression.NewFileStore(filepath.Join(f.dir, "members.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = next.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	return &next
}

func TestCommerceConcurrentCheckoutAndCallbacksCreditOnce(t *testing.T) {
	f := newCommerceFixture(t)
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	for range 16 {
		wg.Go(func() {
			o, err := f.service.Create(t.Context(), Purchase{UserID: 2, Version: f.cfg.Version, Product: "credit", Channel: "gateway", RequestID: "same-checkout-request", Accepted: true})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- o.ID
		})
	}
	wg.Wait()
	close(ids)
	var id string
	for next := range ids {
		if id != "" && next != id {
			t.Fatal("duplicate order", id, next)
		}
		id = next
	}
	o, err := f.service.Order(t.Context(), 2, id)
	if err != nil || o.Checkout.URL == "" || f.provider.creates.Load() != 1 {
		t.Fatal(o, err, f.provider.creates.Load())
	}
	if balance(t, f.service, "main", 2) != 0 {
		t.Fatal("checkout initiated wallet credit")
	}
	body, _ := json.Marshal(paidNotification(o))
	for range 16 {
		wg.Go(func() {
			_, err := f.service.Notify(t.Context(), id, http.Header{"X-Fixture-Verified": []string{"true"}}, body)
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	completed, err := f.service.Order(t.Context(), 2, id)
	if err != nil || completed.Status != "completed" || completed.CreditEntry < 1 || balance(t, f.service, "main", 2) != o.Credit {
		t.Fatal(completed, err)
	}
	restarted := f.restart(t)
	if err := notify(t, restarted, o, paidNotification(o)); err != nil {
		t.Fatal(err)
	}
	if balance(t, restarted, "main", 2) != o.Credit {
		t.Fatal("restart callback credited twice")
	}
}

func TestCommerceRejectsForgedTermsAndCrossAccountOrders(t *testing.T) {
	f := newCommerceFixture(t)
	o := f.purchase(t, "credit", "verify-original-order")
	if _, err := f.service.Order(t.Context(), 3, o.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-user order disclosed", err)
	}
	if _, err := f.service.Sync(t.Context(), 3, o.ID); !errors.Is(err, ErrNotFound) || f.provider.queries.Load() != 0 {
		t.Fatal("cross-user order queried", err)
	}
	if _, err := f.service.Create(t.Context(), Purchase{UserID: 2, Version: f.cfg.Version, Product: "member", Channel: "gateway", RequestID: o.RequestID, Accepted: true}); !errors.Is(err, ErrReused) {
		t.Fatal("request terms replaced", err)
	}
	for _, mutate := range []func(*payment.Notification){func(n *payment.Notification) { n.Amount++ }, func(n *payment.Notification) { n.Currency = "USD" }, func(n *payment.Notification) { n.OrderID = NewID() }, func(n *payment.Notification) { n.ProviderOrderID = "other-gateway-order" }, func(n *payment.Notification) { n.TransactionID = "" }} {
		n := paidNotification(o)
		mutate(&n)
		if err := notify(t, f.service, o, n); !errors.Is(err, ErrUnverified) {
			t.Fatal("forged notification accepted", n, err)
		}
	}
	body, _ := json.Marshal(paidNotification(o))
	if _, err := f.service.Notify(t.Context(), o.ID, nil, body); !errors.Is(err, payment.ErrSignature) {
		t.Fatal("unsigned callback accepted", err)
	}
	if balance(t, f.service, "main", 2) != 0 {
		t.Fatal("unverified callback credited wallet")
	}
	if err := notify(t, f.service, o, paidNotification(o)); err != nil {
		t.Fatal(err)
	}
	other, err := f.service.Create(t.Context(), Purchase{UserID: 3, Version: f.cfg.Version, Product: "credit", Channel: "gateway", RequestID: "other-user-order-key", Accepted: true})
	if err != nil {
		t.Fatal(err)
	}
	n := paidNotification(other)
	n.TransactionID = paidNotification(o).TransactionID
	if err := notify(t, f.service, other, n); !errors.Is(err, ErrUnverified) || balance(t, f.service, "main", 3) != 0 {
		t.Fatal("one transaction settled two orders", err)
	}
}

var errCommitInterrupted = errors.New("fixture stopped before order commit")

type interruptCommit struct {
	Backend
	armed  atomic.Bool
	status string
}

func (b *interruptCommit) Update(ctx context.Context, fn func(*State) error) error {
	return b.Backend.Update(ctx, func(st *State) error {
		if err := fn(st); err != nil {
			return err
		}
		for _, o := range st.Orders {
			status := b.status
			if status == "" {
				status = "completed"
			}
			if o.Status == status && b.armed.CompareAndSwap(true, false) {
				return errCommitInterrupted
			}
		}
		return nil
	})
}

func TestCommerceCreditCommitInterruptionRecoversWithoutDoubleCredit(t *testing.T) {
	f := newCommerceFixture(t)
	o := f.purchase(t, "credit", "crash-safe-credit-order")
	b := &interruptCommit{Backend: f.service.Backend}
	b.armed.Store(true)
	f.service.Backend = b
	if err := notify(t, f.service, o, paidNotification(o)); !errors.Is(err, errCommitInterrupted) {
		t.Fatal("interruption not reached", err)
	}
	if balance(t, f.service, "main", 2) != o.Credit {
		t.Fatal("independent ledger did not commit")
	}
	stale, err := f.service.Order(t.Context(), 2, o.ID)
	if err != nil || stale.Status != "paid" {
		t.Fatal("recoverable state lost", stale, err)
	}
	restarted := f.restart(t)
	done, err := restarted.Sync(t.Context(), 2, o.ID)
	if err != nil || done.Status != "completed" || done.CreditEntry < 1 || balance(t, restarted, "main", 2) != o.Credit {
		t.Fatal("credit recovery failed", done, err)
	}
}

func TestCommerceMembershipCommitInterruptionAndRenewal(t *testing.T) {
	f := newCommerceFixture(t)
	o := f.purchase(t, "member", "external-member-first")
	b := &interruptCommit{Backend: f.service.Backend}
	b.armed.Store(true)
	f.service.Backend = b
	if err := notify(t, f.service, o, paidNotification(o)); !errors.Is(err, errCommitInterrupted) {
		t.Fatal(err)
	}
	restarted := f.restart(t)
	done, err := restarted.Sync(t.Context(), 2, o.ID)
	if err != nil || done.Status != "completed" {
		t.Fatal(done, err)
	}
	first, err := restarted.Members.Snapshot(t.Context(), 2, 0)
	if err != nil || first.Account.Membership.Version != 1 {
		t.Fatal(first, err)
	}
	f.service = restarted
	renewal := f.purchase(t, "member", "external-member-renewal")
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			body, _ := json.Marshal(paidNotification(renewal))
			if _, err := restarted.Notify(t.Context(), renewal.ID, http.Header{"X-Fixture-Verified": []string{"true"}}, body); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	view, err := restarted.Members.Snapshot(t.Context(), 2, 0)
	if err != nil || view.Account.Membership.Version != 2 || !view.Account.Membership.Until.Equal(first.Account.Membership.Until.Add(time.Duration(o.Days)*24*time.Hour)) {
		t.Fatal("duplicate renewal changed term", view, err)
	}
	if balance(t, restarted, "main", 2) != 0 {
		t.Fatal("external membership changed internal wallet")
	}
}

func TestCommerceRefundReconciliationBlocksLatePaymentCredit(t *testing.T) {
	f := newCommerceFixture(t)
	o := f.purchase(t, "credit", "refunded-before-fulfill")
	n := paidNotification(o)
	n.Status = payment.Refunded
	if err := notify(t, f.service, o, n); err != nil {
		t.Fatal(err)
	}
	if err := notify(t, f.service, o, paidNotification(o)); err != nil {
		t.Fatal(err)
	}
	done, err := f.service.Order(t.Context(), 2, o.ID)
	if err != nil || done.Status != "review" || balance(t, f.service, "main", 2) != 0 {
		t.Fatal("late paid event bypassed reconciliation", done, err)
	}
}

type controlledCredit struct {
	virtualcurrency.Store
	fail atomic.Bool
}

func (s *controlledCredit) Credit(ctx context.Context, in virtualcurrency.CreditInput) (virtualcurrency.Entry, error) {
	if s.fail.Load() {
		return virtualcurrency.Entry{}, virtualcurrency.ErrStoreUnavailable
	}
	return s.Store.Credit(ctx, in)
}

type overrideCatalog struct {
	virtualcurrency.Catalog
	key   string
	store virtualcurrency.Store
}

func (c overrideCatalog) Store(ctx context.Context, key string) (virtualcurrency.Store, error) {
	if key == c.key {
		return c.store, nil
	}
	return c.Catalog.Store(ctx, key)
}

func TestCommerceExchangeCreditFailureRecoversWithoutSecondDebit(t *testing.T) {
	f := newCommerceFixture(t)
	fund(t, f.service, "main", 20000, "exchange-fund-initial")
	bonus, err := f.service.Currencies.Store(t.Context(), "bonus")
	if err != nil {
		t.Fatal(err)
	}
	failed := &controlledCredit{Store: bonus}
	failed.fail.Store(true)
	f.service.Currencies = overrideCatalog{Catalog: f.service.Currencies, key: "bonus", store: failed}
	in := ExchangeInput{UserID: 2, Version: f.cfg.Version, Rule: "main-bonus", Amount: 10000, RequestID: "exchange-credit-failure", Accepted: true}
	o, err := f.service.Exchange(t.Context(), in)
	if !errors.Is(err, virtualcurrency.ErrStoreUnavailable) || o.Status != "paid" || balance(t, f.service, "main", 2) != 10000 || balance(t, f.service, "bonus", 2) != 0 {
		t.Fatal(o, err)
	}
	failed.fail.Store(false)
	restarted := f.restart(t)
	done, err := restarted.Sync(t.Context(), 2, o.ID)
	if err != nil || done.Status != "completed" || done.Credit != 292 || balance(t, restarted, "main", 2) != 10000 || balance(t, restarted, "bonus", 2) != 292 {
		t.Fatal(done, err)
	}
	if _, err = restarted.Exchange(t.Context(), in); err != nil || balance(t, restarted, "main", 2) != 10000 || balance(t, restarted, "bonus", 2) != 292 {
		t.Fatal("exchange replay moved balances", err)
	}
}

func TestCommerceInsufficientExchangePermanentlyCancelsReceipt(t *testing.T) {
	f := newCommerceFixture(t)
	in := ExchangeInput{UserID: 2, Version: f.cfg.Version, Rule: "main-bonus", Amount: 10000, RequestID: "exchange-insufficient", Accepted: true}
	o, err := f.service.Exchange(t.Context(), in)
	if err != nil || o.Status != "cancelled" || o.SourceEntry < 1 {
		t.Fatal(o, err)
	}
	fund(t, f.service, "main", 20000, "exchange-replenish-later")
	for range 3 {
		retry, err := f.service.Exchange(t.Context(), in)
		if err != nil || retry.Status != "cancelled" {
			t.Fatal(retry, err)
		}
	}
	if balance(t, f.service, "main", 2) != 20000 || balance(t, f.service, "bonus", 2) != 0 {
		t.Fatal("cancelled exchange later charged")
	}
}

func TestCommerceExchangeTargetPrecisionConflictRetainsPaidRecovery(t *testing.T) {
	f := newCommerceFixture(t)
	fund(t, f.service, "main", 10000, "exchange-precision-fund")
	bonus, _ := f.service.Currencies.Store(t.Context(), "bonus")
	failed := &controlledCredit{Store: bonus}
	failed.fail.Store(true)
	f.service.Currencies = overrideCatalog{Catalog: f.service.Currencies, key: "bonus", store: failed}
	o, err := f.service.Exchange(t.Context(), ExchangeInput{UserID: 2, Version: f.cfg.Version, Rule: "main-bonus", Amount: 10000, RequestID: "exchange-precision-change", Accepted: true})
	if !errors.Is(err, virtualcurrency.ErrStoreUnavailable) {
		t.Fatal(err)
	}
	bc, err := bonus.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	bc.Precision = 2
	bc, err = bonus.UpdateSettings(t.Context(), bc.Version, bc)
	if err != nil {
		t.Fatal(err)
	}
	failed.fail.Store(false)
	if _, err := f.service.Sync(t.Context(), 2, o.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("precision changed credited different units", err)
	}
	if balance(t, f.service, "main", 2) != 0 || balance(t, f.service, "bonus", 2) != 0 {
		t.Fatal("precision conflict altered balances")
	}
	bc.Precision = 0
	if _, err = bonus.UpdateSettings(t.Context(), bc.Version, bc); err != nil {
		t.Fatal(err)
	}
	done, err := f.service.Sync(t.Context(), 2, o.ID)
	if err != nil || done.Status != "completed" || balance(t, f.service, "bonus", 2) != 292 {
		t.Fatal(done, err)
	}
}

func TestCommerceDisabledOrUnverifiedAccountsCannotCheckout(t *testing.T) {
	f := newCommerceFixture(t)
	users := f.service.Users.(commerceUsers)
	for i, user := range []auth.User{{ID: 2, Status: auth.UserStatusSuspended}, {ID: 2, Status: auth.UserStatusActive}} {
		users[2] = user
		_, err := f.service.Create(t.Context(), Purchase{UserID: 2, Version: f.cfg.Version, Product: "credit", Channel: "gateway", RequestID: fmt.Sprintf("blocked-account-order-%d", i), Accepted: true})
		if !errors.Is(err, progression.ErrIneligible) {
			t.Fatal("ineligible checkout accepted", err)
		}
	}
	if f.provider.creates.Load() != 0 {
		t.Fatal("provider contacted for ineligible user")
	}
}

func TestCommerceExchangeDebitCommitInterruptionReplaysReceipt(t *testing.T) {
	f := newCommerceFixture(t)
	fund(t, f.service, "main", 20000, "exchange-crash-fund")
	b := &interruptCommit{Backend: f.service.Backend, status: "paid"}
	b.armed.Store(true)
	f.service.Backend = b
	in := ExchangeInput{UserID: 2, Version: f.cfg.Version, Rule: "main-bonus", Amount: 10000, RequestID: "exchange-debit-crash", Accepted: true}
	o, err := f.service.Exchange(t.Context(), in)
	if !errors.Is(err, errCommitInterrupted) || balance(t, f.service, "main", 2) != 10000 {
		t.Fatal("source receipt interruption not reached", o, err)
	}
	persisted, err := f.service.Order(t.Context(), 2, o.ID)
	if err != nil || persisted.Status != "pending" {
		t.Fatal("pending recovery lost", persisted, err)
	}
	restarted := f.restart(t)
	done, err := restarted.Sync(t.Context(), 2, o.ID)
	if err != nil || done.Status != "completed" || balance(t, restarted, "main", 2) != 10000 || balance(t, restarted, "bonus", 2) != 292 {
		t.Fatal("debit receipt not replayed", done, err)
	}
	in.Amount++
	if _, err := restarted.Exchange(t.Context(), in); !errors.Is(err, ErrReused) {
		t.Fatal("changed exchange terms accepted", err)
	}
}

type blockingCredit struct {
	virtualcurrency.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingCredit) Credit(ctx context.Context, in virtualcurrency.CreditInput) (virtualcurrency.Entry, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return s.Store.Credit(ctx, in)
	case <-ctx.Done():
		return virtualcurrency.Entry{}, ctx.Err()
	}
}

func TestCommerceRefundRacingCreditRetainsReceiptForReconciliation(t *testing.T) {
	f := newCommerceFixture(t)
	o := f.purchase(t, "credit", "refund-during-credit")
	coin, err := f.service.Currencies.Store(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	blocked := &blockingCredit{Store: coin, entered: make(chan struct{}), release: make(chan struct{})}
	f.service.Currencies = overrideCatalog{Catalog: f.service.Currencies, key: "main", store: blocked}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	var released sync.Once
	defer released.Do(func() { close(blocked.release) })
	paidDone := make(chan error, 1)
	go func() {
		body, _ := json.Marshal(paidNotification(o))
		_, err := f.service.Notify(ctx, o.ID, http.Header{"X-Fixture-Verified": []string{"true"}}, body)
		paidDone <- err
	}()
	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal("credit not reached", ctx.Err())
	}
	n := paidNotification(o)
	n.Status = payment.Refunded
	refunded := make(chan error, 1)
	go func() {
		body, _ := json.Marshal(n)
		_, err := f.service.Notify(ctx, o.ID, http.Header{"X-Fixture-Verified": []string{"true"}}, body)
		refunded <- err
	}()
	select {
	case err := <-refunded:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("refund blocked behind independent wallet transaction", ctx.Err())
	}
	released.Do(func() { close(blocked.release) })
	if err := <-paidDone; err != nil {
		t.Fatal(err)
	}
	result, err := f.service.Order(t.Context(), 2, o.ID)
	if err != nil || result.Status != "review" || result.CreditEntry < 1 || result.ErrorCode != "refund_requires_reconciliation" || balance(t, f.service, "main", 2) != o.Credit {
		t.Fatal("refund or credit receipt lost", result, err)
	}
	if err := notify(t, f.service, o, paidNotification(o)); err != nil || balance(t, f.service, "main", 2) != o.Credit {
		t.Fatal("late payment bypassed review", err)
	}
}

func TestCommerceRecoveryOnlyRetriesVerifiedEntitlements(t *testing.T) {
	f := newCommerceFixture(t)
	unpaid := f.purchase(t, "credit", "recovery-unpaid-order")
	paid := f.purchase(t, "credit", "recovery-paid-order")
	coin, err := f.service.Currencies.Store(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	failed := &controlledCredit{Store: coin}
	failed.fail.Store(true)
	f.service.Currencies = overrideCatalog{Catalog: f.service.Currencies, key: "main", store: failed}
	if err := notify(t, f.service, paid, paidNotification(paid)); !errors.Is(err, virtualcurrency.ErrStoreUnavailable) {
		t.Fatal(err)
	}
	failed.fail.Store(false)
	creates, queries := f.provider.creates.Load(), f.provider.queries.Load()
	if err := f.service.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	stillPending, err := f.service.Order(t.Context(), 2, unpaid.ID)
	if err != nil || stillPending.Status != "pending" {
		t.Fatal("recovery changed unpaid external order", stillPending, err)
	}
	completed, err := f.service.Order(t.Context(), 2, paid.ID)
	if err != nil || completed.Status != "completed" || balance(t, f.service, "main", 2) != paid.Credit {
		t.Fatal("verified entitlement not recovered", completed, err)
	}
	if f.provider.creates.Load() != creates || f.provider.queries.Load() != queries {
		t.Fatal("recovery initiated or queried external payment")
	}
	if err := f.service.Recover(t.Context()); err != nil || balance(t, f.service, "main", 2) != paid.Credit {
		t.Fatal("recovery replay credited twice", err)
	}
}

func TestCommercePaidOrderWaitsForAccountReactivation(t *testing.T) {
	f := newCommerceFixture(t)
	o := f.purchase(t, "credit", "payment-user-suspended")
	users := f.service.Users.(commerceUsers)
	active := users[2]
	suspended := active
	suspended.Status = auth.UserStatusSuspended
	users[2] = suspended
	if err := notify(t, f.service, o, paidNotification(o)); !errors.Is(err, progression.ErrIneligible) {
		t.Fatal("suspended user received entitlement", err)
	}
	pending, err := f.service.Order(t.Context(), 2, o.ID)
	if err != nil || pending.Status != "paid" || pending.TransactionID == "" || balance(t, f.service, "main", 2) != 0 {
		t.Fatal("verified payment lost while account was suspended", pending, err)
	}
	users[2] = active
	if err := f.service.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	done, err := f.service.Order(t.Context(), 2, o.ID)
	if err != nil || done.Status != "completed" || balance(t, f.service, "main", 2) != o.Credit {
		t.Fatal(done, err)
	}
}
