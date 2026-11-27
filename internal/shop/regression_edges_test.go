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

func restartRegressionWallet(t *testing.T, f *fixture) *virtualcurrency.FileStore {
	t.Helper()
	f.restart(t)
	ledger, err := virtualcurrency.NewFileStore(filepath.Join(filepath.Dir(f.path), "currency.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.s.Currencies = virtualcurrency.NewMemoryCatalog(ledger)
	return ledger
}

func requireRegressionBalance(t *testing.T, ledger *virtualcurrency.FileStore, user, want int64) {
	t.Helper()
	balance, err := ledger.Balance(t.Context(), user)
	if err != nil || balance != want {
		t.Fatalf("wallet balance: got %d, want %d, error=%v", balance, want, err)
	}
}

func TestRegressionRefundApprovalReleasesStockOnce(t *testing.T) {
	p := item("approval-stock", "physical", "direct")
	p.Stock = 1
	f := newFixture(t, p)
	o := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	if _, err := f.s.RequestRefund(t.Context(), 1, o.ID, "please review"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Ship(t.Context(), Shipment{OrderID: o.ID, Carrier: "carrier", Tracking: "tracking"}); !errors.Is(err, ErrState) {
		t.Fatalf("shipping was allowed during refund review: %v", err)
	}
	approved, err := f.s.ReviewRefund(t.Context(), o.ID, true, "approved")
	if err != nil || approved.Status != "refund_required" || !approved.Released {
		t.Fatalf("approved refund did not release inventory: %+v/%v", approved, err)
	}
	refund := Refund{OrderID: o.ID, Reference: "provider-approved-refund", Note: "verified"}
	for range 2 {
		if _, err = f.s.ReconcileRefund(t.Context(), refund); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := f.s.Catalog(t.Context())
	if err != nil || catalog.Products[0].Stock != 1 {
		t.Fatalf("refund restocked more than once: %+v/%v", catalog, err)
	}
}

func TestRegressionReleasedRefundStateIsRejected(t *testing.T) {
	p := item("reserved-presale", "cdk", "presale")
	p.Stock, p.Codes = 1, []string{"RESERVED-CODE"}
	p.ReleaseAt = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, p)
	o := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	if _, err := f.s.RequestRefund(t.Context(), 1, o.ID, "please review"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Backend.Update(t.Context(), func(st *State) error {
		v := st.Orders[o.ID]
		releaseStock(st, &v)
		st.Orders[v.ID] = v
		return nil
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted a refund request with released stock: %v", err)
	}
	state, err := f.s.Backend.Read(t.Context())
	if err != nil || state.Catalog.Products[0].Stock != 0 || state.Orders[o.ID].Released || state.Orders[o.ID].Status != "refund_requested" {
		t.Fatalf("rejected state changed the reservation: %+v/%v", state, err)
	}
	current, err := f.s.ReviewRefund(t.Context(), o.ID, false, "request rejected")
	if err != nil || current.Status != "awaiting_release" || current.Released {
		t.Fatalf("refund rejection lost a valid reservation: %+v/%v", current, err)
	}
	f.now = p.ReleaseAt.Add(time.Minute)
	if err = f.s.Advance(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err = f.s.Order(t.Context(), 1, o.ID)
	if err != nil || current.Status != "completed" || len(current.Deliveries) != 1 || current.Deliveries[0].Content != "RESERVED-CODE" {
		t.Fatalf("reserved order did not complete delivery: %+v/%v", current, err)
	}
}

func TestRegressionReleasedShipmentStateIsRejected(t *testing.T) {
	p := item("reserved-shipment", "physical", "direct")
	p.Stock = 1
	f := newFixture(t, p)
	o := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	if err := f.s.Backend.Update(t.Context(), func(st *State) error {
		v := st.Orders[o.ID]
		releaseStock(st, &v)
		st.Orders[v.ID] = v
		return nil
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted an active shipment with released stock: %v", err)
	}
	current, err := f.s.Ship(t.Context(), Shipment{OrderID: o.ID, Carrier: "carrier", Tracking: "tracking"})
	if err != nil || current.Status != "shipped" || current.Released || current.ShippedAt.IsZero() {
		t.Fatalf("rejected state prevented the reserved shipment: %+v/%v", current, err)
	}
}

func TestRegressionGroupRefundRejectionAtDeadlineStillRefunds(t *testing.T) {
	p := item("deadline-group", "virtual", "group")
	p.Stock, p.Group = 2, &GroupPlan{Size: 2, Price: 8000, Hours: 1}
	f := newFixture(t, p)
	a := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	if _, err := f.s.RequestRefund(t.Context(), 1, a.ID, "please review"); err != nil {
		t.Fatal(err)
	}
	b := f.pay(t, f.create(t, p.Key, 2, func(in *Purchase) { in.GroupID = a.GroupID }), 0)
	f.now = f.now.Add(time.Hour)
	if err := f.s.Advance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReviewRefund(t.Context(), a.ID, false, "request rejected"); err != nil {
		t.Fatal(err)
	}
	st, err := f.s.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Groups[a.GroupID].Status != "failed" || st.Catalog.Products[0].Stock != 2 {
		t.Fatalf("deadline rejection completed a failed group or lost stock: %+v", st)
	}
	for _, id := range []string{a.ID, b.ID} {
		if o := st.Orders[id]; o.Status != "refund_required" || !o.Released || len(o.Deliveries) != 0 {
			t.Fatalf("expired group member resumed delivery: %+v", o)
		}
	}
}

func TestRegressionCouponReservationBlocksQuoteAndCreate(t *testing.T) {
	p := item("reserved-coupon", "virtual", "direct")
	f := newFixture(t, p)
	c := couponFor(f, "RESERVED-ONE", "fixed", 100)
	c.UseLimit, c.PerUserLimit = 1, 2
	saveCoupons(t, f, c)
	for _, user := range []int64{1, 1, 2} {
		if _, err := f.s.ClaimCoupon(t.Context(), user, c.Code); err != nil {
			t.Fatal(err)
		}
	}
	in := QuoteInput{Product: p.Key, Quantity: 1, Coupon: c.Code}
	if _, err := f.s.QuoteFor(t.Context(), 2, in); err != nil {
		t.Fatal(err)
	}
	o := f.create(t, p.Key, 1, func(in *Purchase) { in.Coupon = c.Code })
	for _, user := range []int64{1, 2} {
		if _, err := f.s.QuoteFor(t.Context(), user, in); !errors.Is(err, ErrStock) {
			t.Fatalf("reserved use limit was ignored for user %d: %v", user, err)
		}
	}
	catalog, err := f.s.Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Create(t.Context(), Purchase{QuoteInput: in, UserID: 2, Version: catalog.Version, RequestID: "coupon-quote-became-stale", Accepted: true}); !errors.Is(err, ErrStock) {
		t.Fatalf("create failed to recheck coupon capacity: %v", err)
	}
	if _, err = f.s.Cancel(t.Context(), 1, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.QuoteFor(t.Context(), 2, in); err != nil {
		t.Fatalf("cancelled reservation kept coupon capacity: %v", err)
	}
	f.pay(t, f.create(t, p.Key, 2, func(in *Purchase) { in.Coupon = c.Code }), 0)
	if _, err = f.s.QuoteFor(t.Context(), 1, in); !errors.Is(err, ErrStock) {
		t.Fatalf("used coupon capacity was reset: %v", err)
	}
}

func TestRegressionRaffleReviewPausesPrizeDelivery(t *testing.T) {
	p := item("held-prize", "raffle", "direct")
	p.Raffle = &RafflePlan{Mode: "scheduled", DrawAt: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), Winners: 1, Prize: "one prize"}
	f := newFixture(t, p)
	o := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	if _, err := f.s.RequestRefund(t.Context(), 1, o.ID, "please review"); err != nil {
		t.Fatal(err)
	}
	f.now = p.Raffle.DrawAt
	if err := f.s.Advance(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := f.s.Order(t.Context(), 1, o.ID)
	if err != nil || current.Status != "refund_requested" || current.Tickets[0].Status != "won" {
		t.Fatalf("held paid ticket missed the original draw: %+v/%v", current, err)
	}
	if _, err = f.s.DeliverPrize(t.Context(), o.ID, current.Tickets[0].ID, "prize"); !errors.Is(err, ErrState) {
		t.Fatalf("prize delivery bypassed refund review: %v", err)
	}
	current, err = f.s.ReviewRefund(t.Context(), o.ID, false, "rejected")
	if err != nil || current.Status != "completed" {
		t.Fatalf("review rejection did not restore the drawn ticket: %+v/%v", current, err)
	}
	if _, err = f.s.DeliverPrize(t.Context(), o.ID, current.Tickets[0].ID, "prize"); err != nil {
		t.Fatal(err)
	}
}

func TestRegressionRaffleApprovedRefundExcludedBeforeDraw(t *testing.T) {
	p := item("approved-raffle", "raffle", "direct")
	p.Raffle = &RafflePlan{Mode: "scheduled", DrawAt: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), Winners: 1, Prize: "one prize"}
	f := newFixture(t, p)
	a := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	b := f.pay(t, f.create(t, p.Key, 2, nil), 0)
	if _, err := f.s.RequestRefund(t.Context(), 1, a.ID, "please review"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReviewRefund(t.Context(), a.ID, true, "approved"); err != nil {
		t.Fatal(err)
	}
	f.now = p.Raffle.DrawAt
	if err := f.s.Advance(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := f.s.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Orders[a.ID].Tickets[0].Status != "pending" || st.Orders[b.ID].Tickets[0].Status != "won" {
		t.Fatal("approved refund participated in the draw")
	}
}

func TestRegressionLegacyRaffleRoundWithRefundedWinnerStaysSealed(t *testing.T) {
	p := item("legacy-raffle", "raffle", "direct")
	p.Raffle = &RafflePlan{Mode: "scheduled", DrawAt: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), Winners: 1, Prize: "one prize"}
	f := newFixture(t, p)
	a := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	b := f.pay(t, f.create(t, p.Key, 2, nil), 0)
	if _, err := f.s.RequestRefund(t.Context(), 1, a.ID, "please review"); err != nil {
		t.Fatal(err)
	}
	f.now = p.Raffle.DrawAt
	// The old implementation drew only b while a was awaiting review.
	if err := f.s.Backend.Update(t.Context(), func(st *State) error {
		o := st.Orders[b.ID]
		o.Tickets[0].Status, o.Tickets[0].DrawnAt = "won", f.now
		o.Status, o.UpdatedAt = "refund_required", f.now
		st.Orders[o.ID] = o
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReconcileRefund(t.Context(), Refund{OrderID: b.ID, Reference: "legacy-raffle-refund", Note: "verified"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReviewRefund(t.Context(), a.ID, false, "rejected"); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	if err := f.s.Advance(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := f.s.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Orders[a.ID].Tickets[0].Status != "lost" || st.Orders[b.ID].Tickets[0].Status != "won" || st.Orders[b.ID].Status != "refunded" {
		t.Fatal("legacy refunded winner allowed another prize in its round")
	}
}

var errRegressionCommit = errors.New("simulated shop commit interruption")

type regressionFailBackend struct {
	Backend
	mu        sync.Mutex
	remaining int
	matches   func(State) bool
}

func (b *regressionFailBackend) Update(ctx context.Context, fn func(*State) error) error {
	return b.Backend.Update(ctx, func(st *State) error {
		if err := fn(st); err != nil {
			return err
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.remaining > 0 && b.matches(*st) {
			b.remaining--
			return errRegressionCommit
		}
		return nil
	})
}

func TestRegressionWalletRefundRecoversInterruptedCommits(t *testing.T) {
	for _, phase := range []string{"receipt", "completion"} {
		t.Run(phase, func(t *testing.T) {
			p := item("interrupted-refund", "virtual", "direct")
			p.Price = 100
			f, ledger := regressionWalletFixture(t, p)
			regressionCredit(t, ledger, 1, 500, "interrupted-refund-funds")
			o := f.create(t, p.Key, 1, nil)
			if _, err := f.s.PayWallet(t.Context(), 1, o.ID, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.RequestRefund(t.Context(), 1, o.ID, "please review"); err != nil {
				t.Fatal(err)
			}
			f.s.Backend = &regressionFailBackend{Backend: f.s.Backend, remaining: 1, matches: func(st State) bool {
				if phase == "completion" {
					return st.Orders[o.ID].Status == "refunded"
				}
				for _, payment := range st.WalletPayments {
					if payment.OrderID == o.ID && payment.RefundEntry > 0 {
						return true
					}
				}
				return false
			}}
			if _, err := f.s.ReviewRefund(t.Context(), o.ID, true, "approved"); !errors.Is(err, errRegressionCommit) {
				t.Fatalf("did not interrupt refund %s commit: %v", phase, err)
			}
			requireRegressionBalance(t, ledger, 1, 500)
			ledger = restartRegressionWallet(t, f)
			if err := f.s.ValidateWallet(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := f.s.RecoverWallet(t.Context()); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for range 8 {
				wg.Go(func() {
					_, err := f.s.ReconcileRefund(t.Context(), Refund{OrderID: o.ID, Reference: "ignored-external-reference"})
					errs <- err
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			requireRegressionBalance(t, ledger, 1, 500)
			current, err := f.s.Order(t.Context(), 1, o.ID)
			if err != nil || current.Status != "refunded" || current.RefundReference != walletRefundReference(o.ID) {
				t.Fatalf("interrupted refund did not recover: %+v/%v", current, err)
			}
			entries, err := ledger.History(t.Context(), 1, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			credits := 0
			for _, entry := range entries {
				if entry.Kind == "credit" && entry.Reason == "商城退款 "+o.ID {
					credits++
				}
			}
			if credits != 1 {
				t.Fatalf("refund retries issued %d credits", credits)
			}
			if err = f.s.ValidateWallet(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRegressionWalletRefundFencesOutstandingInstallment(t *testing.T) {
	for _, debit := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-debit", true: "unacknowledged-debit"}[debit], func(t *testing.T) {
			p := item("pending-installment", "virtual", "installment")
			p.Price, p.Installment = 100, &InstallmentPlan{Periods: []int{2}, IntervalDays: 30}
			f, ledger := regressionWalletFixture(t, p)
			regressionCredit(t, ledger, 1, 500, "pending-installment-funds")
			o := f.create(t, p.Key, 1, func(in *Purchase) { in.Periods = 2 })
			if _, err := f.s.PayWallet(t.Context(), 1, o.ID, 0); err != nil {
				t.Fatal(err)
			}
			cfg, err := ledger.Settings(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			intent := WalletPayment{ID: newID("sw_"), OrderID: o.ID, UserID: 1, Index: 1, Currency: "wallet:main", Amount: o.Schedule[1].Amount, Version: cfg.Version, Precision: cfg.Precision, Status: "pending", CreatedAt: f.now}
			if err = f.s.Backend.Update(t.Context(), func(st *State) error { st.WalletPayments[intent.ID] = intent; return nil }); err != nil {
				t.Fatal(err)
			}
			if debit {
				if _, _, err = ledger.Spend(t.Context(), intent.spend()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = f.s.RequestRefund(t.Context(), 1, o.ID, "please review"); err != nil {
				t.Fatal(err)
			}
			current, err := f.s.ReviewRefund(t.Context(), o.ID, true, "approved")
			if err != nil || current.Status != "refunded" {
				t.Fatalf("pending wallet intent blocked refund recovery: %+v/%v", current, err)
			}
			requireRegressionBalance(t, ledger, 1, 500)
			if err = f.s.finishWallet(t.Context(), intent, true); err != nil && !errors.Is(err, ErrState) {
				t.Fatal(err)
			}
			requireRegressionBalance(t, ledger, 1, 500)
			st, err := f.s.Backend.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			payment := st.WalletPayments[intent.ID]
			if debit && (payment.Status != "paid" || payment.RefundEntry < 1) || !debit && (payment.Status != "cancelled" || payment.RefundEntry != 0) {
				t.Fatalf("refund ignored the actual ledger result: %+v", payment)
			}
			if err = f.s.ValidateWallet(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRegressionWalletLegacyRefundedOrderRecoversCredit(t *testing.T) {
	p := item("legacy-wallet-refund", "virtual", "group")
	p.Price, p.Group = 100, &GroupPlan{Size: 2, Price: 80, Hours: 1}
	f, ledger := regressionWalletFixture(t, p)
	regressionCredit(t, ledger, 1, 500, "legacy-wallet-refund-funds")
	o := f.create(t, p.Key, 1, nil)
	if _, err := f.s.PayWallet(t.Context(), 1, o.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Backend.Update(t.Context(), func(st *State) error {
		v := st.Orders[o.ID]
		v.Status, v.RefundReference, v.RefundNote = "refunded", "legacy-manual-reference", "manually recorded"
		releaseStock(st, &v)
		st.Orders[v.ID] = v
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	requireRegressionBalance(t, ledger, 1, 420)
	ledger = restartRegressionWallet(t, f)
	if err := f.s.ValidateWallet(t.Context()); err != nil {
		t.Fatalf("legacy missing refund receipt blocked recovery: %v", err)
	}
	for range 2 {
		if err := f.s.Advance(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	requireRegressionBalance(t, ledger, 1, 500)
	current, err := f.s.Order(t.Context(), 1, o.ID)
	if err != nil || current.Status != "refunded" || current.RefundReference != walletRefundReference(o.ID) {
		t.Fatalf("legacy manual refund was not reconciled with the ledger: %+v/%v", current, err)
	}
}

func TestRegressionWalletUnapprovedRefundCannotCredit(t *testing.T) {
	p := item("unapproved-wallet", "virtual", "direct")
	p.Price = 100
	f, ledger := regressionWalletFixture(t, p)
	regressionCredit(t, ledger, 1, 500, "unapproved-wallet-funds")
	o := f.create(t, p.Key, 1, nil)
	if _, err := f.s.PayWallet(t.Context(), 1, o.ID, 0); err != nil {
		t.Fatal(err)
	}
	for _, requested := range []bool{false, true} {
		if requested {
			if _, err := f.s.RequestRefund(t.Context(), 1, o.ID, "please review"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.s.ReconcileRefund(t.Context(), Refund{OrderID: o.ID, Reference: "unapproved-external-refund"}); !errors.Is(err, ErrState) {
			t.Fatalf("manual reconciliation bypassed approval: %v", err)
		}
		if err := f.s.RecoverWallet(t.Context()); err != nil {
			t.Fatal(err)
		}
		requireRegressionBalance(t, ledger, 1, 400)
	}
}

func TestRegressionWalletUnacknowledgedDebitRecoversWithoutDoubleSpend(t *testing.T) {
	p := item("interrupted-payment", "virtual", "direct")
	p.Price = 100
	f, ledger := regressionWalletFixture(t, p)
	regressionCredit(t, ledger, 1, 500, "interrupted-payment-funds")
	o := f.create(t, p.Key, 1, nil)
	f.s.Backend = &regressionFailBackend{Backend: f.s.Backend, remaining: 1, matches: func(st State) bool {
		for _, payment := range st.WalletPayments {
			if payment.OrderID == o.ID && payment.Status == "paid" {
				return true
			}
		}
		return false
	}}
	if _, err := f.s.PayWallet(t.Context(), 1, o.ID, 0); !errors.Is(err, errRegressionCommit) {
		t.Fatalf("did not interrupt payment acknowledgement: %v", err)
	}
	requireRegressionBalance(t, ledger, 1, 400)
	ledger = restartRegressionWallet(t, f)
	if err := f.s.Advance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PayWallet(t.Context(), 1, o.ID, 0); err != nil {
		t.Fatal(err)
	}
	requireRegressionBalance(t, ledger, 1, 400)
	current, err := f.s.Order(t.Context(), 1, o.ID)
	if err != nil || current.Status != "completed" || current.PaidAmount != 100 || len(current.Deliveries) != 1 {
		t.Fatalf("ledger debit did not recover order delivery: %+v/%v", current, err)
	}
}
