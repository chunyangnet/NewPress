package shop

import (
	"reflect"
	"testing"
	"time"
)

func TestTieredRaffleInstantBoundaries(t *testing.T) {
	plan := RafflePlan{Mode: "instant", Prize: "奖池", Winners: 1, WinBPS: 8000, Tiers: []RaffleTier{{Prize: "年册", WinBPS: 125}, {Prize: "明信片", WinBPS: 7875}}}
	if !validRafflePlan(&plan) {
		t.Fatal("valid pool rejected")
	}
	for slot, want := range map[int]string{0: "年册", 124: "年册", 125: "明信片", 7999: "明信片"} {
		if got := plan.prizeAt(slot); got != want {
			t.Errorf("slot %d: got %s, want %s", slot, got, want)
		}
	}
	for _, edit := range []func(*RafflePlan){
		func(r *RafflePlan) { r.WinBPS++ },
		func(r *RafflePlan) { r.Tiers[1].Prize = r.Tiers[0].Prize },
		func(r *RafflePlan) { r.Tiers[0].Winners = 1 },
		func(r *RafflePlan) { r.Tiers[0].WinBPS = 0 },
		func(r *RafflePlan) { r.WinBPS = 10125; r.Tiers[1].WinBPS = 10000 },
	} {
		r := plan
		r.Tiers = append([]RaffleTier(nil), plan.Tiers...)
		edit(&r)
		if validRafflePlan(&r) {
			t.Fatalf("accepted invalid pool: %+v", r)
		}
	}
	p := item("instant-tier", "raffle", "direct")
	plan.WinBPS = 10000
	plan.Tiers[1].WinBPS = 9875
	p.Raffle = &plan
	f := newFixture(t, p)
	o := f.pay(t, f.create(t, p.Key, 1, nil), 0)
	if ticket := o.Tickets[0]; ticket.Status != "won" || (ticket.Prize != "年册" && ticket.Prize != "明信片") {
		t.Fatal(ticket)
	}
}

func TestTieredRaffleScheduledQuotasPersist(t *testing.T) {
	for _, tc := range []struct {
		name     string
		quantity int
		want     map[string]int
	}{
		{"oversubscribed", 5, map[string]int{"年册": 1, "明信片": 2}},
		{"undersubscribed", 2, map[string]int{"年册": 1, "明信片": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := item("prizes", "raffle", "direct")
			p.Raffle = &RafflePlan{Mode: "scheduled", DrawAt: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), Prize: "活动奖池", Winners: 3, Tiers: []RaffleTier{{Prize: "年册", Winners: 1}, {Prize: "明信片", Winners: 2}}}
			f := newFixture(t, p)
			o := f.pay(t, f.create(t, p.Key, 1, func(in *Purchase) { in.Quantity = tc.quantity }), 0)
			f.now = p.Raffle.DrawAt
			if err := f.s.Advance(t.Context()); err != nil {
				t.Fatal(err)
			}
			o, err := f.s.Order(t.Context(), 1, o.ID)
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, ticket := range o.Tickets {
				switch ticket.Status {
				case "won":
					counts[ticket.Prize]++
				case "lost":
				default:
					t.Fatalf("unexpected status %s", ticket.Status)
				}
			}
			if len(o.Tickets) != tc.quantity || !reflect.DeepEqual(counts, tc.want) {
				t.Fatalf("tickets=%d, awards=%v", len(o.Tickets), counts)
			}
			f.restart(t)
			if err := f.s.Advance(t.Context()); err != nil {
				t.Fatal(err)
			}
			after, err := f.s.Order(t.Context(), 1, o.ID)
			if err != nil || !reflect.DeepEqual(after.Tickets, o.Tickets) {
				t.Fatal("persisted tickets changed", err)
			}
		})
	}
}
