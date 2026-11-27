package shop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

func requestKey(user int64, key string) string { return strconv.FormatInt(user, 10) + ":" + key }
func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func matchesPurchaseRequest(o Order, in Purchase, fp string) bool {
	if o.Fingerprint == fp {
		return true
	}
	// Donation requests contain an amount. The precision upgrade keeps their
	// original fingerprint, so verify the original terms before accepting tenths.
	if o.Kind != "donation" || o.Currency != "wallet:main" || o.Precision != 1 || in.DonationAmount != o.UnitPrice || in.DonationAmount <= 0 || in.DonationAmount%10 != 0 {
		return false
	}
	in.DonationAmount /= 10
	return o.Fingerprint == fingerprint(in)
}

// Stock and activation codes vary with reservations; the other product terms
// must agree before different buyers can join the same group.
func groupTerms(p Product) string {
	p.Stock = 0
	p.Codes = nil
	p.Enabled = false
	p.Description = ""
	p.DescriptionSource = ""
	p.DescriptionAuthorID = 0
	p.Services = nil
	return fingerprint(p)
}
func (s *Service) Ready(ctx context.Context) error {
	if s == nil || s.Backend == nil {
		return ErrUnavailable
	}
	st, e := s.Backend.Read(ctx)
	if e != nil {
		return e
	}
	return ValidateState(st)
}
func (s *Service) Catalog(ctx context.Context) (Catalog, error) {
	st, e := s.Backend.Read(ctx)
	return st.Catalog, e
}
func (s *Service) SaveCatalog(ctx context.Context, in Catalog) (Catalog, error) {
	if e := ValidateCatalog(in); e != nil {
		return Catalog{}, e
	}
	if e := s.validateCurrencies(ctx, &in); e != nil {
		return Catalog{}, e
	}
	err := s.Backend.Update(ctx, func(st *State) error {
		if in.Version != st.Catalog.Version {
			return ErrConflict
		}
		// Allow all existing reservations to return without exceeding catalog limits.
		reserved, variants, codes := map[string]int64{}, map[string]int64{}, map[string]int{}
		for _, o := range st.Orders {
			if o.Released || len(o.Deliveries) > 0 || len(o.Tickets) > 0 || !o.ShippedAt.IsZero() {
				continue
			}
			for _, r := range o.Reservations {
				reserved[r.Product] += int64(r.Quantity)
				variants[r.Product+":"+r.Variant] += int64(r.Quantity)
				codes[r.Product] += len(r.Codes)
			}
		}
		for _, p := range in.Products {
			if p.Stock >= 0 && p.Stock+reserved[p.Key] > 1_000_000_000 || len(p.Codes)+codes[p.Key] > 10000 {
				return ErrInvalid
			}
			for _, v := range p.Variants {
				if v.Stock >= 0 && v.Stock+variants[p.Key+":"+v.Key] > 1_000_000_000 {
					return ErrInvalid
				}
			}
		}
		// Reservations retain fulfillment snapshots. Prevent removal/retyping while stock can return.
		for _, o := range st.Orders {
			if o.Released || member(o.Status, "completed", "shipped", "refunded") {
				continue
			}
			for _, r := range o.Reservations {
				p, ok := product(in, r.Product)
				if !ok || p.Kind != r.Kind {
					return ErrState
				}
				if r.Variant != "" {
					if _, ok = variant(p, r.Variant); !ok {
						return ErrState
					}
				}
			}
		}
		in.Version++
		st.Catalog = in
		return nil
	})
	return in, err
}
func product(c Catalog, key string) (Product, bool) {
	for _, p := range c.Products {
		if p.Key == key {
			return p, true
		}
	}
	return Product{}, false
}
func variant(p Product, key string) (Variant, bool) {
	for _, v := range p.Variants {
		if v.Key == key {
			return v, true
		}
	}
	return Variant{}, false
}
func stockEnough(stock int64, n int) bool { return stock == -1 || stock >= int64(n) }
func (s *Service) Quote(ctx context.Context, in QuoteInput) (Quote, error) {
	st, e := s.Backend.Read(ctx)
	if e != nil {
		return Quote{}, e
	}
	q, _, e := makeQuote(st, in, 0, s.now())
	return q, e
}
func makeQuote(st State, in QuoteInput, user int64, now time.Time) (Quote, Product, error) {
	fail := func(e error) (Quote, Product, error) { return Quote{}, Product{}, e }
	if !st.Catalog.Enabled {
		return fail(ErrDisabled)
	}
	p, ok := product(st.Catalog, in.Product)
	if !ok || !p.Enabled {
		return fail(ErrNotFound)
	}
	if in.Quantity < 1 || in.Quantity > 99 {
		return fail(ErrInvalid)
	}
	mode := in.Mode
	if mode == "" {
		mode = p.Mode
	}
	if mode != p.Mode && !(mode == "direct" && member(p.Mode, "group", "installment")) {
		return fail(ErrInvalid)
	}
	if mode != "group" && in.GroupID != "" || mode != "installment" && in.Periods != 0 || p.Kind != "donation" && in.DonationAmount != 0 {
		return fail(ErrInvalid)
	}
	unit := p.Price
	if len(p.Variants) > 0 {
		v, found := variant(p, in.Variant)
		if !found || !stockEnough(v.Stock, in.Quantity) {
			return fail(ErrStock)
		}
		unit = v.Price
	} else if in.Variant != "" {
		return fail(ErrInvalid)
	}
	if p.Kind == "raffle" && p.Raffle.Mode == "scheduled" && !now.Before(p.Raffle.DrawAt) {
		return fail(ErrRaffleClosed)
	}
	if p.Kind == "raffle" {
		if p.Raffle.OrderLimit > 0 && in.Quantity > p.Raffle.OrderLimit {
			return fail(ErrRaffleOrderLimit)
		}
		if quota, ok := raffleQuotas(st, user, p.Key, now)[p.Key]; ok {
			if !stockEnough(int64(quota.ProductLeft), in.Quantity) || !stockEnough(int64(quota.CampaignLeft), in.Quantity) {
				return fail(ErrRaffleTotal)
			}
			if !stockEnough(int64(quota.UserProductLeft), in.Quantity) || !stockEnough(int64(quota.UserCampaignLeft), in.Quantity) {
				return fail(ErrRaffleUserLimit)
			}
		}
	}
	if !stockEnough(p.Stock, in.Quantity) {
		return fail(ErrStock)
	}
	if p.Kind == "donation" {
		if in.Quantity != 1 || in.DonationAmount < p.Price || in.DonationAmount > p.MaxDonation {
			return fail(ErrInvalid)
		}
		unit = in.DonationAmount
	}
	if mode == "deposit" && !now.Before(p.ReleaseAt) {
		return fail(ErrStock)
	}
	if mode == "group" {
		if in.Quantity != 1 {
			return fail(ErrInvalid)
		}
		unit = p.Group.Price
		if in.GroupID != "" {
			g, found := st.Groups[in.GroupID]
			if !found || g.Status != "open" || !now.Before(g.ExpiresAt) || g.Product != p.Key || g.Variant != in.Variant || g.Size != p.Group.Size {
				return fail(ErrState)
			}
			live := 0
			for _, id := range g.Orders {
				o := st.Orders[id]
				if !member(o.Status, "cancelled", "expired", "refunded", "refund_required") {
					live++
					if o.UnitPrice != unit || groupTerms(o.Snapshot) != groupTerms(p) || o.Terms != st.Catalog.Terms || o.RefundPolicy != st.Catalog.RefundPolicy {
						return fail(ErrConflict)
					}
				}
			}
			if live >= g.Size {
				return fail(ErrStock)
			}
		}
	}
	if unit > MaxAmount/int64(in.Quantity) {
		return fail(ErrInvalid)
	}
	precision := p.Precision
	if p.Currency == "CNY" {
		precision = 2
	}
	q := Quote{Version: st.Catalog.Version, Product: p.Key, Variant: in.Variant, Quantity: in.Quantity, Kind: p.Kind, Mode: mode, Currency: p.Currency, Precision: precision, UnitPrice: unit, OriginalSubtotal: unit * int64(in.Quantity), Subtotal: unit * int64(in.Quantity), RequiresAddress: p.Kind == "physical"}
	if p.Kind == "bundle" {
		for _, b := range p.Bundle {
			child, exists := product(st.Catalog, b.Product)
			if !exists || !child.Enabled || !stockEnough(child.Stock, b.Quantity*in.Quantity) || child.Kind == "cdk" && len(child.Codes) < b.Quantity*in.Quantity {
				return fail(ErrStock)
			}
			q.RequiresAddress = q.RequiresAddress || child.Kind == "physical"
		}
	}
	if p.Kind == "cdk" && len(p.Codes) < in.Quantity {
		return fail(ErrStock)
	}
	if q.RequiresAddress {
		q.Shipping = p.ShippingFee
	}
	if q.Subtotal > MaxAmount-q.Shipping {
		return fail(ErrInvalid)
	}
	principal := q.Subtotal + q.Shipping
	switch mode {
	case "deposit":
		first := p.Deposit*int64(in.Quantity) + q.Shipping
		q.Schedule = []Installment{{Index: 0, Title: "定金", Principal: first, Amount: first, DueAt: now}, {Index: 1, Title: "尾款", Principal: principal - first, Amount: principal - first, DueAt: p.BalanceDueAt, AvailableAt: p.ReleaseAt}}
	case "installment":
		plan := p.Installment
		found := false
		for _, n := range plan.Periods {
			if n == in.Periods {
				found = true
			}
		}
		if !found || q.Subtotal < int64(in.Periods) {
			return fail(ErrInvalid)
		}
		fee := (q.Subtotal*int64(plan.InterestBPS) + 9999) / 10000
		if fee > (MaxAmount-principal)/int64(in.Periods) {
			return fail(ErrInvalid)
		}
		q.Interest = fee * int64(in.Periods)
		q.InterestDescription = "每期利息按商品本金 × 每期费率计算，不随还款递减；每期向上取整至分。运费在首期支付，全额付清后交付。"
		for i := 0; i < in.Periods; i++ {
			part := q.Subtotal / int64(in.Periods)
			if int64(i) < q.Subtotal%int64(in.Periods) {
				part++
			}
			if i == 0 {
				part += q.Shipping
			}
			q.Schedule = append(q.Schedule, Installment{Index: i, Title: "第 " + strconv.Itoa(i+1) + " 期", Principal: part, Interest: fee, Amount: part + fee, DueAt: now.Add(time.Duration(i*plan.IntervalDays) * 24 * time.Hour)})
		}
	default:
		q.Schedule = []Installment{{Index: 0, Title: "全款", Principal: principal, Amount: principal, DueAt: now}}
	}
	q.Total = principal + q.Interest
	q.PayNow = q.Schedule[0].Amount
	return q, p, nil
}
func (s *Service) Create(ctx context.Context, in Purchase) (Order, error) {
	if in.UserID < 1 || !in.Accepted || !nameOK(in.RequestID, 128) {
		return Order{}, ErrInvalid
	}
	fpInput := in
	fpInput.RequestID = ""
	fp := fingerprint(fpInput)
	var out Order
	err := s.Backend.Update(ctx, func(st *State) error {
		for _, o := range st.Orders {
			if o.UserID == in.UserID && o.RequestID == in.RequestID {
				if !matchesPurchaseRequest(o, fpInput, fp) {
					return ErrReused
				}
				out = o
				return nil
			}
		}
		if st.Catalog.Version != in.Version {
			return ErrConflict
		}
		now := s.now()
		q, p, e := makeQuote(*st, in.QuoteInput, in.UserID, now)
		if e != nil {
			return e
		}
		if strings.TrimSpace(in.Coupon) != "" {
			coupon, ok := couponByCode(*st, in.Coupon)
			if !ok || !couponApplies(coupon, p, q, now) {
				return ErrIneligible
			}
			if _, ok = usableClaim(*st, coupon.Code, in.UserID); !ok {
				return ErrIneligible
			}
			if e = couponUserRules(ctx, s, in.UserID, coupon); e != nil {
				return e
			}
			if e = couponUseCapacity(*st, coupon, in.UserID); e != nil {
				return e
			}
			q = discountQuote(q, p, coupon)
			if q.Coupon == "" {
				return ErrIneligible
			}
		}
		if q.RequiresAddress && (!nameOK(in.Address.Name, 80) || !nameOK(in.Address.Phone, 40) || !nameOK(in.Address.Detail, 500) || !textOK(in.Address.Region, 120) || !textOK(in.Address.PostalCode, 24)) {
			return ErrInvalid
		}
		count := 0
		for _, o := range st.Orders {
			if o.UserID == in.UserID && o.Status == "pending" && now.Before(o.ExpiresAt) {
				count++
			}
		}
		if count >= 10 {
			return ErrState
		}
		out = Order{ID: newID("so_"), UserID: in.UserID, RequestID: in.RequestID, Fingerprint: fp, Title: p.Title, Quote: q, Status: "pending", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Duration(st.Catalog.OrderMinutes) * time.Minute), Terms: st.Catalog.Terms, RefundPolicy: st.Catalog.RefundPolicy, CatalogVersion: in.Version, Snapshot: p}
		out.Snapshot.Codes = nil
		// The order retains fulfillment terms, not the potentially large editor source.
		out.Snapshot.DescriptionSource = ""
		out.Snapshot.DescriptionAuthorID = 0
		if q.RequiresAddress {
			out.Address = in.Address
		}
		if q.Mode == "deposit" && p.ReleaseAt.Before(out.ExpiresAt) {
			out.ExpiresAt = p.ReleaseAt
		}
		if p.Kind == "raffle" && p.Raffle.Mode == "scheduled" && p.Raffle.DrawAt.Before(out.ExpiresAt) {
			out.ExpiresAt = p.Raffle.DrawAt
		}
		if q.Mode == "group" {
			g := st.Groups[in.GroupID]
			if in.GroupID == "" {
				g = Group{ID: newID("sg_"), Product: p.Key, Variant: in.Variant, Version: in.Version, Size: p.Group.Size, Status: "open", ExpiresAt: now.Add(time.Duration(p.Group.Hours) * time.Hour)}
			}
			for _, id := range g.Orders {
				existing := st.Orders[id]
				if existing.UserID == in.UserID && !member(existing.Status, "cancelled", "expired", "refund_required", "refunded") {
					return ErrState
				}
			}
			if g.ExpiresAt.Before(out.ExpiresAt) {
				out.ExpiresAt = g.ExpiresAt
			}
			out.GroupID = g.ID
			g.Orders = append(g.Orders, out.ID)
			st.Groups[g.ID] = g
		}
		reserve := func(key, v string, n int) error {
			r, e := reserveStock(st, key, v, n)
			if e == nil {
				out.Reservations = append(out.Reservations, r)
			}
			return e
		}
		if e = reserve(p.Key, in.Variant, in.Quantity); e != nil {
			return e
		}
		if p.Kind == "bundle" {
			for _, b := range p.Bundle {
				if e = reserve(b.Product, "", b.Quantity*in.Quantity); e != nil {
					return e
				}
			}
		}
		st.Catalog.Version++
		if e = reserveCoupon(st, out, now); e != nil {
			return e
		}
		st.Orders[out.ID] = out
		return nil
	})
	return out, err
}
func reserveStock(st *State, key, v string, n int) (Reservation, error) {
	for i := range st.Catalog.Products {
		p := &st.Catalog.Products[i]
		if p.Key != key {
			continue
		}
		if !p.Enabled || !stockEnough(p.Stock, n) {
			return Reservation{}, ErrStock
		}
		if p.Stock >= 0 {
			p.Stock -= int64(n)
		}
		if v != "" {
			found := false
			for j := range p.Variants {
				if p.Variants[j].Key == v {
					found = true
					if !stockEnough(p.Variants[j].Stock, n) {
						return Reservation{}, ErrStock
					}
					if p.Variants[j].Stock >= 0 {
						p.Variants[j].Stock -= int64(n)
					}
				}
			}
			if !found {
				return Reservation{}, ErrInvalid
			}
		}
		r := Reservation{Product: key, Variant: v, Quantity: n, Kind: p.Kind, Title: p.Title, Delivery: p.Delivery}
		if p.Kind == "cdk" {
			if len(p.Codes) < n {
				return Reservation{}, ErrStock
			}
			r.Codes = append([]string(nil), p.Codes[:n]...)
			p.Codes = append([]string(nil), p.Codes[n:]...)
		}
		return r, nil
	}
	return Reservation{}, ErrNotFound
}
func releaseStock(st *State, o *Order) {
	if o.Released || len(o.Deliveries) > 0 || len(o.Tickets) > 0 || !o.ShippedAt.IsZero() {
		return
	}
	for _, r := range o.Reservations {
		for i := range st.Catalog.Products {
			p := &st.Catalog.Products[i]
			if p.Key != r.Product {
				continue
			}
			if p.Stock >= 0 {
				p.Stock += int64(r.Quantity)
			}
			for j := range p.Variants {
				if p.Variants[j].Key == r.Variant && p.Variants[j].Stock >= 0 {
					p.Variants[j].Stock += int64(r.Quantity)
				}
			}
			p.Codes = append(p.Codes, r.Codes...)
		}
	}
	o.Released = true
	st.Catalog.Version++
}

// DeliverPrize records a traceable delivery after the draw; it cannot edit a
// result, issue a prize to a losing ticket, or silently overwrite delivery.
func (s *Service) DeliverPrize(ctx context.Context, orderID, ticketID, content string) (Order, error) {
	var out Order
	if strings.TrimSpace(content) == "" || !textOK(content, 4000) {
		return out, ErrInvalid
	}
	err := s.Backend.Update(ctx, func(st *State) error {
		o, ok := st.Orders[orderID]
		if !ok {
			return ErrNotFound
		}
		if o.Kind != "raffle" || o.Status != "completed" || o.PaidAmount != o.Total {
			return ErrState
		}
		for i := range o.Tickets {
			ticket := &o.Tickets[i]
			if ticket.ID != ticketID {
				continue
			}
			if ticket.Status != "won" {
				return ErrState
			}
			if !ticket.DeliveredAt.IsZero() {
				if ticket.Delivery != content {
					return ErrConflict
				}
				out = o
				return nil
			}
			ticket.Delivery, ticket.DeliveredAt = content, s.now()
			o.UpdatedAt = s.now()
			st.Orders[o.ID] = o
			out = o
			return nil
		}
		return ErrNotFound
	})
	return out, err
}
func (s *Service) Orders(ctx context.Context, user int64) ([]Order, error) {
	st, e := s.Backend.Read(ctx)
	if e != nil {
		return nil, e
	}
	out := []Order{}
	for _, o := range st.Orders {
		if user == 0 || o.UserID == user {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
func (s *Service) Order(ctx context.Context, user int64, id string) (Order, error) {
	st, e := s.Backend.Read(ctx)
	if e != nil {
		return Order{}, e
	}
	o, ok := st.Orders[id]
	if !ok || user != 0 && o.UserID != user {
		return Order{}, ErrNotFound
	}
	return o, nil
}
func (s *Service) Groups(ctx context.Context) ([]Group, error) {
	st, e := s.Backend.Read(ctx)
	if e != nil {
		return nil, e
	}
	out := []Group{}
	for _, g := range st.Groups {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExpiresAt.Before(out[j].ExpiresAt) })
	return out, nil
}
func (s *Service) Payment(ctx context.Context, user int64, id string, index int) (PaymentRequest, error) {
	o, e := s.Order(ctx, user, id)
	if e != nil {
		return PaymentRequest{}, e
	}
	return paymentRequest(o, index, s.now())
}

func paymentRequest(o Order, index int, now time.Time) (PaymentRequest, error) {
	if o.Released || !member(o.Status, "pending", "partially_paid") || index < 0 || index >= len(o.Schedule) || !o.Schedule[index].PaidAt.IsZero() {
		return PaymentRequest{}, ErrState
	}
	for i := 0; i < index; i++ {
		if o.Schedule[i].PaidAt.IsZero() {
			return PaymentRequest{}, ErrState
		}
	}
	expires := now.Add(30 * time.Minute)
	if index == 0 {
		expires = o.ExpiresAt
	} else if o.Mode == "deposit" {
		if now.Before(o.Snapshot.ReleaseAt) {
			return PaymentRequest{}, ErrState
		}
		expires = o.Snapshot.BalanceDueAt
	} else if o.Mode == "installment" {
		expires = o.Schedule[index].DueAt.Add(24 * time.Hour)
	}
	if !now.Before(expires) {
		return PaymentRequest{}, ErrState
	}
	return PaymentRequest{OrderID: o.ID, Index: index, UserID: o.UserID, Amount: o.Schedule[index].Amount, Currency: o.Currency, Title: o.Title, Product: o.Product, ExpiresAt: expires}, nil
}
func (s *Service) Cancel(ctx context.Context, user int64, id string) (Order, error) {
	var out Order
	e := s.Backend.Update(ctx, func(st *State) error {
		o, ok := st.Orders[id]
		if !ok || o.UserID != user {
			return ErrNotFound
		}
		if o.Status == "cancelled" {
			out = o
			return nil
		}
		if o.Status != "pending" || o.PaidAmount != 0 {
			return ErrState
		}
		releaseCoupon(st, o)
		releaseStock(st, &o)
		o.Status = "cancelled"
		o.UpdatedAt = s.now()
		st.Orders[id] = o
		out = o
		return nil
	})
	return out, e
}
func (s *Service) Ship(ctx context.Context, in Shipment) (Order, error) {
	var out Order
	if !nameOK(in.Carrier, 80) || !nameOK(in.Tracking, 120) {
		return out, ErrInvalid
	}
	e := s.Backend.Update(ctx, func(st *State) error {
		o, ok := st.Orders[in.OrderID]
		if !ok {
			return ErrNotFound
		}
		if o.Status == "shipped" && o.Carrier == in.Carrier && o.Tracking == in.Tracking {
			out = o
			return nil
		}
		if o.Status != "awaiting_shipment" || o.PaidAmount != o.Total || o.Released {
			return ErrState
		}
		o.Carrier = in.Carrier
		o.Tracking = in.Tracking
		o.Status = "shipped"
		o.ShippedAt = s.now()
		o.UpdatedAt = s.now()
		st.Orders[o.ID] = o
		out = o
		return nil
	})
	return out, e
}
func (s *Service) ConfirmReceipt(ctx context.Context, user int64, id string) (Order, error) {
	var out Order
	e := s.Backend.Update(ctx, func(st *State) error {
		o, ok := st.Orders[id]
		if !ok || o.UserID != user {
			return ErrNotFound
		}
		if o.Status == "completed" {
			out = o
			return nil
		}
		if o.Status != "shipped" {
			return ErrState
		}
		o.Status = "completed"
		o.CompletedAt = s.now()
		o.UpdatedAt = s.now()
		st.Orders[id] = o
		out = o
		return nil
	})
	return out, e
}
func (s *Service) ReconcileRefund(ctx context.Context, in Refund) (Order, error) {
	o, err := s.Order(ctx, 0, in.OrderID)
	if err != nil {
		return Order{}, err
	}
	if walletCurrency(o.Currency) {
		if !textOK(in.Reference, 200) || !textOK(in.Note, 1000) {
			return Order{}, ErrInvalid
		}
		if err = s.refundWallet(ctx, o); err != nil {
			return Order{}, err
		}
		return s.Order(ctx, 0, o.ID)
	}
	return s.reconcileRefund(ctx, in)
}

func (s *Service) reconcileRefund(ctx context.Context, in Refund) (Order, error) {
	var out Order
	if !nameOK(in.Reference, 200) || !nameOK(in.Note, 1000) {
		return out, ErrInvalid
	}
	e := s.Backend.Update(ctx, func(st *State) error {
		o, ok := st.Orders[in.OrderID]
		if !ok {
			return ErrNotFound
		}
		wallet := walletCurrency(o.Currency)
		if wallet && (in.Reference != walletRefundReference(o.ID) || !walletRefundComplete(*st, o)) {
			return ErrState
		}
		if o.Status == "refunded" && o.RefundReference == in.Reference {
			out = o
			return nil
		}
		if o.Status != "refund_required" && !(wallet && o.Status == "refunded") {
			return ErrState
		}
		o.Status = "refunded"
		o.RefundReference = in.Reference
		o.RefundNote = in.Note
		o.UpdatedAt = s.now()
		releaseStock(st, &o)
		st.Orders[o.ID] = o
		out = o
		return nil
	})
	return out, e
}
func validPayment(o Order, p PaymentReceipt) bool {
	return o.UserID == p.UserID && p.Index >= 0 && p.Index < len(o.Schedule) && p.Amount == o.Schedule[p.Index].Amount && p.Currency == o.Currency && nameOK(p.Reference, 200)
}
func (s *Service) ReviewPayment(ctx context.Context, p PaymentReceipt) error {
	return s.Backend.Update(ctx, func(st *State) error {
		o, ok := st.Orders[p.OrderID]
		if !ok {
			return ErrNotFound
		}
		if !validPayment(o, p) {
			return ErrInvalid
		}
		if o.Status == "refunded" {
			return nil
		}
		o.Status = "refund_required"
		o.UpdatedAt = s.now()
		releaseStock(st, &o)
		st.Orders[o.ID] = o
		return nil
	})
}
func (s *Service) RequestRefund(ctx context.Context, user int64, id, note string) (Order, error) {
	var out Order
	if !nameOK(note, 1000) {
		return out, ErrInvalid
	}
	e := s.Backend.Update(ctx, func(st *State) error {
		o, ok := st.Orders[id]
		if !ok || o.UserID != user {
			return ErrNotFound
		}
		if o.PaidAmount == 0 || !member(o.Status, "partially_paid", "awaiting_group", "awaiting_release", "awaiting_draw", "awaiting_shipment", "shipped", "completed") {
			return ErrState
		}
		o.RefundPreviousStatus = o.Status
		o.Status = "refund_requested"
		o.RefundNote = strings.TrimSpace(note)
		o.UpdatedAt = s.now()
		st.Orders[id] = o
		out = o
		return nil
	})
	return out, e
}

func (s *Service) ReviewRefund(ctx context.Context, id string, approved bool, note string) (Order, error) {
	var out Order
	if !textOK(note, 1000) {
		return out, ErrInvalid
	}
	err := s.Backend.Update(ctx, func(st *State) error {
		o, ok := st.Orders[id]
		if !ok {
			return ErrNotFound
		}
		if o.Status != "refund_requested" {
			return ErrState
		}
		if approved {
			o.Status = "refund_required"
			releaseStock(st, &o)
		} else {
			o.Status = o.RefundPreviousStatus
			if o.Mode == "group" {
				if g, exists := st.Groups[o.GroupID]; exists && g.Status == "failed" && o.Status == "awaiting_group" {
					o.Status = "refund_required"
				}
			}
			if o.Status == "refund_required" {
				releaseStock(st, &o)
			}
		}
		o.RefundReview = strings.TrimSpace(note)
		o.RefundPreviousStatus = ""
		o.UpdatedAt = s.now()
		st.Orders[id] = o
		out = o
		return nil
	})
	if err != nil {
		return out, err
	}
	if approved && walletCurrency(out.Currency) {
		if err = s.refundWallet(ctx, out); err != nil {
			return out, err
		}
		return s.Order(ctx, out.UserID, out.ID)
	}
	if !approved {
		if err = s.Advance(ctx); err != nil {
			return out, err
		}
		return s.Order(ctx, out.UserID, out.ID)
	}
	return out, nil
}
