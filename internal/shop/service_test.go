package shop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	s    *Service
	now  time.Time
	path string
}

func newFixture(t *testing.T, products ...Product) *fixture {
	t.Helper()
	f := &fixture{now: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC), path: filepath.Join(t.TempDir(), "shop.json")}
	b, e := NewFileBackend(f.path)
	if e != nil {
		t.Fatal(e)
	}
	f.s = &Service{Backend: b, Now: func() time.Time { return f.now }}
	c := InitialState().Catalog
	c.Enabled = true
	c.Products = products
	if _, e = f.s.SaveCatalog(t.Context(), c); e != nil {
		t.Fatal(e)
	}
	return f
}
func item(key, kind, mode string) Product {
	return Product{Key: key, Title: "商品 " + key, Kind: kind, Mode: mode, Currency: "CNY", Price: 10000, Stock: 50, Enabled: true, Delivery: "https://example.test/private/download"}
}
func (f *fixture) create(t *testing.T, key string, user int64, edit func(*Purchase)) Order {
	t.Helper()
	c, e := f.s.Catalog(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	in := Purchase{QuoteInput: QuoteInput{Product: key, Quantity: 1}, UserID: user, Version: c.Version, RequestID: fmt.Sprintf("request-%s-%d-%d", key, user, c.Version), Accepted: true, Address: Address{Name: "收货人", Phone: "123456789", Detail: "测试收货地址"}}
	if edit != nil {
		edit(&in)
	}
	o, e := f.s.Create(t.Context(), in)
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func (f *fixture) pay(t *testing.T, o Order, index int) Order {
	t.Helper()
	p, e := f.s.Payment(t.Context(), o.UserID, o.ID, index)
	if e != nil {
		t.Fatal(e)
	}
	out, e := f.s.MarkPaid(t.Context(), PaymentReceipt{OrderID: o.ID, UserID: o.UserID, Index: index, Reference: fmt.Sprintf("payment-%s-%d", o.ID, index), Amount: p.Amount, Currency: p.Currency})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func (f *fixture) restart(t *testing.T) {
	t.Helper()
	b, e := NewFileBackend(f.path)
	if e != nil {
		t.Fatal(e)
	}
	f.s = &Service{Backend: b, Now: func() time.Time { return f.now }}
}

func TestCDKReservedPrivateIdempotentAndDurable(t *testing.T) {
	p := item("cdk", "cdk", "direct")
	p.Stock = 1
	p.Codes = []string{"SECRET-CODE"}
	f := newFixture(t, p)
	o := f.create(t, p.Key, 1, nil)
	raw, _ := json.Marshal(PublicOrder(o))
	if strings.Contains(string(raw), "SECRET-CODE") || strings.Contains(string(raw), "private/download") {
		t.Fatal("unpaid secret leaked")
	}
	c, _ := f.s.Catalog(t.Context())
	raw, _ = json.Marshal(PublicCatalog(c))
	if strings.Contains(string(raw), "SECRET-CODE") || strings.Contains(string(raw), "private/download") {
		t.Fatal("catalog secret leaked")
	}
	if _, e := f.s.Quote(t.Context(), QuoteInput{Product: p.Key, Quantity: 1}); !errors.Is(e, ErrStock) {
		t.Fatal(e)
	}
	if _, e := f.s.Order(t.Context(), 2, o.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("other owner", e)
	}
	f.restart(t)
	o = f.pay(t, o, 0)
	if o.Status != "completed" || len(o.Deliveries) != 1 || o.Deliveries[0].Content != "SECRET-CODE" {
		t.Fatal(o)
	}
	receipt := PaymentReceipt{OrderID: o.ID, UserID: 1, Index: 0, Reference: fmt.Sprintf("payment-%s-0", o.ID), Amount: o.Total, Currency: "CNY"}
	if _, e := f.s.MarkPaid(t.Context(), receipt); e != nil {
		t.Fatal(e)
	}
	receipt.Reference = "different-payment"
	if _, e := f.s.MarkPaid(t.Context(), receipt); !errors.Is(e, ErrReused) {
		t.Fatal("duplicate payment accepted", e)
	}
	f.restart(t)
	again, _ := f.s.Order(t.Context(), 1, o.ID)
	if again.PaidAmount != o.Total || len(again.Deliveries) != 1 {
		t.Fatal(again)
	}
	c, _ = f.s.Catalog(t.Context())
	c.Products[0].Codes = []string{"SECRET-CODE"}
	if _, e := f.s.SaveCatalog(t.Context(), c); e == nil {
		t.Fatal("issued code reimport accepted")
	}
}
func TestInventoryCancellationLatePaymentAndRefund(t *testing.T) {
	p := item("cdk", "cdk", "direct")
	p.Stock = 1
	p.Codes = []string{"A"}
	f := newFixture(t, p)
	o := f.create(t, p.Key, 1, nil)
	if _, e := f.s.Cancel(t.Context(), 2, o.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if _, e := f.s.Cancel(t.Context(), 1, o.ID); e != nil {
		t.Fatal(e)
	}
	c, _ := f.s.Catalog(t.Context())
	if c.Products[0].Stock != 1 || len(c.Products[0].Codes) != 1 {
		t.Fatal(c)
	}
	next := f.create(t, p.Key, 2, nil)
	late, e := f.s.MarkPaid(t.Context(), PaymentReceipt{OrderID: o.ID, UserID: 1, Index: 0, Reference: "late-receipt", Amount: o.Total, Currency: "CNY"})
	if e != nil || late.Status != "refund_required" || len(late.Deliveries) != 0 {
		t.Fatal(late, e)
	}
	next = f.pay(t, next, 0)
	if next.Deliveries[0].Content != "A" {
		t.Fatal(next)
	}
	late, e = f.s.ReconcileRefund(t.Context(), Refund{OrderID: o.ID, Reference: "refund-provider-1", Note: "支付渠道已退款并核对"})
	if e != nil || late.Status != "refunded" {
		t.Fatal(e)
	}
}
func TestCreateIdempotencyConcurrentStockAndAdminConflict(t *testing.T) {
	p := item("limited", "virtual", "direct")
	p.Stock = 1
	f := newFixture(t, p)
	c, _ := f.s.Catalog(t.Context())
	in := Purchase{QuoteInput: QuoteInput{Product: p.Key, Quantity: 1}, UserID: 1, Version: c.Version, RequestID: "same-request", Accepted: true}
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := f.s.Create(context.Background(), in)
			if e != nil {
				errs <- e
			} else {
				ids <- o.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	id := ""
	for next := range ids {
		if id != "" && next != id {
			t.Fatal("multiple orders")
		}
		id = next
	}
	in.Quantity = 2
	if _, e := f.s.Create(t.Context(), in); !errors.Is(e, ErrReused) {
		t.Fatal(e)
	}
	c.Products[0].Stock = 99
	if _, e := f.s.SaveCatalog(t.Context(), c); !errors.Is(e, ErrConflict) {
		t.Fatal("stale inventory save", e)
	}
}
func TestBundleAtomicReservationAndPhysicalShipment(t *testing.T) {
	a := item("digital", "virtual", "direct")
	b := item("physical", "physical", "direct")
	b.Stock = 1
	set := item("set", "bundle", "direct")
	set.Bundle = []BundleItem{{Product: a.Key, Quantity: 2}, {Product: b.Key, Quantity: 1}}
	set.ShippingFee = 600
	f := newFixture(t, a, b, set)
	c, _ := f.s.Catalog(t.Context())
	_, e := f.s.Create(t.Context(), Purchase{QuoteInput: QuoteInput{Product: set.Key, Quantity: 1}, UserID: 1, Version: c.Version, RequestID: "no-address", Accepted: true})
	if !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	o := f.create(t, set.Key, 1, nil)
	if o.Total != 10600 || !o.RequiresAddress || len(o.Reservations) != 3 {
		t.Fatal(o)
	}
	o = f.pay(t, o, 0)
	if o.Status != "awaiting_shipment" || len(o.Deliveries) != 1 || o.Deliveries[0].Quantity != 2 {
		t.Fatal(o)
	}
	if _, e = f.s.Ship(t.Context(), Shipment{OrderID: o.ID, Carrier: "快递", Tracking: "TRACK123"}); e != nil {
		t.Fatal(e)
	}
	o, e = f.s.ConfirmReceipt(t.Context(), 1, o.ID)
	if e != nil || o.Status != "completed" {
		t.Fatal(e)
	}
	c, _ = f.s.Catalog(t.Context())
	if c.Products[0].Stock != 48 || c.Products[1].Stock != 0 {
		t.Fatal(c.Products)
	}
}
func TestDepositPresaleAndInstallmentSchedules(t *testing.T) {
	base := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	d := item("deposit", "virtual", "deposit")
	d.Deposit = 2500
	d.ReleaseAt = base.Add(24 * time.Hour)
	d.BalanceDueAt = base.Add(48 * time.Hour)
	pre := item("presale", "virtual", "presale")
	pre.ReleaseAt = base.Add(24 * time.Hour)
	inst := item("installments", "physical", "installment")
	inst.Price = 10001
	inst.ShippingFee = 600
	inst.Installment = &InstallmentPlan{Periods: []int{3}, IntervalDays: 30, InterestBPS: 100}
	f := newFixture(t, d, pre, inst)
	o := f.create(t, d.Key, 1, nil)
	if o.PayNow != 2500 || o.Schedule[1].Amount != 7500 {
		t.Fatal(o)
	}
	o = f.pay(t, o, 0)
	if o.Status != "partially_paid" {
		t.Fatal(o.Status)
	}
	if _, e := f.s.Payment(t.Context(), 1, o.ID, 1); !errors.Is(e, ErrState) {
		t.Fatal("tail payment before release", e)
	}
	presale := f.pay(t, f.create(t, pre.Key, 2, nil), 0)
	if presale.Status != "awaiting_release" {
		t.Fatal(presale)
	}
	plan := f.create(t, inst.Key, 3, func(p *Purchase) { p.Periods = 3 })
	if plan.Interest != 303 || plan.Total != 10904 || plan.Schedule[0].Principal != 3934 || plan.Schedule[1].Principal != 3334 || plan.Schedule[2].Principal != 3333 {
		t.Fatal(plan.Quote)
	}
	if _, e := f.s.Payment(t.Context(), 3, plan.ID, 1); !errors.Is(e, ErrState) {
		t.Fatal("out of order payment", e)
	}
	plan = f.pay(t, plan, 0)
	plan = f.pay(t, plan, 1)
	plan = f.pay(t, plan, 2)
	if plan.Status != "awaiting_shipment" {
		t.Fatal(plan.Status)
	}
	f.now = f.now.Add(24 * time.Hour)
	f.restart(t)
	if e := f.s.Advance(t.Context()); e != nil {
		t.Fatal(e)
	}
	o = f.pay(t, o, 1)
	if o.Status != "completed" {
		t.Fatal(o)
	}
	presale, _ = f.s.Order(t.Context(), 2, presale.ID)
	if presale.Status != "completed" {
		t.Fatal(presale)
	}
}
func TestGroupSuccessFailureAndUniqueMembers(t *testing.T) {
	p := item("group", "cdk", "group")
	p.Codes = []string{"A", "B", "C", "D"}
	p.Group = &GroupPlan{Size: 2, Price: 8000, Hours: 1}
	f := newFixture(t, p)
	a := f.create(t, p.Key, 1, nil)
	a = f.pay(t, a, 0)
	if a.Status != "awaiting_group" || len(a.Deliveries) != 0 {
		t.Fatal(a)
	}
	c, _ := f.s.Catalog(t.Context())
	_, e := f.s.Create(t.Context(), Purchase{QuoteInput: QuoteInput{Product: p.Key, Quantity: 1, GroupID: a.GroupID}, UserID: 1, Version: c.Version, RequestID: "same-member", Accepted: true})
	if !errors.Is(e, ErrState) {
		t.Fatal(e)
	}
	b := f.create(t, p.Key, 2, func(in *Purchase) { in.GroupID = a.GroupID })
	b = f.pay(t, b, 0)
	a, _ = f.s.Order(t.Context(), 1, a.ID)
	if a.Status != "completed" || b.Status != "completed" || a.Deliveries[0].Content == b.Deliveries[0].Content {
		t.Fatal(a, b)
	}
	failed := f.pay(t, f.create(t, p.Key, 3, nil), 0)
	f.now = f.now.Add(2 * time.Hour)
	if e = f.s.Advance(t.Context()); e != nil {
		t.Fatal(e)
	}
	failed, _ = f.s.Order(t.Context(), 3, failed.ID)
	if failed.Status != "refund_required" || !failed.Released {
		t.Fatal(failed)
	}
	f.restart(t)
	c, _ = f.s.Catalog(t.Context())
	if len(c.Products[0].Codes) != 2 {
		t.Fatal(c.Products[0].Codes)
	}
}
func TestRaffleInstantScheduledPersistentAndNoLateEntry(t *testing.T) {
	instant := item("instant", "raffle", "direct")
	instant.Raffle = &RafflePlan{Mode: "instant", WinBPS: 10000, Winners: 1, Prize: "体验资格"}
	scheduled := item("scheduled", "raffle", "direct")
	scheduled.Raffle = &RafflePlan{Mode: "scheduled", DrawAt: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), Winners: 2, Prize: "纪念礼物"}
	f := newFixture(t, instant, scheduled)
	o := f.pay(t, f.create(t, instant.Key, 1, nil), 0)
	if o.Status != "completed" || o.Tickets[0].Status != "won" {
		t.Fatal(o)
	}
	ids := []string{}
	for i := int64(1); i <= 4; i++ {
		x := f.pay(t, f.create(t, scheduled.Key, i, nil), 0)
		ids = append(ids, x.ID)
	}
	late := f.create(t, scheduled.Key, 8, nil)
	f.now = f.now.Add(2 * time.Hour)
	if e := f.s.Advance(t.Context()); e != nil {
		t.Fatal(e)
	}
	winners := 0
	before := map[string]string{}
	for _, id := range ids {
		o, _ := f.s.Order(t.Context(), 0, id)
		if o.Status != "completed" {
			t.Fatal(o)
		}
		before[id] = o.Tickets[0].Status
		if o.Tickets[0].Status == "won" {
			winners++
		}
	}
	if winners != 2 {
		t.Fatal(winners)
	}
	f.restart(t)
	if e := f.s.Advance(t.Context()); e != nil {
		t.Fatal(e)
	}
	for id, status := range before {
		o, _ := f.s.Order(t.Context(), 0, id)
		if o.Tickets[0].Status != status {
			t.Fatal("redraw")
		}
	}
	late, e := f.s.MarkPaid(t.Context(), PaymentReceipt{OrderID: late.ID, UserID: 8, Index: 0, Reference: "late-ticket", Amount: late.Total, Currency: "CNY"})
	if e != nil || late.Status != "refund_required" || len(late.Tickets) != 0 {
		t.Fatal(late, e)
	}
}
func TestDonationValidationBoundsAndWorkerExpiry(t *testing.T) {
	p := item("support", "donation", "direct")
	p.MaxDonation = 100000
	f := newFixture(t, p)
	for _, amount := range []int64{0, 9999, 100001} {
		if _, e := f.s.Quote(t.Context(), QuoteInput{Product: p.Key, Quantity: 1, DonationAmount: amount}); !errors.Is(e, ErrInvalid) {
			t.Fatal(amount, e)
		}
	}
	o := f.create(t, p.Key, 1, func(p *Purchase) { p.DonationAmount = 15000 })
	if o.Total != 15000 {
		t.Fatal(o)
	}
	f.now = f.now.Add(time.Hour)
	if e := f.s.Advance(t.Context()); e != nil {
		t.Fatal(e)
	}
	o, _ = f.s.Order(t.Context(), 1, o.ID)
	if o.Status != "expired" || !o.Released {
		t.Fatal(o)
	}
	if _, e := f.s.Payment(t.Context(), 1, o.ID, 0); !errors.Is(e, ErrState) {
		t.Fatal(e)
	}
}
