package shop

import (
	"errors"
	"math"
	"testing"
)

func TestCatalogShowsEffectiveStockWithoutModifyingPrivateInventory(t *testing.T) {
	c := InitialState().Catalog
	code := item("code", "cdk", "direct")
	code.Stock = -1
	code.Codes = []string{"ONE", "TWO", "THREE"}
	code.Variants = []Variant{{Key: "standard", Title: "标准版", Price: 100, Stock: 20}}
	physical := item("physical", "physical", "direct")
	physical.Stock = 5
	bundle := item("bundle", "bundle", "direct")
	bundle.Bundle = []BundleItem{{Product: "physical", Quantity: 2}}
	c.Products = []Product{code, physical, bundle}
	visible := PublicCatalog(c)
	if visible.Products[0].Stock != 3 || visible.Products[0].Variants[0].Stock != 3 || visible.Products[2].Stock != 2 {
		t.Fatal(visible.Products)
	}
	if c.Products[0].Stock != -1 || c.Products[0].Variants[0].Stock != 20 || len(c.Products[0].Codes) != 3 {
		t.Fatal("projection changed private inventory")
	}
	c.Products[1].Enabled = false
	if PublicCatalog(c).Products[2].Stock != 0 {
		t.Fatal("disabled bundle child remained purchasable")
	}
}

func TestChangedGroupTermsRequireANewGroup(t *testing.T) {
	p := item("group", "virtual", "group")
	p.Group = &GroupPlan{Size: 2, Price: 5000, Hours: 24}
	f := newFixture(t, p)
	leader := f.create(t, p.Key, 1, nil)
	c, _ := f.s.Catalog(t.Context())
	c.Products[0].Delivery = "changed entitlement"
	if _, err := f.s.SaveCatalog(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Quote(t.Context(), QuoteInput{Product: p.Key, Quantity: 1, GroupID: leader.GroupID}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestStateRejectsBrokenPaymentReferencesAndOverflow(t *testing.T) {
	f := newFixture(t, item("digital", "virtual", "direct"))
	order := f.pay(t, f.create(t, "digital", 1, nil), 0)
	pristine, err := f.s.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*State){
		func(st *State) { delete(st.Receipts, order.Schedule[0].PaymentRef) },
		func(st *State) {
			r := st.Receipts[order.Schedule[0].PaymentRef]
			r.Amount++
			st.Receipts[r.Reference] = r
		},
		func(st *State) {
			o := st.Orders[order.ID]
			o.Schedule[0].Amount = math.MaxInt64
			o.Schedule[0].Principal = math.MaxInt64
			st.Orders[o.ID] = o
		},
		func(st *State) { o := st.Orders[order.ID]; o.Snapshot.Key = "wrong"; st.Orders[o.ID] = o },
		func(st *State) { o := st.Orders[order.ID]; o.OriginalSubtotal = 0; st.Orders[o.ID] = o },
	} {
		st := clone(pristine)
		mutate(&st)
		if ValidateState(st) == nil {
			t.Fatal("corrupt state accepted")
		}
	}
}

func TestPrizeDeliveryIsOwnedDurableAndCannotRewriteDraw(t *testing.T) {
	p := item("raffle", "raffle", "direct")
	p.Raffle = &RafflePlan{Mode: "instant", WinBPS: 10000, Winners: 1, Prize: "测试奖品"}
	f := newFixture(t, p)
	order := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	ticket := order.Tickets[0]
	delivered, err := f.s.DeliverPrize(t.Context(), order.ID, ticket.ID, "物流公司：测试物流\n运单号：TEST123")
	if err != nil || delivered.Tickets[0].DeliveredAt.IsZero() || delivered.Tickets[0].Status != "won" {
		t.Fatal(delivered, err)
	}
	if _, err = f.s.DeliverPrize(t.Context(), order.ID, ticket.ID, delivered.Tickets[0].Delivery); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.DeliverPrize(t.Context(), order.ID, ticket.ID, "other"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = f.s.Order(t.Context(), 2, order.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	f.restart(t)
	restored, _ := f.s.Order(t.Context(), 1, order.ID)
	if restored.Tickets[0].Delivery != delivered.Tickets[0].Delivery {
		t.Fatal(restored)
	}
	c, _ := f.s.Catalog(t.Context())
	stock := c.Products[0].Stock
	if _, err = f.s.RequestRefund(t.Context(), 1, order.ID, "售后申请"); err != nil {
		t.Fatal(err)
	}
	c, _ = f.s.Catalog(t.Context())
	if c.Products[0].Stock != stock {
		t.Fatal("issued ticket was restocked")
	}
}

func TestAdminCannotPreventReservedStockFromReturning(t *testing.T) {
	f := newFixture(t, item("physical", "physical", "direct"))
	o := f.create(t, "physical", 1, nil)
	c, _ := f.s.Catalog(t.Context())
	c.Products[0].Stock = 1_000_000_000
	if _, err := f.s.SaveCatalog(t.Context(), c); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := f.s.Cancel(t.Context(), 1, o.ID); err != nil {
		t.Fatal(err)
	}
}
