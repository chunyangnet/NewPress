package commerce

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/payment"
)

func TestShopPaymentVerifiedIdempotentSettlementAndRecovery(t *testing.T) {
	f := newCommerceFixture(t)
	var calls atomic.Int32
	fail := true
	f.service.SettleShop = func(_ context.Context, receipt ShopSettlement) error {
		if receipt.OrderID != "shop_test_order" || receipt.Amount != 1234 || receipt.Currency != "CNY" || receipt.UserID != 2 || receipt.Installment != 1 || receipt.PaymentID == "" {
			t.Fatal("invalid receipt", receipt)
		}
		calls.Add(1)
		if fail {
			return errors.New("interrupted shop commit")
		}
		return nil
	}
	in := ShopPayment{UserID: 2, OrderID: "shop_test_order", Installment: 1, Amount: 1234, Currency: "CNY", Title: "真实商品", Channel: "gateway", Accepted: true, ExpiresAt: time.Now().Add(time.Hour)}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, e := f.service.CreateShopPayment(t.Context(), in); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	orders, e := f.service.ShopPayments(t.Context(), 2, in.OrderID)
	if e != nil || len(orders) != 1 || f.provider.creates.Load() != 1 {
		t.Fatal("duplicate payment", len(orders), e, f.provider.creates.Load())
	}
	o := orders[0]
	invalid := paidNotification(o)
	invalid.Amount++
	if _, e = f.service.apply(t.Context(), o.ID, invalid); !errors.Is(e, ErrUnverified) {
		t.Fatal("wrong price accepted", e)
	}
	if _, e = f.service.apply(t.Context(), o.ID, paidNotification(o)); e == nil {
		t.Fatal("interruption hidden")
	}
	stored, _ := f.service.Order(t.Context(), 2, o.ID)
	if stored.Status != "paid" || stored.ShopSettled {
		t.Fatal(stored)
	}
	fail = false
	if e = f.service.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	stored, _ = f.service.Order(t.Context(), 2, o.ID)
	if stored.Status != "completed" || !stored.ShopSettled || calls.Load() != 2 {
		t.Fatal(stored.Status, calls.Load())
	}
	if _, e = f.service.apply(t.Context(), o.ID, paidNotification(o)); e != nil || calls.Load() != 2 {
		t.Fatal("replayed callback", e, calls.Load())
	}
	in.Amount++
	if _, e = f.service.CreateShopPayment(t.Context(), in); !errors.Is(e, ErrReused) {
		t.Fatal("price rebound", e)
	}
	if _, e = f.service.Order(t.Context(), 3, o.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("order privacy", e)
	}
	var refunds atomic.Int32
	f.service.ReviewShop = func(_ context.Context, receipt ShopSettlement) error { refunds.Add(1); return nil }
	n := paidNotification(o)
	n.Status = payment.Refunded
	if _, e = f.service.apply(t.Context(), o.ID, n); e != nil || refunds.Load() != 1 {
		t.Fatal("refund propagation", e)
	}
}
