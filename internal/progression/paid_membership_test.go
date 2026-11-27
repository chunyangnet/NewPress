package progression

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPaidMembershipConcurrentReplayAndRestartDoNotExtendTwice(t *testing.T) {
	s, _, _, now, path := fixture(t)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, err := s.Store.ApplyPaidMembership(t.Context(), 2, "member", 30, "paid-member-same-receipt", "支付订单 fixture-one"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	first, err := s.Store.Snapshot(t.Context(), 2, 0)
	if err != nil || first.Account.Membership.Version != 1 || !first.Account.Membership.Until.Equal(now.Add(30*24*time.Hour)) {
		t.Fatal(first, err)
	}
	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = s.Store.now
	replay, err := reopened.ApplyPaidMembership(t.Context(), 2, "member", 30, "paid-member-same-receipt", "支付订单 fixture-one")
	if err != nil || !replay.Replayed {
		t.Fatal(replay, err)
	}
	for range 16 {
		wg.Go(func() {
			if _, err := reopened.ApplyPaidMembership(t.Context(), 2, "member", 30, "paid-member-next-receipt", "支付订单 fixture-two"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	view, err := reopened.Snapshot(t.Context(), 2, 0)
	if err != nil || view.Account.Membership.Version != 2 || !view.Account.Membership.Until.Equal(now.Add(60*24*time.Hour)) {
		t.Fatal("duplicate external renewal extended membership", view, err)
	}
}

func TestPaidMembershipRejectsReceiptTermsChangesAndActivePlanReplacement(t *testing.T) {
	s, _, _, _, _ := fixture(t)
	const request = "paid-member-immutable-terms"
	if _, err := s.Store.ApplyPaidMembership(t.Context(), 2, "member", 30, request, "已核对支付"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		plan   string
		days   int
		reason string
	}{{"member", 31, "已核对支付"}, {"supporter", 30, "已核对支付"}, {"member", 30, "改动支付说明"}} {
		if _, err := s.Store.ApplyPaidMembership(t.Context(), 2, test.plan, test.days, request, test.reason); !errors.Is(err, ErrReused) {
			t.Fatal("receipt terms changed", test, err)
		}
	}
	if _, err := s.Store.ApplyPaidMembership(t.Context(), 2, "supporter", 365, "paid-member-other-active", "另一套餐付款"); !errors.Is(err, ErrMemberConflict) {
		t.Fatal("different plan replaced active term", err)
	}
	if _, err := s.Store.ApplyPaidMembership(t.Context(), 2, "missing", 30, "paid-member-missing-plan", "无效套餐付款"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown plan accepted", err)
	}
}

func TestPaidMembershipHonorsCapturedTermAfterPlanDisabledAndRejectsDurationOverflow(t *testing.T) {
	s, _, cfg, now, _ := fixture(t)
	cfg.Membership.Plans[0].Days = 365
	cfg.Membership.Plans[0].Enabled = false
	cfg.Membership.Enabled = false
	if _, err := s.Store.UpdateSettings(t.Context(), cfg.Version, cfg); err != nil {
		t.Fatal(err)
	}
	r, err := s.Store.ApplyPaidMembership(t.Context(), 2, "member", 30, "paid-member-old-terms", "先前订单已付款")
	if err != nil || !r.Entry.Membership.Until.Equal(now.Add(30*24*time.Hour)) {
		t.Fatal("captured purchase term changed", r, err)
	}
	if _, err := s.Store.ApplyPaidMembership(t.Context(), 2, "member", MaxMembershipDays, "paid-member-overlong", "超长会员期限"); !errors.Is(err, ErrLimit) {
		t.Fatal("duration limit bypassed", err)
	}
	view, err := s.Store.Snapshot(t.Context(), 2, 0)
	if err != nil || view.MembershipActive || view.Benefits.Active || view.Account.Membership.Version != 1 {
		t.Fatal("module disable granted active rights or failed request changed term", view, err)
	}
}

func TestPaidMembershipWaitsForPendingWalletPurchase(t *testing.T) {
	s, _, cfg, _, _ := fixture(t)
	// Reserve the immutable wallet purchase without charging it.
	in := purchaseInput(cfg)
	coin, err := s.Currencies.Store(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	cc, err := coin.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.BeginPurchase(t.Context(), in, CurrencyQuote{Key: "main", Symbol: cc.Symbol, Version: cc.Version, Precision: cc.Precision, Enabled: cc.Enabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.ApplyPaidMembership(t.Context(), 2, "member", 30, "paid-member-wallet-pending", "外部支付待恢复"); !errors.Is(err, ErrPending) {
		t.Fatal("external payment overwrote pending internal purchase", err)
	}
}
