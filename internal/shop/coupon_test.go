package shop

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func couponFor(f *fixture, code, kind string, value int64) Coupon {
	return Coupon{
		Code: code, Title: "测试优惠券", Kind: kind, Value: value, Currency: "CNY",
		PerUserLimit: 1, StartsAt: f.now.Add(-time.Hour), EndsAt: f.now.Add(48 * time.Hour), Enabled: true,
	}
}

func saveCoupons(t *testing.T, f *fixture, coupons ...Coupon) {
	t.Helper()
	if _, err := f.s.SaveCoupons(t.Context(), 0, coupons); err != nil {
		t.Fatal(err)
	}
}

func TestCouponClaimLimitIsAtomicAndEligibilityIsVisible(t *testing.T) {
	p := item("physical", "physical", "direct")
	p.Category = "prints"
	f := newFixture(t, p)
	c := couponFor(f, "PRINT-10", "fixed", 1000)
	c.ClaimLimit = 1
	c.Kinds = []string{"physical"}
	c.Products = []string{p.Key}
	c.Categories = []string{p.Category}
	c.Modes = []string{"direct"}
	saveCoupons(t, f, c)

	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded, stockErrors := 0, 0
	otherErrors := []error{}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.s.ClaimCoupon(t.Context(), 23, c.Code)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				succeeded++
			} else if errors.Is(err, ErrStock) {
				stockErrors++
			} else {
				otherErrors = append(otherErrors, err)
			}
		}()
	}
	wg.Wait()
	if succeeded != 1 || stockErrors != 15 || len(otherErrors) != 0 {
		t.Fatalf("claims: succeeded=%d stock=%d other=%v", succeeded, stockErrors, otherErrors)
	}

	options, err := f.s.CouponList(t.Context(), 23, false)
	if err != nil || len(options) != 1 || !options[0].Claimed || options[0].CanClaim || options[0].AvailableCount != 1 || options[0].UserClaimCount != 1 {
		t.Fatalf("claim projection: %+v, %v", options, err)
	}
	quote, err := f.s.QuoteFor(t.Context(), 23, QuoteInput{Product: p.Key, Quantity: 1, Coupon: c.Code})
	if err != nil || quote.Discount != 1000 || quote.Total != 9000 {
		t.Fatalf("eligible quote: %+v, %v", quote, err)
	}

	other := item("other", "virtual", "direct")
	current, _ := f.s.Catalog(t.Context())
	current.Products = append(current.Products, other)
	if _, err = f.s.SaveCatalog(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.QuoteFor(t.Context(), 23, QuoteInput{Product: other.Key, Quantity: 1, Coupon: c.Code}); !errors.Is(err, ErrIneligible) {
		t.Fatalf("product restriction was not enforced: %v", err)
	}
}

func TestCouponRepricesInstallmentsAndRefundReviewLifecycle(t *testing.T) {
	base := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	p := item("camera", "physical", "installment")
	p.Price = 10001
	p.ShippingFee = 600
	p.Installment = &InstallmentPlan{Periods: []int{3}, IntervalDays: 30, InterestBPS: 100}
	f := newFixture(t, p)
	f.now = base
	c := couponFor(f, "CAMERA-10", "percent", 1000)
	c.MaximumDiscount = 500
	c.Minimum = 5000
	c.Modes = []string{"installment"}
	saveCoupons(t, f, c)
	if _, err := f.s.ClaimCoupon(t.Context(), 31, c.Code); err != nil {
		t.Fatal(err)
	}

	quote, err := f.s.QuoteFor(t.Context(), 31, QuoteInput{Product: p.Key, Quantity: 1, Periods: 3, Coupon: c.Code})
	if err != nil {
		t.Fatal(err)
	}
	if quote.OriginalSubtotal != 10001 || quote.Discount != 500 || quote.Subtotal != 9501 || quote.Interest != 288 || quote.Total != 10389 || len(quote.Schedule) != 3 {
		t.Fatalf("coupon did not reprice installment plan: %+v", quote)
	}
	var scheduled int64
	for _, installment := range quote.Schedule {
		if installment.Amount != installment.Principal+installment.Interest || installment.Amount < 1 {
			t.Fatalf("invalid installment after discount: %+v", installment)
		}
		scheduled += installment.Amount
	}
	if scheduled != quote.Total || quote.PayNow != quote.Schedule[0].Amount {
		t.Fatalf("inconsistent discounted schedule: %+v", quote)
	}

	direct := item("download", "virtual", "direct")
	f = newFixture(t, direct)
	c = couponFor(f, "REFUND-10", "fixed", 1000)
	saveCoupons(t, f, c)
	if _, err = f.s.ClaimCoupon(t.Context(), 44, c.Code); err != nil {
		t.Fatal(err)
	}
	o := f.create(t, direct.Key, 44, func(in *Purchase) { in.Coupon = c.Code })
	o = f.pay(t, o, 0)
	if o.Status != "completed" || o.Discount != 1000 {
		t.Fatalf("discounted order did not complete: %+v", o)
	}
	o, err = f.s.RequestRefund(t.Context(), 44, o.ID, "订单需要售后处理")
	if err != nil || o.Status != "refund_requested" {
		t.Fatalf("refund request: %+v, %v", o, err)
	}
	o, err = f.s.ReviewRefund(t.Context(), o.ID, false, "不符合退款条件")
	if err != nil || o.Status != "completed" || o.RefundReview != "不符合退款条件" {
		t.Fatalf("refund rejection: %+v, %v", o, err)
	}
	if _, err = f.s.RequestRefund(t.Context(), 44, o.ID, "补充售后申请"); err != nil {
		t.Fatal(err)
	}
	o, err = f.s.ReviewRefund(t.Context(), o.ID, true, "同意退款")
	if err != nil || o.Status != "refund_required" {
		t.Fatalf("refund approval: %+v, %v", o, err)
	}
	o, err = f.s.ReconcileRefund(t.Context(), Refund{OrderID: o.ID, Reference: "provider-refund-44", Note: "渠道退款已核对"})
	if err != nil || o.Status != "refunded" {
		t.Fatalf("refund reconciliation: %+v, %v", o, err)
	}

	state, err := f.s.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	claim, ok := usableClaim(state, c.Code, 44)
	if ok {
		t.Fatalf("used coupon was returned after a paid refund: %+v", claim)
	}
	if _, err = f.s.ClaimCoupon(t.Context(), 44, c.Code); !errors.Is(err, ErrStock) {
		t.Fatalf("per-user claim limit did not persist through refund: %v", err)
	}
}

func TestPercentCouponNeedsCurrencyForAmountRules(t *testing.T) {
	c := Coupon{Code: "ANY-10", Title: "全站折扣", Kind: "percent", Value: 1000, PerUserLimit: 1, StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Enabled: true}
	if err := validateCouponSet([]Coupon{c}, InitialState().Catalog); err != nil {
		t.Fatalf("unrestricted percentage coupon should be valid: %v", err)
	}
	for _, mutate := range []func(*Coupon){
		func(v *Coupon) { v.Minimum = 100 },
		func(v *Coupon) { v.MaximumDiscount = 100 },
	} {
		bad := c
		mutate(&bad)
		if err := validateCouponSet([]Coupon{bad}, InitialState().Catalog); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unrestricted coupon with currency-denominated amount rule accepted: %v", err)
		}
	}
}
