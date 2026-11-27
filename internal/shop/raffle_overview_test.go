package shop

import (
	"errors"
	"testing"
	"time"
)

func TestPublishedRaffleCountsFollowDrawEligibility(t *testing.T) {
	p := item("draw", "raffle", "direct")
	p.Raffle = &RafflePlan{Mode: "scheduled", DrawAt: time.Date(2026, 11, 20, 9, 0, 17, 0, time.UTC), Prize: "本期奖池", Winners: 2, Tiers: []RaffleTier{{Prize: "年册", Winners: 1}, {Prize: "明信片", Winners: 1}}}
	f := newFixture(t, p)
	ctx := t.Context()
	counts := func() *RaffleRoundStats {
		t.Helper()
		overview, err := f.s.StorefrontOverview(ctx)
		if err != nil || !overview.ServerTime.Equal(f.now) || overview.RaffleRounds[p.Key] == nil {
			t.Fatal(overview, err)
		}
		return overview.RaffleRounds[p.Key]
	}
	f.create(t, p.Key, 1, nil) // An unpaid order has no draw entries.
	held := f.pay(t, f.create(t, p.Key, 2, func(in *Purchase) { in.Quantity = 3 }), 0)
	withdrawn := f.pay(t, f.create(t, p.Key, 3, nil), 0)
	for _, order := range []Order{held, withdrawn} {
		if _, err := f.s.RequestRefund(ctx, order.UserID, order.ID, "审核申请"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.ReviewRefund(ctx, withdrawn.ID, true, "同意"); err != nil {
		t.Fatal(err)
	}
	if c := counts(); c.Entries != 3 || c.Drawn || c.Winners != 0 {
		t.Fatal(c)
	}
	f.now = p.Raffle.DrawAt
	if _, err := f.s.Quote(ctx, QuoteInput{Product: p.Key, Quantity: 1}); !errors.Is(err, ErrRaffleClosed) || !errors.Is(err, ErrStock) {
		t.Fatal("cutoff", err)
	}
	if c := counts(); c.Drawn {
		t.Fatal("published before drawing", c)
	}
	if err := f.s.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReviewRefund(ctx, held.ID, true, "同意"); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	c := counts()
	if !c.Drawn || c.Entries != 3 || c.Winners != 2 || c.ByPrize["年册"] != 1 || c.ByPrize["明信片"] != 1 {
		t.Fatal(c)
	}
	// A new configuration at the same timestamp forms its own round.
	catalog, err := f.s.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tiers := catalog.Products[0].Raffle.Tiers
	tiers[0], tiers[1] = tiers[1], tiers[0]
	catalog, err = f.s.SaveCatalog(ctx, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if c := counts(); c.Entries != 0 || c.Winners != 0 || c.Drawn {
		t.Fatalf("mixed rounds: %+v", c)
	}
	catalog.Products[0].Enabled = false
	if _, err = f.s.SaveCatalog(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	overview, err := f.s.StorefrontOverview(ctx)
	if err != nil || len(overview.RaffleRounds) != 0 || len(overview.Catalog.Products) != 0 {
		t.Fatal(overview, err)
	}
}
