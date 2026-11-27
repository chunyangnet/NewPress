package shop

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var couponCodePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{2,39}$`)

type CouponOption struct {
	Coupon
	Claimed        bool `json:"claimed"`
	CanClaim       bool `json:"can_claim"`
	UserClaimCount int  `json:"user_claim_count"`
	AvailableCount int  `json:"available_count"`
	ClaimCount     int  `json:"claim_count"`
	Remaining      int  `json:"remaining"`
}

func (s *Service) CouponConfig(ctx context.Context) (int64, []Coupon, error) {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return 0, nil, err
	}
	return st.CouponVersion, append([]Coupon{}, st.Coupons...), nil
}

func normalizeCouponCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }
func couponByCode(st State, code string) (Coupon, bool) {
	code = normalizeCouponCode(code)
	for _, coupon := range st.Coupons {
		if coupon.Code == code {
			return coupon, true
		}
	}
	return Coupon{}, false
}

func validateCouponSet(coupons []Coupon, catalog Catalog) error {
	if len(coupons) > 500 {
		return ErrInvalid
	}
	products := map[string]Product{}
	for _, p := range catalog.Products {
		products[p.Key] = p
	}
	seen := map[string]bool{}
	for _, c := range coupons {
		if !couponCodePattern.MatchString(c.Code) || seen[c.Code] || !nameOK(c.Title, 80) || !member(c.Kind, "fixed", "percent") || !priceOK(c.Value) || c.Value < 1 || !priceOK(c.Minimum) || !priceOK(c.MaximumDiscount) || c.PerUserLimit < 1 || c.PerUserLimit > 100 || c.ClaimLimit < 0 || c.ClaimLimit > 1_000_000 || c.UseLimit < 0 || c.UseLimit > 1_000_000 || c.StartsAt.IsZero() || c.EndsAt.IsZero() || !c.EndsAt.After(c.StartsAt) {
			return ErrInvalid
		}
		if c.Kind == "percent" && c.Value > 10000 {
			return ErrInvalid
		}
		if c.Kind == "percent" && c.Currency == "" && (c.Minimum > 0 || c.MaximumDiscount > 0) {
			return ErrInvalid
		}
		if c.Kind == "fixed" && (!currencyOK(c.Currency) || c.Currency == "") {
			return ErrInvalid
		}
		if c.Currency != "" && !currencyOK(c.Currency) {
			return ErrInvalid
		}
		for _, v := range c.Kinds {
			if !member(v, "virtual", "physical", "cdk", "donation", "raffle", "bundle") {
				return ErrInvalid
			}
		}
		for _, v := range c.Modes {
			if !member(v, "direct", "presale", "deposit", "installment", "group") {
				return ErrInvalid
			}
		}
		for _, v := range c.Products {
			if _, ok := products[v]; !ok {
				return ErrInvalid
			}
		}
		for _, v := range c.Categories {
			if !textOK(v, 80) || strings.TrimSpace(v) == "" {
				return ErrInvalid
			}
		}
		for _, v := range c.Levels {
			if !keyPattern.MatchString(v) {
				return ErrInvalid
			}
		}
		for _, v := range c.Memberships {
			if !keyPattern.MatchString(v) {
				return ErrInvalid
			}
		}
		seen[c.Code] = true
	}
	return nil
}

func (s *Service) SaveCoupons(ctx context.Context, version int64, coupons []Coupon) (int64, error) {
	for i := range coupons {
		coupons[i].Code = normalizeCouponCode(coupons[i].Code)
	}
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return 0, err
	}
	if err = validateCouponSet(coupons, st.Catalog); err != nil {
		return 0, err
	}
	for _, coupon := range coupons {
		if coupon.Currency == "" || coupon.Currency == "CNY" {
			continue
		}
		store, _, currencyErr := s.walletStore(ctx, coupon.Currency)
		if currencyErr != nil {
			return 0, ErrInvalid
		}
		settings, currencyErr := store.Settings(ctx)
		if currencyErr != nil {
			return 0, currencyErr
		}
		if !settings.Enabled {
			return 0, ErrInvalid
		}
	}
	err = s.Backend.Update(ctx, func(current *State) error {
		if current.CouponVersion != version {
			return ErrConflict
		}
		desired := map[string]Coupon{}
		for _, c := range coupons {
			desired[c.Code] = c
		}
		for _, old := range current.Coupons {
			claimed := false
			for _, claim := range current.Claims {
				if claim.Code == old.Code {
					claimed = true
					break
				}
			}
			updated, exists := desired[old.Code]
			if claimed && (!exists || couponTerms(old) != couponTerms(updated)) {
				return ErrState
			}
		}
		for i := range coupons {
			if coupons[i].CreatedAt.IsZero() {
				for _, old := range current.Coupons {
					if old.Code == coupons[i].Code {
						coupons[i].CreatedAt = old.CreatedAt
						break
					}
				}
				if coupons[i].CreatedAt.IsZero() {
					coupons[i].CreatedAt = s.now()
				}
			}
		}
		current.Coupons = append([]Coupon{}, coupons...)
		current.CouponVersion++
		return nil
	})
	return version + 1, err
}

func couponTerms(c Coupon) string {
	c.Enabled = false
	c.Home = false
	c.CreatedAt = time.Time{}
	return fingerprint(c)
}

func couponUserRules(ctx context.Context, s *Service, user int64, c Coupon) error {
	if user < 1 {
		return ErrIneligible
	}
	if s.Users != nil {
		u, err := s.Users.GetUser(ctx, user)
		if err != nil {
			return err
		}
		if u.Status != "active" {
			return ErrIneligible
		}
	}
	if len(c.Levels) == 0 && len(c.Memberships) == 0 {
		return nil
	}
	if s.Members == nil {
		return ErrUnavailable
	}
	snapshot, err := s.Members.Snapshot(ctx, user, 0)
	if err != nil {
		return err
	}
	if len(c.Levels) > 0 {
		matched := false
		if snapshot.Level != nil {
			for _, key := range c.Levels {
				if snapshot.Level.Key == key {
					matched = true
					break
				}
			}
		}
		if !matched {
			return ErrIneligible
		}
	}
	if len(c.Memberships) > 0 {
		matched := false
		if snapshot.MembershipActive {
			for _, key := range c.Memberships {
				if snapshot.Benefits.Plan == key {
					matched = true
					break
				}
			}
		}
		if !matched {
			return ErrIneligible
		}
	}
	return nil
}

func (s *Service) CouponList(ctx context.Context, user int64, homeOnly bool) ([]CouponOption, error) {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := []CouponOption{}
	for _, c := range st.Coupons {
		if !c.Enabled || homeOnly && !c.Home || now.Before(c.StartsAt) || !now.Before(c.EndsAt) {
			continue
		}
		count := 0
		for _, claim := range st.Claims {
			if claim.Code == c.Code {
				count++
			}
		}
		remaining := 0
		if c.ClaimLimit > 0 {
			remaining = c.ClaimLimit - count
			if remaining < 0 {
				remaining = 0
			}
		}
		userClaimCount, availableCount := 0, 0
		if user > 0 {
			for _, claim := range st.Claims {
				if claim.Code == c.Code && claim.UserID == user {
					userClaimCount++
					if claim.Status == "available" {
						availableCount++
					}
				}
			}
		}
		if c.ClaimLimit > 0 && remaining == 0 && userClaimCount == 0 {
			continue
		}
		canClaim := user > 0 && userClaimCount < c.PerUserLimit && (c.ClaimLimit == 0 || remaining > 0)
		out = append(out, CouponOption{Coupon: c, Claimed: availableCount > 0, CanClaim: canClaim, UserClaimCount: userClaimCount, AvailableCount: availableCount, ClaimCount: count, Remaining: remaining})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Home != out[j].Home {
			return out[i].Home
		}
		return out[i].Coupon.StartsAt.Before(out[j].Coupon.StartsAt)
	})
	return out, nil
}

func (s *Service) ClaimCoupon(ctx context.Context, user int64, code string) (CouponClaim, error) {
	code = normalizeCouponCode(code)
	var out CouponClaim
	err := s.Backend.Update(ctx, func(st *State) error {
		c, ok := couponByCode(*st, code)
		if !ok || !c.Enabled || s.now().Before(c.StartsAt) || !s.now().Before(c.EndsAt) {
			return ErrNotFound
		}
		if err := couponUserRules(ctx, s, user, c); err != nil {
			return err
		}
		count, userCount := 0, 0
		for _, claim := range st.Claims {
			if claim.Code == code {
				count++
				if claim.UserID == user {
					userCount++
				}
			}
		}
		if c.ClaimLimit > 0 && count >= c.ClaimLimit || userCount >= c.PerUserLimit {
			return ErrStock
		}
		if st.Claims == nil {
			st.Claims = map[string]CouponClaim{}
		}
		id := newID("sc_")
		out = CouponClaim{ID: id, Code: code, UserID: user, Status: "available", ClaimedAt: s.now()}
		st.Claims[id] = out
		return nil
	})
	return out, err
}

func usableClaim(st State, code string, user int64) (CouponClaim, bool) {
	for _, claim := range st.Claims {
		if claim.Code == code && claim.UserID == user && claim.Status == "available" {
			return claim, true
		}
	}
	return CouponClaim{}, false
}

func couponUseCapacity(st State, c Coupon, user int64) error {
	uses, userUses := 0, 0
	for _, claim := range st.Claims {
		if claim.Code == c.Code && member(claim.Status, "reserved", "used") {
			uses++
			if claim.UserID == user {
				userUses++
			}
		}
	}
	if c.UseLimit > 0 && uses >= c.UseLimit || userUses >= c.PerUserLimit {
		return ErrStock
	}
	return nil
}

func couponApplies(c Coupon, p Product, q Quote, now time.Time) bool {
	if !c.Enabled || now.Before(c.StartsAt) || !now.Before(c.EndsAt) || q.OriginalSubtotal < c.Minimum || c.Currency != "" && c.Currency != q.Currency {
		return false
	}
	if c.Kind == "fixed" && c.Currency != q.Currency {
		return false
	}
	if len(c.Kinds) > 0 && !member(p.Kind, c.Kinds...) || len(c.Modes) > 0 && !member(q.Mode, c.Modes...) || len(c.Products) > 0 && !member(p.Key, c.Products...) || len(c.Categories) > 0 && !member(p.Category, c.Categories...) {
		return false
	}
	return true
}

func discountQuote(q Quote, p Product, c Coupon) Quote {
	discount := c.Value
	if c.Kind == "percent" {
		discount = (q.OriginalSubtotal * c.Value) / 10000
		if c.MaximumDiscount > 0 && discount > c.MaximumDiscount {
			discount = c.MaximumDiscount
		}
	}
	minimumSubtotal := int64(1)
	if q.Mode == "installment" {
		minimumSubtotal = int64(len(q.Schedule))
	}
	if q.OriginalSubtotal <= minimumSubtotal {
		return q
	}
	if discount > q.OriginalSubtotal-minimumSubtotal {
		discount = q.OriginalSubtotal - minimumSubtotal
	}
	if discount <= 0 {
		return q
	}
	q.Coupon = c.Code
	q.CouponTitle = c.Title
	q.Discount = discount
	q.Subtotal = q.OriginalSubtotal - discount
	if q.Mode == "installment" {
		plan := p.Installment
		oldSchedule := q.Schedule
		count := len(oldSchedule)
		fee := (q.Subtotal*int64(plan.InterestBPS) + 9999) / 10000
		q.Interest = fee * int64(count)
		q.Schedule = nil
		for i := 0; i < count; i++ {
			part := q.Subtotal / int64(count)
			if int64(i) < q.Subtotal%int64(count) {
				part++
			}
			if i == 0 {
				part += q.Shipping
			}
			q.Schedule = append(q.Schedule, Installment{Index: i, Title: "第 " + strconv.Itoa(i+1) + " 期", Principal: part, Interest: fee, Amount: part + fee, DueAt: oldSchedule[i].DueAt})
		}
	} else if q.Mode == "deposit" {
		oldDeposit := p.Deposit * int64(q.Quantity)
		paidMerch := oldDeposit
		if paidMerch > q.Subtotal {
			paidMerch = q.Subtotal
		}
		tail := q.Subtotal - paidMerch
		q.Schedule[0].Principal = paidMerch + q.Shipping
		q.Schedule[0].Amount = paidMerch + q.Shipping
		q.Schedule[1].Principal = tail
		q.Schedule[1].Amount = tail
		if tail == 0 {
			q.Schedule = q.Schedule[:1]
		}
	} else {
		q.Schedule[0].Principal = q.Subtotal + q.Shipping
		q.Schedule[0].Amount = q.Subtotal + q.Shipping
	}
	q.Total = q.Subtotal + q.Shipping + q.Interest
	q.PayNow = q.Schedule[0].Amount
	return q
}

func (s *Service) QuoteFor(ctx context.Context, user int64, in QuoteInput) (Quote, error) {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return Quote{}, err
	}
	q, p, err := makeQuote(st, in, user, s.now())
	if err != nil {
		return Quote{}, err
	}
	if strings.TrimSpace(in.Coupon) == "" {
		return q, nil
	}
	c, ok := couponByCode(st, in.Coupon)
	if !ok || !couponApplies(c, p, q, s.now()) {
		return Quote{}, ErrIneligible
	}
	if _, ok = usableClaim(st, c.Code, user); !ok {
		return Quote{}, ErrIneligible
	}
	if err = couponUserRules(ctx, s, user, c); err != nil {
		return Quote{}, err
	}
	if err = couponUseCapacity(st, c, user); err != nil {
		return Quote{}, err
	}
	q = discountQuote(q, p, c)
	if q.Coupon == "" {
		return Quote{}, ErrIneligible
	}
	return q, nil
}

func reserveCoupon(st *State, o Order, now time.Time) error {
	if o.Coupon == "" {
		return nil
	}
	c, ok := couponByCode(*st, o.Coupon)
	if !ok || !couponApplies(c, o.Snapshot, o.Quote, now) {
		return ErrIneligible
	}
	claim, ok := usableClaim(*st, c.Code, o.UserID)
	if !ok {
		return ErrIneligible
	}
	if err := couponUseCapacity(*st, c, o.UserID); err != nil {
		return err
	}
	claim.Status = "reserved"
	claim.OrderID = o.ID
	st.Claims[claim.ID] = claim
	return nil
}

func releaseCoupon(st *State, o Order) {
	if o.Coupon == "" || o.PaidAmount > 0 {
		return
	}
	for id, claim := range st.Claims {
		if claim.OrderID == o.ID && claim.Status == "reserved" {
			claim.Status = "available"
			claim.OrderID = ""
			st.Claims[id] = claim
		}
	}
}

func useCoupon(st *State, o Order, now time.Time) {
	if o.Coupon == "" {
		return
	}
	for id, claim := range st.Claims {
		if claim.OrderID == o.ID && claim.Status == "reserved" {
			claim.Status = "used"
			claim.UsedAt = now
			st.Claims[id] = claim
		}
	}
}
