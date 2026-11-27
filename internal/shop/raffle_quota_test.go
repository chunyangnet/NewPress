package shop

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func quotaRaffle(key string) Product {
	p := item(key, "raffle", "direct")
	p.Stock = -1
	p.Raffle = &RafflePlan{Mode: "instant", Prize: "礼物", Winners: 1, WinBPS: 10000}
	return p
}

func saveQuotaCatalog(t *testing.T, f *fixture, edit func(*Catalog)) Catalog {
	t.Helper()
	c, err := f.s.Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	edit(&c)
	c, err = f.s.SaveCatalog(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func assertRaffleQuota(t *testing.T, f *fixture, user int64, key string, want RaffleQuota) {
	t.Helper()
	quotas, err := f.s.PersonalRaffleQuotas(t.Context(), user)
	if err != nil || quotas[key] != want {
		t.Fatalf("quota user=%d product=%s: got %+v err=%v want %+v", user, key, quotas[key], err, want)
	}
}

func TestRaffleQuotaCombinationAndSplitOrders(t *testing.T) {
	f := newFixture(t, quotaRaffle("a"), quotaRaffle("b"))
	saveQuotaCatalog(t, f, func(c *Catalog) {
		c.RaffleCampaigns = []RaffleCampaign{{Key: "weekend", Title: "周末抽奖", TicketLimit: 500, UserLimit: 5}}
		for i := range c.Products {
			c.Products[i].Raffle.Campaign = "weekend"
		}
		c.Products[0].Raffle.TicketLimit, c.Products[0].Raffle.UserLimit, c.Products[0].Raffle.OrderLimit = 100, 2, 1
		c.Products[1].Raffle.TicketLimit, c.Products[1].Raffle.OrderLimit = 200, 2
	})
	quote := func(key string, user int64, qty int, want error) {
		t.Helper()
		_, err := f.s.QuoteFor(t.Context(), user, QuoteInput{Product: key, Quantity: qty})
		if !errors.Is(err, want) {
			t.Fatalf("quote %s/%d/%d: %v want %v", key, user, qty, err, want)
		}
	}
	quote("a", 1, 2, ErrRaffleOrderLimit)
	first := f.create(t, "a", 1, nil)
	f.create(t, "a", 1, nil)
	quote("a", 1, 1, ErrRaffleUserLimit)
	quote("a", 2, 1, nil)
	quote("a", 0, 1, nil)
	f.create(t, "b", 1, func(in *Purchase) { in.Quantity = 2 })
	f.create(t, "b", 1, nil)
	quote("b", 1, 1, ErrRaffleUserLimit)
	assertRaffleQuota(t, f, 1, "a", RaffleQuota{98, 495, 0, 0})
	assertRaffleQuota(t, f, 1, "b", RaffleQuota{197, 495, -1, 0})
	assertRaffleQuota(t, f, 2, "a", RaffleQuota{98, 495, 2, 5})
	// Cancelling an unpaid reservation restores both personal scopes.
	if _, err := f.s.Cancel(t.Context(), 1, first.ID); err != nil {
		t.Fatal(err)
	}
	quote("a", 1, 1, nil)
	assertRaffleQuota(t, f, 1, "a", RaffleQuota{99, 496, 1, 1})
}

func TestRaffleQuotaTotalAndIndependentProducts(t *testing.T) {
	f := newFixture(t, quotaRaffle("a"), quotaRaffle("b"), quotaRaffle("independent"))
	saveQuotaCatalog(t, f, func(c *Catalog) {
		c.RaffleCampaigns = []RaffleCampaign{{Key: "shared", Title: "共享活动", TicketLimit: 3}}
		c.Products[0].Raffle.Campaign, c.Products[1].Raffle.Campaign = "shared", "shared"
		c.Products[0].Raffle.TicketLimit = 2
	})
	f.pay(t, f.create(t, "a", 1, func(in *Purchase) { in.Quantity = 2 }), 0)
	if _, err := f.s.Quote(t.Context(), QuoteInput{Product: "a", Quantity: 1}); !errors.Is(err, ErrRaffleTotal) {
		t.Fatal("product allocation exceeded", err)
	}
	// Previous activity orders still count after a product is removed.
	saveQuotaCatalog(t, f, func(c *Catalog) { c.Products = c.Products[1:] })
	assertRaffleQuota(t, f, 1, "b", RaffleQuota{-1, 1, -1, -1})
	f.create(t, "b", 2, nil)
	if _, err := f.s.Quote(t.Context(), QuoteInput{Product: "b", Quantity: 1}); !errors.Is(err, ErrRaffleTotal) {
		t.Fatal("shared allocation exceeded", err)
	}
	if _, err := f.s.Quote(t.Context(), QuoteInput{Product: "independent", Quantity: 99}); err != nil {
		t.Fatal("independent product incorrectly limited", err)
	}
	// Starting a new campaign isolates new purchases from historical usage.
	saveQuotaCatalog(t, f, func(c *Catalog) {
		c.RaffleCampaigns = append(c.RaffleCampaigns, RaffleCampaign{Key: "next", Title: "新活动", TicketLimit: 2, UserLimit: 1})
		c.Products[0].Raffle.Campaign = "next"
	})
	assertRaffleQuota(t, f, 2, "b", RaffleQuota{-1, 2, -1, 1})
	f.create(t, "b", 2, nil)
	if _, err := f.s.QuoteFor(t.Context(), 2, QuoteInput{Product: "b", Quantity: 1}); !errors.Is(err, ErrRaffleUserLimit) {
		t.Fatal("new campaign per-person cap exceeded", err)
	}
}

func TestRaffleQuotaExpiryAndIssuedRefund(t *testing.T) {
	p := quotaRaffle("draw")
	p.Raffle.Mode, p.Raffle.DrawAt = "scheduled", time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	p.Raffle.TicketLimit, p.Raffle.UserLimit = 1, 1
	f := newFixture(t, p)
	unpaid := f.create(t, p.Key, 1, nil)
	assertRaffleQuota(t, f, 1, p.Key, RaffleQuota{0, -1, 0, -1})
	f.now = unpaid.ExpiresAt
	assertRaffleQuota(t, f, 1, p.Key, RaffleQuota{1, -1, 1, -1})
	// Capacity is usable at expiry even before the periodic advance job.
	paid := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	if _, err := f.s.RequestRefund(t.Context(), 1, paid.ID, "申请退款"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReviewRefund(t.Context(), paid.ID, true, "同意退款"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReconcileRefund(t.Context(), Refund{OrderID: paid.ID, Reference: "verified-refund", Note: "渠道已退回"}); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	assertRaffleQuota(t, f, 1, p.Key, RaffleQuota{0, -1, 0, -1})
	if _, err := f.s.QuoteFor(t.Context(), 1, QuoteInput{Product: p.Key, Quantity: 1}); !errors.Is(err, ErrRaffleTotal) {
		t.Fatal("issued/refunded ticket freed allocation", err)
	}
}

func TestRaffleQuotaRetriesAndCapacityEditsKeepDraw(t *testing.T) {
	p := quotaRaffle("draw")
	p.Raffle.Mode, p.Raffle.DrawAt = "scheduled", time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	p.Raffle.TicketLimit = 1
	f := newFixture(t, p)
	c, _ := f.s.Catalog(t.Context())
	in := Purchase{QuoteInput: QuoteInput{Product: p.Key, Quantity: 1}, UserID: 1, Version: c.Version, RequestID: "recover-one", Accepted: true}
	order, err := f.s.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	paid := f.pay(t, order, 0)
	round := paid.Tickets[0].Round
	saveQuotaCatalog(t, f, func(c *Catalog) {
		c.Products[0].Raffle.TicketLimit, c.Products[0].Raffle.UserLimit, c.Products[0].Raffle.OrderLimit = 2, 2, 1
	})
	other := f.pay(t, f.create(t, p.Key, 2, nil), 0)
	if other.Tickets[0].Round != round {
		t.Fatal("capacity edit split draw identity")
	}
	f.now = p.Raffle.DrawAt
	if err := f.s.Advance(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Recover the exact original request even after quota exhaustion and cutoff.
	recovered, err := f.s.Create(t.Context(), in)
	if err != nil || recovered.ID != order.ID {
		t.Fatal("idempotent recovery denied", recovered.ID, err)
	}
	overview, err := f.s.StorefrontOverview(t.Context())
	if err != nil || overview.RaffleRounds[p.Key].Entries != 2 || overview.RaffleRounds[p.Key].Winners != 1 {
		t.Fatal("split or repeated draw", overview, err)
	}
}

func TestRaffleQuotaConcurrentBuyers(t *testing.T) {
	for _, personal := range []bool{false, true} {
		t.Run(fmt.Sprintf("personal=%t", personal), func(t *testing.T) {
			f := newFixture(t, quotaRaffle("a"), quotaRaffle("b"))
			saveQuotaCatalog(t, f, func(c *Catalog) {
				campaign := RaffleCampaign{Key: "shared", Title: "共享活动", TicketLimit: 4}
				if personal {
					campaign.TicketLimit, campaign.UserLimit = 100, 2
				}
				c.RaffleCampaigns = []RaffleCampaign{campaign}
				for i := range c.Products {
					c.Products[i].Raffle.Campaign = campaign.Key
				}
			})
			var wg sync.WaitGroup
			results := make(chan error, 12)
			for i := range 12 {
				wg.Go(func() {
					user := int64(i + 1)
					if personal {
						user = 1
					}
					key := []string{"a", "b"}[i%2]
					for range 30 {
						c, err := f.s.Catalog(t.Context())
						if err != nil {
							results <- err
							return
						}
						_, err = f.s.Create(t.Context(), Purchase{QuoteInput: QuoteInput{Product: key, Quantity: 1}, UserID: user, Version: c.Version, RequestID: fmt.Sprintf("buyer-%d", i), Accepted: true})
						if errors.Is(err, ErrConflict) {
							continue
						}
						results <- err
						return
					}
					results <- fmt.Errorf("conflicts never resolved")
				})
			}
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				} else if !errors.Is(err, ErrRaffleTotal) && !errors.Is(err, ErrRaffleUserLimit) {
					t.Fatal(err)
				}
			}
			want := 4
			if personal {
				want = 2
			}
			if success != want {
				t.Fatalf("concurrent allowance got=%d want=%d", success, want)
			}
		})
	}
}

func TestRaffleQuotaValidation(t *testing.T) {
	c := InitialState().Catalog
	c.Products = []Product{quotaRaffle("a")}
	for _, cap := range []int{-1, 1_000_000_001} {
		c.Products[0].Raffle.TicketLimit = cap
		if !errors.Is(ValidateCatalog(c), ErrInvalid) {
			t.Fatal("invalid cap accepted", cap)
		}
	}
	c.Products[0].Raffle.TicketLimit = 0
	c.Products[0].Raffle.OrderLimit = 100
	if !errors.Is(ValidateCatalog(c), ErrInvalid) {
		t.Fatal("per-order cap over 99 accepted")
	}
	c.Products[0].Raffle.OrderLimit = 0
	c.Products[0].Raffle.Campaign = "missing"
	if !errors.Is(ValidateCatalog(c), ErrInvalid) {
		t.Fatal("unconfigured campaign accepted")
	}
	c.RaffleCampaigns = []RaffleCampaign{{Key: "missing", Title: "不限活动"}}
	if err := ValidateCatalog(c); err != nil {
		t.Fatal("unlimited campaign rejected", err)
	}
	c.RaffleCampaigns = append(c.RaffleCampaigns, c.RaffleCampaigns[0])
	if !errors.Is(ValidateCatalog(c), ErrInvalid) {
		t.Fatal("duplicate campaigns accepted")
	}
}
