package shop

import (
	"context"
	"crypto/rand"
	"log/slog"
	"math/big"
	"sort"
	"strings"
	"time"
)

func (s *Service) MarkPaid(ctx context.Context, p PaymentReceipt) (Order, error) {
	var out Order
	err := s.Backend.Update(ctx, func(st *State) error {
		o, ok := st.Orders[p.OrderID]
		if !ok {
			return ErrNotFound
		}
		if !validPayment(o, p) {
			return ErrInvalid
		}
		if old, ok := st.Receipts[p.Reference]; ok {
			if old != p {
				return ErrReused
			}
			out = o
			return nil
		}
		if !o.Schedule[p.Index].PaidAt.IsZero() {
			return ErrReused
		}
		now := s.now()
		st.Receipts[p.Reference] = p
		o.Schedule[p.Index].PaidAt = now
		o.Schedule[p.Index].PaymentRef = p.Reference
		o.PaidAmount += p.Amount
		useCoupon(st, o, now)
		o.UpdatedAt = now
		late := member(o.Status, "cancelled", "expired", "refunded", "refund_required", "refund_requested") || o.Released || p.Index == 0 && !now.Before(o.ExpiresAt)
		if p.Index > 0 {
			for i := 0; i < p.Index; i++ {
				if o.Schedule[i].PaidAt.IsZero() {
					late = true
				}
			}
		}
		if o.Mode == "deposit" && p.Index > 0 && (now.Before(o.Snapshot.ReleaseAt) || !now.Before(o.Snapshot.BalanceDueAt)) {
			late = true
		}
		if o.Mode == "installment" && p.Index > 0 && !now.Before(o.Schedule[p.Index].DueAt.Add(24*time.Hour)) {
			late = true
		}
		if o.Kind == "raffle" && o.Snapshot.Raffle.Mode == "scheduled" && !now.Before(o.Snapshot.Raffle.DrawAt) {
			late = true
		}
		if o.Mode == "group" {
			g := st.Groups[o.GroupID]
			if g.Status != "open" || !now.Before(g.ExpiresAt) {
				late = true
			}
		}
		if late {
			o.Status = "refund_required"
			releaseStock(st, &o)
			st.Orders[o.ID] = o
			out = o
			return nil
		}
		if o.PaidAmount < o.Total {
			o.Status = "partially_paid"
			st.Orders[o.ID] = o
			out = o
			return nil
		}
		if o.Mode == "group" {
			o.Status = "awaiting_group"
			st.Orders[o.ID] = o
			g := st.Groups[o.GroupID]
			if e := advanceGroup(st, &g, now); e != nil {
				return e
			}
			st.Groups[g.ID] = g
			out = st.Orders[o.ID]
			return nil
		}
		if e := fulfill(st, &o, now); e != nil {
			return e
		}
		st.Orders[o.ID] = o
		out = o
		return nil
	})
	return out, err
}
func randomBelow(n int) (int, error) {
	v, e := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if e != nil {
		return 0, e
	}
	return int(v.Int64()), nil
}

// slot is the zero-based roll (instant) or shuffled winner rank (scheduled).
func (r *RafflePlan) prizeAt(slot int) string {
	for _, tier := range r.Tiers {
		width := tier.Winners
		if r.Mode == "instant" {
			width = tier.WinBPS
		}
		if slot < width {
			return tier.Prize
		}
		slot -= width
	}
	return r.Prize
}

func fulfill(st *State, o *Order, now time.Time) error {
	if o.PaidAmount != o.Total || o.Released {
		return ErrState
	}
	if member(o.Mode, "presale", "deposit") && now.Before(o.Snapshot.ReleaseAt) {
		o.Status = "awaiting_release"
		o.UpdatedAt = now
		return nil
	}
	if len(o.Deliveries) > 0 || len(o.Tickets) > 0 {
		return nil
	}
	if o.Kind == "raffle" {
		r := o.Snapshot.Raffle
		round := r.roundID(o.Product)
		for i := 0; i < o.Quantity; i++ {
			ticket := Ticket{ID: newID("st_"), OrderID: o.ID, Round: round, Status: "pending", Prize: r.Prize, DrawAt: r.DrawAt, Winners: r.Winners}
			if r.Mode == "instant" {
				n, e := randomBelow(10000)
				if e != nil {
					return e
				}
				ticket.Status = "lost"
				if n < r.WinBPS {
					ticket.Status = "won"
					ticket.Prize = r.prizeAt(n)
				}
				ticket.DrawAt = now
				ticket.DrawnAt = now
			}
			o.Tickets = append(o.Tickets, ticket)
		}
		o.Status = "awaiting_draw"
		if r.Mode == "instant" {
			o.Status = "completed"
			o.CompletedAt = now
		}
		o.UpdatedAt = now
		return nil
	}
	physical := false
	for _, r := range o.Reservations {
		if r.Kind == "bundle" {
			continue
		}
		if r.Kind == "physical" {
			physical = true
			continue
		}
		content := r.Delivery
		if r.Kind == "cdk" {
			content = strings.Join(r.Codes, "\n")
		}
		if r.Kind == "donation" {
			content = "捐赠付款已确认。"
		}
		o.Deliveries = append(o.Deliveries, Delivery{Product: r.Product, Kind: r.Kind, Title: r.Title, Content: content, Quantity: r.Quantity})
	}
	o.Status = "completed"
	o.CompletedAt = now
	if physical {
		o.Status = "awaiting_shipment"
		o.CompletedAt = time.Time{}
	}
	o.UpdatedAt = now
	return nil
}

func advanceGroup(st *State, g *Group, now time.Time) error {
	if g.Status != "open" {
		return nil
	}
	g.Paid = 0
	for _, id := range g.Orders {
		o := st.Orders[id]
		if o.Status == "awaiting_group" && o.PaidAmount == o.Total && !o.Released {
			g.Paid++
		}
	}
	if !now.Before(g.ExpiresAt) {
		g.Status = "failed"
		for _, id := range g.Orders {
			o := st.Orders[id]
			if !member(o.Status, "pending", "awaiting_group") {
				continue
			}
			o.Status = "refund_required"
			if o.PaidAmount == 0 {
				o.Status = "expired"
				releaseCoupon(st, o)
			}
			releaseStock(st, &o)
			o.UpdatedAt = now
			st.Orders[id] = o
		}
	} else if g.Paid == g.Size {
		g.Status = "completed"
		for _, id := range g.Orders {
			o := st.Orders[id]
			if o.Status != "awaiting_group" || o.PaidAmount != o.Total || o.Released {
				continue
			}
			if err := fulfill(st, &o, now); err != nil {
				return err
			}
			st.Orders[id] = o
		}
	}
	return nil
}

func (s *Service) Advance(ctx context.Context) error {
	err := s.Backend.Update(ctx, func(st *State) error {
		now := s.now()
		for id, o := range st.Orders {
			expired := o.Status == "pending" && !now.Before(o.ExpiresAt)
			overdue := false
			if o.Status == "partially_paid" {
				if o.Mode == "deposit" {
					overdue = !now.Before(o.Snapshot.BalanceDueAt)
				} else {
					for _, part := range o.Schedule {
						if part.PaidAt.IsZero() {
							overdue = !now.Before(part.DueAt.Add(24 * time.Hour))
							break
						}
					}
				}
			}
			if expired || overdue {
				if o.PaidAmount > 0 {
					o.Status = "refund_required"
				} else {
					o.Status = "expired"
					releaseCoupon(st, o)
				}
				releaseStock(st, &o)
				o.UpdatedAt = now
				st.Orders[id] = o
			}
			if o.Status == "awaiting_release" && !now.Before(o.Snapshot.ReleaseAt) {
				if e := fulfill(st, &o, now); e != nil {
					return e
				}
				st.Orders[id] = o
			}
		}
		for id, g := range st.Groups {
			if g.Status != "open" {
				continue
			}
			if e := advanceGroup(st, &g, now); e != nil {
				return e
			}
			st.Groups[id] = g
		}
		type entry struct {
			id    string
			index int
		}
		rounds := map[string][]entry{}
		drawn := map[string]bool{}
		for id, o := range st.Orders {
			eligible := awaitingRaffleDraw(o)
			for i, t := range o.Tickets {
				if !t.DrawnAt.IsZero() {
					drawn[t.Round] = true
				}
				if eligible && t.Status == "pending" && !now.Before(t.DrawAt) {
					rounds[t.Round] = append(rounds[t.Round], entry{id, i})
				}
			}
		}
		for round, entries := range rounds {
			// Stable ordering keeps the audited input set independent of map iteration.
			sort.Slice(entries, func(i, j int) bool {
				a, b := entries[i], entries[j]
				return a.id < b.id || a.id == b.id && a.index < b.index
			})
			winners := st.Orders[entries[0].id].Tickets[entries[0].index].Winners
			// Any persisted result seals the round, including results on
			// refunded orders. Held tickets never create extra prizes.
			if drawn[round] {
				winners = 0
			}
			if winners > len(entries) {
				winners = len(entries)
			}
			for i := len(entries) - 1; i > 0; i-- {
				j, e := randomBelow(i + 1)
				if e != nil {
					return e
				}
				entries[i], entries[j] = entries[j], entries[i]
			}
			for i, entry := range entries {
				o := st.Orders[entry.id]
				t := &o.Tickets[entry.index]
				t.Status = "lost"
				if i < winners {
					t.Status = "won"
					t.Prize = o.Snapshot.Raffle.prizeAt(i)
				}
				t.DrawnAt = now
				o.UpdatedAt = now
				st.Orders[o.ID] = o
			}
		}
		for id, o := range st.Orders {
			if o.Status == "awaiting_draw" && len(o.Tickets) > 0 {
				done := true
				for _, t := range o.Tickets {
					done = done && t.Status != "pending"
				}
				if done {
					o.Status = "completed"
					o.CompletedAt = now
					o.UpdatedAt = now
					st.Orders[id] = o
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Ledger work runs after the aggregate commits and releases its lock.
	return s.RecoverWallet(ctx)
}
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		operation, cancel := context.WithTimeout(ctx, 30*time.Second)
		e := s.Advance(operation)
		cancel()
		if e != nil && ctx.Err() == nil {
			slog.Error("advance shop orders failed", "error", e)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
