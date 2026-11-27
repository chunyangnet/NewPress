package shop

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

const MaxDescriptionBytes = 256 << 10

func textOK(s string, n int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= n && !strings.ContainsAny(s, "\x00<>")
}
func nameOK(s string, n int) bool {
	return strings.TrimSpace(s) != "" && textOK(s, n) && !strings.ContainsAny(s, "\r\n")
}
func member(v string, set ...string) bool {
	for _, x := range set {
		if v == x {
			return true
		}
	}
	return false
}
func priceOK(n int64) bool { return n >= 0 && n <= MaxAmount }
func imageOK(s string) bool {
	if s == "" {
		return true
	}
	u, e := url.Parse(s)
	return e == nil && len(s) <= 2000 && !strings.ContainsAny(s, "\\\r\n\x00") && !strings.HasPrefix(s, "//") && (strings.HasPrefix(s, "/") && u.Host == "" || u.Scheme == "https" && u.Host != "" && u.User == nil)
}

func validRafflePlan(r *RafflePlan) bool {
	if r == nil || !member(r.Mode, "instant", "scheduled") || !nameOK(r.Prize, 1000) || r.WinBPS < 0 || r.WinBPS > 10000 || r.Winners < 1 || r.Winners > 1000 || r.Mode == "scheduled" && r.DrawAt.IsZero() || len(r.Tiers) > 12 {
		return false
	}
	if !validTicketCap(r.TicketLimit) || !validTicketCap(r.UserLimit) || r.OrderLimit < 0 || r.OrderLimit > 99 || r.Campaign != "" && !keyPattern.MatchString(r.Campaign) {
		return false
	}
	if len(r.Tiers) == 0 {
		return true
	}
	seen := map[string]bool{}
	total := 0
	for _, tier := range r.Tiers {
		name := strings.TrimSpace(tier.Prize)
		if !nameOK(name, 120) || name != tier.Prize || seen[name] {
			return false
		}
		seen[name] = true
		if r.Mode == "instant" {
			if tier.WinBPS < 1 || tier.WinBPS > 10000 || tier.Winners != 0 {
				return false
			}
			total += tier.WinBPS
		} else {
			if tier.Winners < 1 || tier.Winners > 1000 || tier.WinBPS != 0 {
				return false
			}
			total += tier.Winners
		}
	}
	if r.Mode == "instant" {
		return total == r.WinBPS
	}
	return total == r.Winners
}

func imagesOK(images []string, cover string) bool {
	if len(images) > 12 {
		return false
	}
	seen := map[string]bool{}
	if cover != "" {
		seen[cover] = true
	}
	for _, image := range images {
		if image == "" || !imageOK(image) {
			return false
		}
		seen[image] = true
	}
	return len(seen) <= 12
}

func ValidateCatalog(c Catalog) error {
	if c.Version < 1 || c.OrderMinutes < 5 || c.OrderMinutes > 1440 || !textOK(c.Terms, 4000) || strings.TrimSpace(c.Terms) == "" || !textOK(c.RefundPolicy, 4000) || strings.TrimSpace(c.RefundPolicy) == "" || len(c.Products) > 1000 {
		return ErrInvalid
	}
	if len(c.RaffleCampaigns) > 100 {
		return ErrInvalid
	}
	campaigns := map[string]bool{}
	for _, campaign := range c.RaffleCampaigns {
		if !keyPattern.MatchString(campaign.Key) || campaigns[campaign.Key] || !nameOK(campaign.Title, 80) || !validTicketCap(campaign.TicketLimit) || !validTicketCap(campaign.UserLimit) {
			return ErrInvalid
		}
		campaigns[campaign.Key] = true
	}
	products := map[string]Product{}
	codes := map[string]bool{}
	for _, p := range c.Products {
		if !keyPattern.MatchString(p.Key) || products[p.Key].Key != "" || !nameOK(p.Title, 80) || !textOK(p.Summary, 4000) || !textOK(p.Category, 80) || !imageOK(p.Image) || !member(p.Kind, "virtual", "physical", "cdk", "donation", "raffle", "bundle") || !member(p.Mode, "direct", "presale", "deposit", "installment", "group") || !currencyOK(p.Currency) || p.Precision < 0 || p.Precision > 6 || p.Currency == "CNY" && p.Precision != 0 && p.Precision != 2 || p.Price < 1 || !priceOK(p.Price) || !priceOK(p.CompareAt) || !priceOK(p.ShippingFee) || p.Stock < -1 || p.Stock > 1_000_000_000 || len(p.Variants) > 100 || len(p.Codes) > 10000 || len(p.Bundle) > 50 || !textOK(p.Delivery, 16000) {
			return ErrInvalid
		}
		if p.Kind != "cdk" && len(p.Codes) > 0 {
			return ErrInvalid
		}
		if !imagesOK(p.Images, p.Image) || !imagesOK(p.DetailImages, "") || !textOK(p.Description, 16000) {
			return ErrInvalid
		}
		if len(p.DescriptionSource) > MaxDescriptionBytes || !utf8.ValidString(p.DescriptionSource) || strings.ContainsRune(p.DescriptionSource, '\x00') || p.DescriptionAuthorID < 0 {
			return ErrInvalid
		}
		if !serviceModulesOK(p.Services) {
			return ErrServiceModules
		}
		if p.Kind != "bundle" && len(p.Bundle) > 0 {
			return ErrInvalid
		}
		for _, code := range p.Codes {
			if !nameOK(code, 2000) || codes[code] {
				return ErrInvalid
			}
			codes[code] = true
		}
		variants := map[string]bool{}
		for _, v := range p.Variants {
			if !keyPattern.MatchString(v.Key) || variants[v.Key] || !nameOK(v.Title, 80) || !imageOK(v.Image) || v.Price < 1 || !priceOK(v.Price) || v.Stock < -1 || v.Stock > 1_000_000_000 {
				return ErrInvalid
			}
			variants[v.Key] = true
		}
		if len(p.Variants) > 0 && member(p.Kind, "donation", "raffle", "bundle") {
			return ErrInvalid
		}
		if p.Kind == "donation" && (p.Mode != "direct" || p.MaxDonation < p.Price || !priceOK(p.MaxDonation)) {
			return ErrInvalid
		}
		if p.Kind == "raffle" && (p.Mode != "direct" || !validRafflePlan(p.Raffle) || p.Raffle.Campaign != "" && !campaigns[p.Raffle.Campaign]) {
			return ErrInvalid
		}
		switch p.Mode {
		case "presale":
			if p.ReleaseAt.IsZero() {
				return ErrInvalid
			}
		case "deposit":
			if p.ReleaseAt.IsZero() || !p.BalanceDueAt.After(p.ReleaseAt) || p.Deposit < 1 || p.Deposit >= p.Price || len(p.Variants) > 0 {
				return ErrInvalid
			}
		case "installment":
			v := p.Installment
			if v == nil || len(v.Periods) == 0 || len(v.Periods) > 12 || v.IntervalDays < 1 || v.IntervalDays > 366 || v.InterestBPS < 0 || v.InterestBPS > 10000 {
				return ErrInvalid
			}
			seen := map[int]bool{}
			for _, n := range v.Periods {
				if n < 2 || n > 36 || seen[n] {
					return ErrInvalid
				}
				seen[n] = true
			}
		case "group":
			v := p.Group
			if v == nil || v.Size < 2 || v.Size > 100 || v.Price < 1 || v.Price >= p.Price || v.Hours < 1 || v.Hours > 720 || len(p.Variants) > 0 {
				return ErrInvalid
			}
		}
		products[p.Key] = p
	}
	for _, p := range c.Products {
		if p.Kind == "bundle" {
			if len(p.Bundle) == 0 {
				return ErrInvalid
			}
			seen := map[string]bool{}
			for _, b := range p.Bundle {
				child, ok := products[b.Product]
				if !ok || seen[b.Product] || b.Quantity < 1 || b.Quantity > 99 || member(child.Kind, "bundle", "raffle", "donation") || child.Mode != "direct" || len(child.Variants) > 0 {
					return ErrInvalid
				}
				seen[b.Product] = true
			}
		}
	}
	return nil
}

func ValidateState(st State) error {
	if err := validateReviews(st); err != nil {
		return err
	}
	if st.Schema != 1 || ValidateCatalog(st.Catalog) != nil || st.Orders == nil || st.Groups == nil || st.Receipts == nil || st.Claims == nil || st.WalletPayments == nil || st.Coupons == nil || len(st.Orders) > 100000 || len(st.Groups) > 100000 || len(st.Receipts) > 500000 || len(st.Claims) > 500000 || len(st.WalletPayments) > 500000 {
		return ErrInvalid
	}
	if validateCouponSet(st.Coupons, st.Catalog) != nil || st.CouponVersion < 0 {
		return ErrInvalid
	}
	requests := map[string]bool{}
	tickets := map[string]bool{}
	codes := map[string]bool{}
	for _, p := range st.Catalog.Products {
		for _, c := range p.Codes {
			codes[c] = true
		}
	}
	for id, o := range st.Orders {
		if id != o.ID || !strings.HasPrefix(id, "so_") || o.UserID < 1 || !nameOK(o.RequestID, 128) || o.Fingerprint == "" || o.CreatedAt.IsZero() || o.UpdatedAt.Before(o.CreatedAt) || !currencyOK(o.Currency) || o.Precision < 0 || o.Precision > 6 || o.Total < 1 || !priceOK(o.Total) || o.PaidAmount < 0 || len(o.Schedule) == 0 || len(o.Schedule) > 36 || !member(o.Status, "pending", "partially_paid", "awaiting_group", "awaiting_release", "awaiting_draw", "awaiting_shipment", "shipped", "completed", "cancelled", "expired", "refund_requested", "refund_required", "refunded") {
			return ErrInvalid
		}
		k := requestKey(o.UserID, o.RequestID)
		if requests[k] {
			return ErrInvalid
		}
		requests[k] = true
		if o.Snapshot.Key != o.Product || o.Snapshot.Kind != o.Kind || o.Snapshot.Currency != o.Currency || o.Quantity < 1 || o.Quantity > 99 || o.ExpiresAt.Before(o.CreatedAt) || o.CatalogVersion < 1 || o.Version != o.CatalogVersion || len(o.Reservations) == 0 || len(o.Reservations) > 51 {
			return ErrInvalid
		}
		if o.Kind == "raffle" && !validRafflePlan(o.Snapshot.Raffle) {
			return ErrInvalid
		}
		if o.Mode == "installment" && o.Snapshot.Installment == nil || o.Mode == "group" && o.Snapshot.Group == nil {
			return ErrInvalid
		}
		if o.Released && member(o.Status, "pending", "partially_paid", "awaiting_group", "awaiting_release", "awaiting_draw", "awaiting_shipment", "shipped", "completed", "refund_requested") {
			return ErrInvalid
		}
		if o.Status == "refund_requested" && !member(o.RefundPreviousStatus, "partially_paid", "awaiting_group", "awaiting_release", "awaiting_draw", "awaiting_shipment", "shipped", "completed") {
			return ErrInvalid
		}
		original := o.OriginalSubtotal
		if !priceOK(original) || original == 0 || !priceOK(o.Discount) || o.Discount > original || o.Subtotal != original-o.Discount || !priceOK(o.Subtotal) || o.Subtotal == 0 || !priceOK(o.Shipping) || !priceOK(o.Interest) || o.Subtotal+o.Shipping+o.Interest != o.Total || o.PayNow != o.Schedule[0].Amount || (o.Coupon == "") != (o.Discount == 0) {
			return ErrInvalid
		}
		var total, paid int64
		for i, p := range o.Schedule {
			if p.Index != i || p.Amount < 1 || !priceOK(p.Amount) || !priceOK(p.Principal) || !priceOK(p.Interest) || p.Principal+p.Interest != p.Amount || p.DueAt.IsZero() || total > o.Total-p.Amount {
				return ErrInvalid
			}
			total += p.Amount
			if !p.PaidAt.IsZero() {
				if p.PaymentRef == "" {
					return ErrInvalid
				}
				paid += p.Amount
				r, ok := st.Receipts[p.PaymentRef]
				if !ok || r.OrderID != id || r.UserID != o.UserID || r.Index != i || r.Amount != p.Amount || r.Currency != o.Currency {
					return ErrInvalid
				}
			} else if p.PaymentRef != "" {
				return ErrInvalid
			}
		}
		if total != o.Total || paid != o.PaidAmount || paid > o.Total {
			return ErrInvalid
		}
		for _, ticket := range o.Tickets {
			if o.Kind != "raffle" || !strings.HasPrefix(ticket.ID, "st_") || tickets[ticket.ID] || ticket.OrderID != id || ticket.Round == "" || !member(ticket.Status, "pending", "won", "lost") || ticket.DrawAt.IsZero() || ticket.Winners < 1 || ticket.Winners > 1000 || !nameOK(ticket.Prize, 1000) || (ticket.Status == "pending") != ticket.DrawnAt.IsZero() {
				return ErrInvalid
			}
			tickets[ticket.ID] = true
			if !textOK(ticket.Delivery, 4000) || (ticket.Delivery == "") != ticket.DeliveredAt.IsZero() || !ticket.DeliveredAt.IsZero() && (ticket.Status != "won" || ticket.DeliveredAt.Before(ticket.DrawnAt)) {
				return ErrInvalid
			}
		}
		if len(o.Tickets) > 0 && len(o.Tickets) != o.Quantity {
			return ErrInvalid
		}
		if o.GroupID != "" {
			g, ok := st.Groups[o.GroupID]
			if !ok || g.Product != o.Product || g.Variant != o.Variant || o.Mode != "group" {
				return ErrInvalid
			}
		}
		if !o.Released {
			for _, r := range o.Reservations {
				if r.Quantity < 1 {
					return ErrInvalid
				}
				for _, c := range r.Codes {
					if codes[c] {
						return ErrInvalid
					}
					codes[c] = true
				}
			}
		}
	}
	for id, g := range st.Groups {
		if id != g.ID || g.Size < 2 || g.Size > 100 || g.Paid < 0 || g.Paid > g.Size || !member(g.Status, "open", "completed", "failed") || g.ExpiresAt.IsZero() {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for _, id := range g.Orders {
			o, ok := st.Orders[id]
			if !ok || o.GroupID != g.ID || seen[id] {
				return ErrInvalid
			}
			seen[id] = true
		}
	}
	for ref, r := range st.Receipts {
		if ref != r.Reference || !nameOK(ref, 200) || r.Amount < 1 || !priceOK(r.Amount) || !currencyOK(r.Currency) {
			return ErrInvalid
		}
		o, ok := st.Orders[r.OrderID]
		if !ok || o.UserID != r.UserID || r.Index < 0 || r.Index >= len(o.Schedule) {
			return ErrInvalid
		}
		p := o.Schedule[r.Index]
		if p.PaymentRef != ref || p.PaidAt.IsZero() || p.Amount != r.Amount {
			return ErrInvalid
		}
	}
	for id, claim := range st.Claims {
		coupon, exists := couponByCode(st, claim.Code)
		if id != claim.ID || !strings.HasPrefix(id, "sc_") || !exists || claim.UserID < 1 || claim.ClaimedAt.IsZero() || !member(claim.Status, "available", "reserved", "used") || claim.Status == "available" && claim.OrderID != "" || claim.Status != "available" && claim.OrderID == "" {
			return ErrInvalid
		}
		if (claim.Status == "used") != !claim.UsedAt.IsZero() {
			return ErrInvalid
		}
		if claim.Status != "available" {
			order, ok := st.Orders[claim.OrderID]
			if !ok || order.UserID != claim.UserID || order.Coupon != coupon.Code || claim.Status == "reserved" && order.PaidAmount > 0 || claim.Status == "used" && order.PaidAmount == 0 {
				return ErrInvalid
			}
		}
	}
	walletIndexes := map[string]bool{}
	for id, payment := range st.WalletPayments {
		order, ok := st.Orders[payment.OrderID]
		key := payment.OrderID + ":" + strconv.Itoa(payment.Index)
		if id != payment.ID || !strings.HasPrefix(id, "sw_") || !ok || payment.UserID != order.UserID || payment.Currency != order.Currency || !walletCurrency(payment.Currency) || payment.Index < 0 || payment.Index >= len(order.Schedule) || payment.Amount != order.Schedule[payment.Index].Amount || payment.Version < 1 || payment.Precision < 0 || payment.Precision > 6 || !member(payment.Status, "pending", "paid", "cancelled") || payment.Status != "cancelled" && walletIndexes[key] || payment.Status == "paid" && payment.Entry < 1 || payment.Status == "cancelled" && payment.Entry < 1 || payment.Entry == 0 && payment.Status != "pending" || payment.Entry < 0 || payment.RefundEntry < 0 || payment.Status != "paid" && payment.RefundEntry != 0 {
			return ErrInvalid
		}
		if payment.Status != "cancelled" {
			walletIndexes[key] = true
		}
	}
	return nil
}
