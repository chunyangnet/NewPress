package commerce

import (
	"context"
	"sort"
	"strconv"
	"time"

	"newpress.local/newpress/internal/payment"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/virtualcurrency"
)

// ResourcePayment only accepts server-derived prices and condition snapshots.
type ResourcePayment struct {
	UserID                                       int64
	Key, Title, Currency, Channel, ClientIP, URL string
	Amount                                       int64
	ExpiresAt                                    time.Time
	Accepted                                     bool
	Terms                                        *resources.PurchaseTerms
	ExpectedVersion                              int64
}

// ResourceMatches verifies legacy price-dependent keys against the original
// order amount/currency. A new price is never used as financial evidence.
func ResourceMatches(o Order, terms resources.PurchaseTerms) bool {
	if o.Kind != "resource" || !terms.Valid() || terms.Kind != "payment" {
		return false
	}
	if o.ResourceTerms != nil {
		return o.ResourceTerms.Key() == terms.Key()
	}
	terms.Amount, terms.Currency = o.Amount, o.Currency
	return terms.LegacyKey() == o.ResourceKey
}

func resourcePaymentMatches(o Order, in ResourcePayment) bool {
	if in.Terms != nil {
		return ResourceMatches(o, *in.Terms)
	}
	return o.Kind == "resource" && o.ResourceKey == in.Key
}

func resourceOrders(st State, in ResourcePayment) []Order {
	orders := []Order{}
	for _, o := range st.Orders {
		if o.UserID == in.UserID && resourcePaymentMatches(o, in) {
			orders = append(orders, o)
		}
	}
	sort.Slice(orders, func(i, j int) bool {
		if orders[i].CreatedAt.Equal(orders[j].CreatedAt) {
			return orders[i].ID < orders[j].ID
		}
		return orders[i].CreatedAt.Before(orders[j].CreatedAt)
	})
	return orders
}

func resourceRetryable(o Order) bool {
	return o.Status == "cancelled" && !o.PaymentClosedAt.IsZero() || o.Status == "refunded" && o.TransactionID != ""
}

func resourceDuplicate(st State, o Order) bool {
	for _, other := range st.Orders {
		if other.ID == o.ID || other.UserID != o.UserID || other.Kind != "resource" {
			continue
		}
		matches := other.ResourceKey == o.ResourceKey
		if o.ResourceTerms != nil {
			matches = ResourceMatches(other, *o.ResourceTerms)
		} else if other.ResourceTerms != nil {
			matches = ResourceMatches(o, *other.ResourceTerms)
		}
		if matches && (other.Status == "completed" || !o.PaymentClosedAt.IsZero() && other.CreatedAt.After(o.CreatedAt)) {
			return true
		}
	}
	return false
}

// closeUncreated competes with checkout's durable lease under the same lock.
// Zero attempts proves that no provider request was sent, even after a crash.
func (s *Service) closeUncreated(ctx context.Context, id string) (bool, error) {
	closed := false
	err := s.Backend.Update(ctx, func(st *State) error {
		o := st.Orders[id]
		if o.Kind == "resource" && (o.Status == "pending" || o.Status == "cancelled") && o.CheckoutAttempts == 0 &&
			o.Checkout.URL == "" && o.Checkout.QRCode == "" && o.Checkout.ProviderOrderID == "" && o.TransactionID == "" && !o.LeaseUntil.After(time.Now()) {
			o.Status, o.ErrorCode = "cancelled", "payment_not_created"
			o.PaymentClosedAt, o.UpdatedAt = time.Now().UTC(), time.Now().UTC()
			st.Orders[id] = o
			closed = true
		}
		return nil
	})
	return closed, err
}

func (s *Service) CreateResourcePayment(ctx context.Context, in ResourcePayment) (Order, error) {
	if in.UserID < 1 || !in.Accepted || !validText(in.Key, 80) || !validText(in.Title, 80) || in.Amount < 1 || in.Amount > 1_000_000_000_000 || len(in.Key) != 64 {
		return Order{}, ErrInvalid
	}
	if in.Terms != nil {
		terms := *in.Terms
		if !terms.Valid() || terms.Kind != "payment" || terms.LegacyKey() != in.Key || terms.Amount != in.Amount || terms.Currency != in.Currency {
			return Order{}, ErrInvalid
		}
		if terms.EndsAt != nil {
			deadline := *terms.EndsAt
			terms.EndsAt = &deadline
			if !in.ExpiresAt.IsZero() && !in.ExpiresAt.Equal(deadline) {
				return Order{}, ErrInvalid
			}
			in.ExpiresAt = deadline
		}
		in.Terms = &terms
	}
	precision, err := Exponent(in.Currency)
	if err != nil {
		return Order{}, err
	}
	var last Order
	for pass := 0; pass < 4; pass++ {
		state, err := s.Backend.Read(ctx)
		if err != nil {
			return last, err
		}
		if err = s.user(ctx, in.UserID, state.Settings.RequireVerifiedEmail); err != nil {
			return last, err
		}
		orders := resourceOrders(state, in)
		if in.Terms == nil {
			for _, old := range orders {
				if old.Amount != in.Amount || old.Currency != in.Currency {
					return old, ErrReused
				}
			}
		}
		for _, old := range orders {
			if old.Status == "completed" {
				return old, nil
			}
		}
		reconciled := false
		for _, old := range orders {
			last = old
			if resourceRetryable(old) {
				continue
			}
			if old.Status == "paid" {
				return s.Fulfill(ctx, old.ID)
			}
			if old.Status == "review" || old.Status == "refunded" {
				return old, ErrPending
			}
			if old.Status == "pending" && old.ExpiresAt.After(time.Now()) {
				if old.Channel != in.Channel {
					return old, ErrPending
				}
				return s.checkout(ctx, old, in.ClientIP)
			}
			closed, err := s.closeUncreated(ctx, old.ID)
			if err != nil {
				return old, err
			}
			if closed {
				reconciled = true
				continue
			}
			if old.LeaseUntil.After(time.Now()) {
				return old, ErrPending
			}
			synced, err := s.Sync(ctx, in.UserID, old.ID)
			if err != nil || !resourceRetryable(synced) {
				if err == nil && synced.Status == "completed" {
					return synced, nil
				}
				return oldIfEmpty(synced, old), ErrPending
			}
			reconciled = true
		}
		if reconciled {
			continue
		}
		if !state.Settings.Enabled {
			return last, ErrDisabled
		}
		if in.ExpectedVersion != 0 && state.Settings.Version != in.ExpectedVersion {
			return last, ErrConflict
		}
		var channel Channel
		for _, ch := range state.Settings.Channels {
			if ch.Key == in.Channel {
				channel = ch
			}
		}
		if !channel.Enabled || channel.Currency != in.Currency {
			return last, ErrInvalid
		}
		config, err := s.Vault.Open(channel.SealedConfig)
		if err != nil {
			return last, err
		}
		if _, err = s.provider(config); err != nil {
			return last, err
		}
		now := time.Now().UTC()
		expires := now.Add(time.Duration(state.Settings.OrderMinutes) * time.Minute)
		if channel.Provider == "stripe" && expires.Before(now.Add(31*time.Minute)) {
			// Checkout requires at least thirty minutes; leave a minute for the
			// request/lease round trip without extending the resource deadline.
			expires = now.Add(31 * time.Minute)
		}
		if !in.ExpiresAt.IsZero() && in.ExpiresAt.Before(expires) {
			expires = in.ExpiresAt
		}
		if !expires.After(now) {
			return last, ErrResourceDelivery
		}
		if channel.Provider == "stripe" && expires.Before(now.Add(31*time.Minute)) {
			return last, payment.ErrExpiry
		}
		identity := in.Key
		if in.Terms != nil {
			identity = in.Terms.Key()
		}
		request := "resource-" + virtualcurrency.FingerprintForStore(identity, "article")[:40]
		fp := virtualcurrency.FingerprintForStore("resource", in.Key, "article", stringID(in.Amount), in.Currency)
		o := Order{ID: NewID(), UserID: in.UserID, Fingerprint: fp, Kind: "resource", Title: in.Title, Product: "resource",
			Channel: channel.Key, Provider: channel.Provider, PaymentScope: paymentScope(config), SealedConfig: channel.SealedConfig,
			Status: "pending", Amount: in.Amount, Currency: in.Currency, Precision: precision, SettingsVersion: state.Settings.Version,
			Disclaimer: state.Settings.Disclaimer, RefundPolicy: state.Settings.RefundPolicy, CreatedAt: now, UpdatedAt: now, ExpiresAt: expires,
			ResourceKey: in.Key, ResourceURL: in.URL, ResourceTerms: in.Terms, ResourceDeadline: in.ExpiresAt}
		concurrent := false
		err = s.Backend.Update(ctx, func(st *State) error {
			if st.Settings.Version != state.Settings.Version {
				return ErrConflict
			}
			prior := resourceOrders(*st, in)
			attempt := len(prior) + 1
			for _, old := range prior {
				if !resourceRetryable(old) {
					o, concurrent = old, true
					return nil
				}
				attempt = max(attempt, old.ResourceAttempt+1)
			}
			pending := 0
			for _, old := range st.Orders {
				if old.UserID == in.UserID && old.Status == "pending" {
					pending++
				}
			}
			if pending >= 10 || attempt > 1_000_000 {
				return ErrPending
			}
			o.ResourceAttempt, o.RequestID = attempt, request+"-"+strconv.Itoa(attempt)
			st.Orders[o.ID] = o
			return nil
		})
		if err != nil {
			return o, err
		}
		if concurrent {
			last = o
			continue
		}
		return s.checkout(ctx, o, in.ClientIP)
	}
	return last, ErrPending
}

func oldIfEmpty(current, old Order) Order {
	if current.ID == "" {
		return old
	}
	return current
}

func (s *Service) checkResourceCapture(ctx context.Context, id string, n payment.Notification) error {
	o, err := s.Order(ctx, 0, id)
	if err != nil {
		return err
	}
	if n.OrderID != id || n.Amount != o.Amount || n.Currency != o.Currency || o.Checkout.ProviderOrderID != "" && n.ProviderOrderID != o.Checkout.ProviderOrderID {
		return ErrUnverified
	}
	if o.Kind != "resource" || o.Status != "pending" || !o.ExpiresAt.After(time.Now()) {
		return ErrResourceDelivery
	}
	if err = s.user(ctx, o.UserID, false); err != nil {
		return err
	}
	if s.ValidateResource != nil {
		return s.ValidateResource(ctx, o)
	}
	return nil
}
