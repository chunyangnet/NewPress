package commerce

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"newpress.local/newpress/internal/payment"
)

func TestCommerceNotifyBindsEveryStatusBeforeAcknowledging(t *testing.T) {
	mutations := []struct {
		name   string
		change func(*payment.Notification)
	}{
		{"other_order", func(n *payment.Notification) { n.OrderID = NewID() }},
		{"missing_order", func(n *payment.Notification) { n.OrderID = "" }},
		{"other_amount", func(n *payment.Notification) { n.Amount++ }},
		{"missing_amount", func(n *payment.Notification) { n.Amount = 0 }},
		{"other_currency", func(n *payment.Notification) { n.Currency = "USD" }},
		{"missing_currency", func(n *payment.Notification) { n.Currency = "" }},
		{"other_provider_order", func(n *payment.Notification) { n.ProviderOrderID = "another-checkout" }},
		{"missing_provider_order", func(n *payment.Notification) { n.ProviderOrderID = "" }},
	}
	for _, status := range []string{payment.Pending, payment.Failed, payment.Refunded, payment.Paid, "unknown-event"} {
		t.Run(status, func(t *testing.T) {
			f := newCommerceFixture(t)
			o := f.purchase(t, "credit", "notify-bind-every-status")
			for _, mutation := range mutations {
				t.Run(mutation.name, func(t *testing.T) {
					n := paidNotification(o)
					n.Status = status
					mutation.change(&n)
					if err := notify(t, f.service, o, n); !errors.Is(err, ErrUnverified) {
						t.Fatalf("mismatched %s notification acknowledged: %v", status, err)
					}
					after, err := f.service.Order(t.Context(), 2, o.ID)
					if err != nil || !reflect.DeepEqual(after, o) || balance(t, f.service, "main", 2) != 0 {
						t.Fatal("rejected notification changed the order or balance", after, err)
					}
				})
			}
		})
	}
}

func TestCommerceNotifyNonPaidWithoutTransactionDoesNotChangeEntitlements(t *testing.T) {
	for _, status := range []string{payment.Pending, payment.Failed} {
		t.Run(status, func(t *testing.T) {
			f := newCommerceFixture(t)
			o := f.purchase(t, "credit", "notify-nonpaid-empty-transaction")
			n := paidNotification(o)
			n.Status, n.TransactionID = status, ""
			if err := notify(t, f.service, o, n); err != nil {
				t.Fatal("bound unpaid notification rejected", err)
			}
			after, err := f.service.Order(t.Context(), 2, o.ID)
			if err != nil || !reflect.DeepEqual(after, o) || balance(t, f.service, "main", 2) != 0 {
				t.Fatal("unpaid notification changed the order or balance", after, err)
			}
			if err := notify(t, f.service, o, paidNotification(o)); err != nil {
				t.Fatal(err)
			}
			completed, err := f.service.Order(t.Context(), 2, o.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := notify(t, f.service, o, n); err != nil {
				t.Fatal("late unpaid notification rejected", err)
			}
			after, err = f.service.Order(t.Context(), 2, o.ID)
			if err != nil || !reflect.DeepEqual(after, completed) || balance(t, f.service, "main", 2) != o.Credit {
				t.Fatal("late unpaid notification changed payment evidence", after, err)
			}
		})
	}
}

func TestCommerceNotifyNonPaidDoesNotFulfillAnEarlierPaidOrder(t *testing.T) {
	f := newCommerceFixture(t)
	o := f.purchase(t, "credit", "notify-unpaid-does-not-fulfill")
	coin, err := f.service.Currencies.Store(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	controlled := &controlledCredit{Store: coin}
	controlled.fail.Store(true)
	f.service.Currencies = overrideCatalog{Catalog: f.service.Currencies, key: "main", store: controlled}
	if err := notify(t, f.service, o, paidNotification(o)); err == nil {
		t.Fatal("expected interrupted fulfillment")
	}
	before, err := f.service.Order(t.Context(), 2, o.ID)
	if err != nil || before.Status != "paid" {
		t.Fatal(before, err)
	}
	controlled.fail.Store(false)
	n := paidNotification(o)
	n.Status, n.TransactionID = payment.Pending, ""
	if err := notify(t, f.service, o, n); err != nil {
		t.Fatal(err)
	}
	after, err := f.service.Order(t.Context(), 2, o.ID)
	if err != nil || !reflect.DeepEqual(after, before) || balance(t, f.service, "main", 2) != 0 {
		t.Fatal("unpaid notification unexpectedly fulfilled an earlier payment", after, err)
	}
}

func TestCommerceNotifyRefundWithoutTransactionPreservesPaymentEvidence(t *testing.T) {
	for _, paidFirst := range []bool{false, true} {
		name := "before_payment"
		if paidFirst {
			name = "after_payment"
		}
		t.Run(name, func(t *testing.T) {
			f := newCommerceFixture(t)
			o := f.purchase(t, "credit", "notify-refund-empty-transaction")
			if paidFirst {
				if err := notify(t, f.service, o, paidNotification(o)); err != nil {
					t.Fatal(err)
				}
			}
			before, err := f.service.Order(t.Context(), 2, o.ID)
			if err != nil {
				t.Fatal(err)
			}
			n := paidNotification(o)
			n.Status, n.TransactionID = payment.Refunded, ""
			if err := notify(t, f.service, o, n); err != nil {
				t.Fatal("bound refund without transaction rejected", err)
			}
			restarted := f.restart(t)
			after, err := restarted.Order(t.Context(), 2, o.ID)
			if err != nil || after.Status != "review" || after.ErrorCode != "refund_requires_reconciliation" || after.TransactionID != before.TransactionID || after.CreditEntry != before.CreditEntry || !after.PaidAt.Equal(before.PaidAt) {
				t.Fatal("refund lost original payment evidence", after, err)
			}
			if err := notify(t, restarted, o, paidNotification(o)); err != nil {
				t.Fatal(err)
			}
			wantBalance := int64(0)
			if paidFirst {
				wantBalance = o.Credit
			}
			after, err = restarted.Order(t.Context(), 2, o.ID)
			if err != nil || after.Status != "review" || balance(t, restarted, "main", 2) != wantBalance {
				t.Fatal("late payment bypassed refund review", after, err)
			}
		})
	}
}

type verifyHookProvider struct {
	payment.Provider
	afterVerify func() error
}

func (p verifyHookProvider) Verify(ctx context.Context, h http.Header, body []byte) (payment.Notification, error) {
	n, err := p.Provider.Verify(ctx, h, body)
	if err == nil {
		err = p.afterVerify()
	}
	return n, err
}

func TestCommerceNotifyRechecksCheckoutBindingAfterVerification(t *testing.T) {
	for _, status := range []string{payment.Pending, payment.Paid} {
		t.Run(status, func(t *testing.T) {
			f := newCommerceFixture(t)
			o := f.purchase(t, "credit", "notify-checkout-binding-race")
			if err := f.service.Backend.Update(t.Context(), func(st *State) error {
				v := st.Orders[o.ID]
				v.Checkout.ProviderOrderID = ""
				st.Orders[o.ID] = v
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			f.service.ProviderFactory = func(payment.Config) (payment.Provider, error) {
				return verifyHookProvider{Provider: f.provider, afterVerify: func() error {
					return f.service.Backend.Update(t.Context(), func(st *State) error {
						v := st.Orders[o.ID]
						v.Checkout.ProviderOrderID = o.Checkout.ProviderOrderID
						st.Orders[o.ID] = v
						return nil
					})
				}}, nil
			}
			n := paidNotification(o)
			n.Status, n.ProviderOrderID = status, ""
			if err := notify(t, f.service, o, n); !errors.Is(err, ErrUnverified) {
				t.Fatal("notification bypassed newly published checkout binding", err)
			}
			after, err := f.service.Order(t.Context(), 2, o.ID)
			if err != nil || !reflect.DeepEqual(after, o) || balance(t, f.service, "main", 2) != 0 {
				t.Fatal("notification race altered order or balance", after, err)
			}
		})
	}
}
