package shop

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/virtualcurrency"
)

func regressionWalletFixture(t *testing.T, p Product) (*fixture, *virtualcurrency.FileStore) {
	t.Helper()
	dir := t.TempDir()
	ledger, err := virtualcurrency.NewFileStore(filepath.Join(dir, "currency.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ledger.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled, cfg.RequireVerifiedEmail = true, false
	if _, err = ledger.UpdateSettings(t.Context(), cfg.Version, cfg); err != nil {
		t.Fatal(err)
	}
	f := &fixture{now: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC), path: filepath.Join(dir, "shop.json")}
	backend, err := NewFileBackend(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.s = &Service{Backend: backend, Now: func() time.Time { return f.now }, Currencies: virtualcurrency.NewMemoryCatalog(ledger)}
	p.Currency, p.Precision = "wallet:main", cfg.Precision
	catalog := InitialState().Catalog
	catalog.Enabled, catalog.Products = true, []Product{p}
	if _, err = f.s.SaveCatalog(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	return f, ledger
}

func regressionCredit(t *testing.T, ledger *virtualcurrency.FileStore, user, amount int64, request string) {
	t.Helper()
	cfg, err := ledger.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.Credit(t.Context(), virtualcurrency.CreditInput{UserID: user, Amount: amount, ExpectedVersion: cfg.Version, RequestID: request, Source: "audit fixture credit"}); err != nil {
		t.Fatal(err)
	}
}

func TestRegressionRefundRequestRetainsUnshippedStock(t *testing.T) {
	p := item("refund-stock", "physical", "direct")
	p.Stock = 1
	f := newFixture(t, p)
	o := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	if _, err := f.s.RequestRefund(t.Context(), 1, o.ID, "please review"); err != nil {
		t.Fatal(err)
	}
	c, err := f.s.Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if c.Products[0].Stock != 0 {
		t.Errorf("unapproved refund returned stock: got %d, want 0", c.Products[0].Stock)
	}
	if _, err = f.s.Quote(t.Context(), QuoteInput{Product: p.Key, Quantity: 1}); !errors.Is(err, ErrStock) {
		t.Errorf("a second buyer can quote the already reserved item: %v", err)
	}
	restored, err := f.s.ReviewRefund(t.Context(), o.ID, false, "request rejected")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Released {
		t.Errorf("refund rejection restored %s with Released=true", restored.Status)
	}
}

func TestRegressionRejectedPresaleRefundStillFulfills(t *testing.T) {
	p := item("refund-presale", "virtual", "presale")
	p.ReleaseAt = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, p)
	o := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	if _, err := f.s.RequestRefund(t.Context(), 1, o.ID, "please review"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReviewRefund(t.Context(), o.ID, false, "request rejected"); err != nil {
		t.Fatal(err)
	}
	f.now = p.ReleaseAt.Add(time.Minute)
	if err := f.s.Advance(t.Context()); err != nil {
		t.Fatalf("a rejected refund prevents the shared advance transaction: %v", err)
	}
	o, err := f.s.Order(t.Context(), 1, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != "completed" {
		t.Fatalf("rejected refund prevented delivery: %s", o.Status)
	}
}

func TestRegressionWalletPaymentRetriesAfterInsufficientBalance(t *testing.T) {
	p := item("wallet-retry", "virtual", "direct")
	p.Price = 100
	f, ledger := regressionWalletFixture(t, p)
	o := f.create(t, p.Key, 1, nil)
	for range 3 {
		if _, err := f.s.PayWallet(t.Context(), 1, o.ID, 0); !errors.Is(err, virtualcurrency.ErrInsufficient) {
			t.Fatalf("payment should fail for insufficient balance: %v", err)
		}
	}
	regressionCredit(t, ledger, 1, 100, "audit-wallet-recharge-0001")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := f.s.PayWallet(t.Context(), 1, o.ID, 0)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("retry after funding a cancelled payment fails: %v", err)
		}
	}
	paid, err := f.s.Order(t.Context(), 1, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paid.Status != "completed" {
		t.Fatalf("payment did not finish: %s", paid.Status)
	}
	requireRegressionBalance(t, ledger, 1, 0)
	st, err := f.s.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cancelled, active := 0, 0
	for _, payment := range st.WalletPayments {
		if payment.Status == "cancelled" {
			cancelled++
		} else {
			active++
		}
	}
	if cancelled != 3 || active != 1 {
		t.Fatalf("payment retry lost cancelled receipts or created duplicate active intents: %d/%d", cancelled, active)
	}
}

type regressionObservedBackend struct {
	Backend
	orderID   string
	completed chan struct{}
}

func (b *regressionObservedBackend) Update(ctx context.Context, fn func(*State) error) error {
	refunded := false
	err := b.Backend.Update(ctx, func(st *State) error {
		if err := fn(st); err != nil {
			return err
		}
		refunded = st.Orders[b.orderID].Status == "refunded"
		return nil
	})
	if err == nil && refunded {
		select {
		case b.completed <- struct{}{}:
		default:
		}
	}
	return err
}

func TestRegressionWalletGroupTimeoutRefundsInWorker(t *testing.T) {
	p := item("wallet-group", "virtual", "group")
	p.Price, p.Group = 100, &GroupPlan{Size: 2, Price: 80, Hours: 1}
	f, ledger := regressionWalletFixture(t, p)
	regressionCredit(t, ledger, 1, 500, "audit-group-funds")
	o := f.create(t, p.Key, 1, nil)
	if _, err := f.s.PayWallet(t.Context(), 1, o.ID, 0); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Hour)
	observer := &regressionObservedBackend{Backend: f.s.Backend, orderID: o.ID, completed: make(chan struct{}, 1)}
	f.s.Backend = observer
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); f.s.Run(ctx) }()
	select {
	case <-observer.completed:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not complete the wallet refund")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
	current, err := f.s.Order(t.Context(), 1, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	balance, err := ledger.Balance(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "refunded" || balance != 500 {
		t.Errorf("worker left timeout refund unprocessed: status=%s balance=%d, want refunded/500", current.Status, balance)
	}
	if err = f.s.RecoverWallet(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err = f.s.Order(t.Context(), 1, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	balance, err = ledger.Balance(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "refunded" || balance != 500 {
		t.Fatalf("explicit recovery also failed: %s/%d", current.Status, balance)
	}
}

func TestRegressionGroupCompletesAfterRefundRejection(t *testing.T) {
	p := item("refund-group", "virtual", "group")
	p.Group = &GroupPlan{Size: 2, Price: 8000, Hours: 1}
	f := newFixture(t, p)
	a := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	if _, err := f.s.RequestRefund(t.Context(), 1, a.ID, "please review"); err != nil {
		t.Fatal(err)
	}
	b := f.create(t, p.Key, 2, func(in *Purchase) { in.GroupID = a.GroupID })
	f.pay(t, b, 0)
	if _, err := f.s.ReviewRefund(t.Context(), a.ID, false, "request rejected"); err != nil {
		t.Fatal(err)
	}
	st, err := f.s.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	g := st.Groups[a.GroupID]
	if g.Status != "completed" || g.Paid != g.Size {
		t.Fatalf("group did not complete after rejection: status=%s paid=%d size=%d", g.Status, g.Paid, g.Size)
	}
	for _, id := range g.Orders {
		if current := st.Orders[id]; current.Status != "completed" || len(current.Deliveries) != 1 || current.Released {
			t.Fatalf("group member did not retain its delivery: %+v", current)
		}
	}
}

func TestRegressionQuoteRejectsExhaustedCouponUseLimit(t *testing.T) {
	p := item("coupon-limit", "virtual", "direct")
	f := newFixture(t, p)
	c := Coupon{Code: "ONE-USE", Title: "one use", Kind: "fixed", Value: 100, Currency: "CNY", UseLimit: 1, PerUserLimit: 1, StartsAt: f.now.Add(-time.Hour), EndsAt: f.now.Add(time.Hour), Enabled: true}
	if _, err := f.s.SaveCoupons(t.Context(), 0, []Coupon{c}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []int64{1, 2} {
		if _, err := f.s.ClaimCoupon(t.Context(), user, c.Code); err != nil {
			t.Fatal(err)
		}
	}
	f.pay(t, f.create(t, p.Key, 1, func(in *Purchase) { in.Coupon = c.Code }), 0)
	quote, err := f.s.QuoteFor(t.Context(), 2, QuoteInput{Product: p.Key, Quantity: 1, Coupon: c.Code})
	if !errors.Is(err, ErrStock) {
		t.Fatalf("exhausted coupon is still quoted: discount=%d error=%v", quote.Discount, err)
	}
}

func TestRegressionCanDeleteLastUnclaimedCoupon(t *testing.T) {
	f := newFixture(t, item("coupon-delete", "virtual", "direct"))
	c := Coupon{Code: "DELETE-ME", Title: "unclaimed coupon", Kind: "fixed", Value: 100, Currency: "CNY", PerUserLimit: 1, StartsAt: f.now.Add(-time.Hour), EndsAt: f.now.Add(time.Hour), Enabled: true}
	version, err := f.s.SaveCoupons(t.Context(), 0, []Coupon{c})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SaveCoupons(t.Context(), version, []Coupon{}); err != nil {
		t.Fatalf("deleting the final unclaimed coupon fails: %v", err)
	}
	f.restart(t)
	currentVersion, coupons, err := f.s.CouponConfig(t.Context())
	if err != nil || coupons == nil || len(coupons) != 0 || currentVersion != version+1 {
		t.Fatalf("empty coupon configuration did not persist: %d/%+v/%v", currentVersion, coupons, err)
	}
}

func TestRegressionScheduledRaffleDrawsOnlyOncePerRound(t *testing.T) {
	p := item("refund-raffle", "raffle", "direct")
	p.Raffle = &RafflePlan{Mode: "scheduled", DrawAt: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), Winners: 1, Prize: "one prize"}
	f := newFixture(t, p)
	a := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	b := f.pay(t, f.create(t, p.Key, 2, nil), 0)
	if _, err := f.s.RequestRefund(t.Context(), 1, a.ID, "please review"); err != nil {
		t.Fatal(err)
	}
	f.now = p.Raffle.DrawAt.Add(time.Minute)
	if err := f.s.Advance(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := f.s.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if before.Orders[id].Tickets[0].DrawnAt.IsZero() {
			t.Fatal("refund review excluded a paid ticket from the original draw")
		}
	}
	if _, err := f.s.ReviewRefund(t.Context(), a.ID, false, "request rejected"); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- f.s.Advance(t.Context()) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, err := f.s.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	winners := 0
	for _, id := range []string{a.ID, b.ID} {
		if st.Orders[id].Tickets[0] != before.Orders[id].Tickets[0] {
			t.Fatal("persisted draw changed after refund rejection or restart")
		}
		for _, ticket := range st.Orders[id].Tickets {
			if ticket.Status == "won" {
				winners++
			}
		}
	}
	if winners != p.Raffle.Winners {
		t.Fatalf("refund rejection triggered a second draw for the same round: winners=%d configured=%d", winners, p.Raffle.Winners)
	}
}

func TestRegressionWalletRefundCannotCompleteWithoutLedgerCredit(t *testing.T) {
	p := item("wallet-reconcile", "virtual", "group")
	p.Price, p.Group = 100, &GroupPlan{Size: 2, Price: 80, Hours: 1}
	f, ledger := regressionWalletFixture(t, p)
	regressionCredit(t, ledger, 1, 500, "audit-reconcile-funds")
	o := f.create(t, p.Key, 1, nil)
	if _, err := f.s.PayWallet(t.Context(), 1, o.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RequestRefund(t.Context(), 1, o.ID, "please review"); err != nil {
		t.Fatal(err)
	}
	cfg, err := ledger.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = false
	cfg, err = ledger.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ReviewRefund(t.Context(), o.ID, true, "approved"); !errors.Is(err, virtualcurrency.ErrDisabled) {
		t.Fatalf("disabled ledger should leave the approved refund pending: %v", err)
	}
	if _, err = f.s.ReconcileRefund(t.Context(), Refund{OrderID: o.ID, Reference: "audit-manual-refund", Note: "external receipt"}); !errors.Is(err, virtualcurrency.ErrDisabled) {
		t.Fatalf("manual reconciliation bypassed the wallet ledger: %v", err)
	}
	current, err := f.s.Order(t.Context(), 1, o.ID)
	if err != nil || current.Status != "refund_required" {
		t.Fatalf("failed credit completed the refund: %+v/%v", current, err)
	}
	cfg.Enabled = true
	if _, err = ledger.UpdateSettings(t.Context(), cfg.Version, cfg); err != nil {
		t.Fatal(err)
	}
	current, err = f.s.ReconcileRefund(t.Context(), Refund{OrderID: o.ID})
	if err != nil || current.Status != "refunded" || current.RefundReference != walletRefundReference(o.ID) {
		t.Fatalf("retry did not complete a verified wallet refund: %+v/%v", current, err)
	}
	if err = f.s.RecoverWallet(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err = f.s.Order(t.Context(), 1, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	balance, err := ledger.Balance(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "refunded" || balance != 500 {
		t.Fatalf("manual reconciliation hides an uncredited wallet refund from recovery: %s balance=%d", current.Status, balance)
	}
	if err = f.s.ValidateWallet(t.Context()); err != nil {
		t.Fatalf("verified wallet refund failed ledger validation: %v", err)
	}
}
