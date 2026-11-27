package commerce

import (
	"context"
	"strconv"
	"time"

	"newpress.local/newpress/internal/virtualcurrency"
)

// ShopPayment is trusted server input derived from a persisted shop installment.
// It is never decoded from an HTTP request: browser-supplied prices are rejected.
type ShopPayment struct {
	UserID                                      int64
	OrderID, Title, Currency, Channel, ClientIP string
	Installment                                 int
	Amount                                      int64
	ExpiresAt                                   time.Time
	Accepted                                    bool
}

type ShopSettlement struct {
	UserID                                      int64
	OrderID, PaymentID, TransactionID, Currency string
	Installment                                 int
	Amount                                      int64
	PaidAt                                      time.Time
}

func shopSettlement(o Order) ShopSettlement {
	return ShopSettlement{UserID: o.UserID, OrderID: o.ShopOrder, PaymentID: o.ID,
		TransactionID: o.TransactionID, Currency: o.Currency, Installment: o.ShopInstallment, Amount: o.Amount, PaidAt: o.PaidAt}
}

// CreateShopPayment allocates exactly one provider order per shop installment.
// Expired/uncertain attempts are reconciled, never replaced with a second charge.
func (s *Service) CreateShopPayment(ctx context.Context, in ShopPayment) (Order, error) {
	if in.UserID < 1 || !in.Accepted || !validText(in.OrderID, 80) || !validText(in.Title, 80) || in.Amount < 1 || in.Amount > 1_000_000_000_000 || in.Installment < 1 || in.Installment > 60 {
		return Order{}, ErrInvalid
	}
	precision, e := Exponent(in.Currency)
	if e != nil {
		return Order{}, e
	}
	state, e := s.Backend.Read(ctx)
	if e != nil {
		return Order{}, e
	}
	if e = s.user(ctx, in.UserID, state.Settings.RequireVerifiedEmail); e != nil {
		return Order{}, e
	}
	request := "shop-" + virtualcurrency.FingerprintForStore(in.OrderID, strconv.Itoa(in.Installment))[:40]
	fp := virtualcurrency.FingerprintForStore("shop", in.OrderID, strconv.Itoa(in.Installment), stringID(in.Amount), in.Currency)
	for _, old := range state.Orders {
		if old.UserID == in.UserID && old.RequestID == request {
			if old.Fingerprint != fp || old.Kind != "shop" {
				return Order{}, ErrReused
			}
			if old.Channel != in.Channel {
				return old, ErrPending
			}
			return s.checkout(ctx, old, in.ClientIP)
		}
	}
	if !state.Settings.Enabled {
		return Order{}, ErrDisabled
	}
	var channel Channel
	for _, ch := range state.Settings.Channels {
		if ch.Key == in.Channel {
			channel = ch
		}
	}
	if !channel.Enabled || channel.Currency != in.Currency || s.SettleShop == nil {
		return Order{}, ErrInvalid
	}
	config, e := s.Vault.Open(channel.SealedConfig)
	if e != nil {
		return Order{}, e
	}
	if _, e = s.provider(config); e != nil {
		return Order{}, e
	}
	now := time.Now().UTC()
	expires := now.Add(time.Duration(state.Settings.OrderMinutes) * time.Minute)
	if !in.ExpiresAt.IsZero() && in.ExpiresAt.Before(expires) {
		expires = in.ExpiresAt
	}
	if !expires.After(now) {
		return Order{}, ErrPending
	}
	o := Order{ID: NewID(), UserID: in.UserID, RequestID: request, Fingerprint: fp, Kind: "shop", Title: in.Title, Product: "shop", Channel: channel.Key, Provider: channel.Provider, PaymentScope: paymentScope(config), SealedConfig: channel.SealedConfig, Status: "pending", Amount: in.Amount, Currency: in.Currency, Precision: precision, SettingsVersion: state.Settings.Version, Disclaimer: state.Settings.Disclaimer, RefundPolicy: state.Settings.RefundPolicy, CreatedAt: now, UpdatedAt: now, ExpiresAt: expires, ShopOrder: in.OrderID, ShopInstallment: in.Installment}
	e = s.Backend.Update(ctx, func(st *State) error {
		if st.Settings.Version != state.Settings.Version {
			return ErrConflict
		}
		pending := 0
		for _, old := range st.Orders {
			if old.UserID != in.UserID {
				continue
			}
			if old.RequestID == request {
				if old.Fingerprint != fp {
					return ErrReused
				}
				if old.Channel != in.Channel {
					return ErrPending
				}
				o = old
				return nil
			}
			if old.Status == "pending" && old.ExpiresAt.After(now) {
				pending++
			}
		}
		if pending >= 10 {
			return ErrPending
		}
		st.Orders[o.ID] = o
		return nil
	})
	if e != nil {
		return o, e
	}
	return s.checkout(ctx, o, in.ClientIP)
}

func (s *Service) ShopPayments(ctx context.Context, user int64, id string) ([]Order, error) {
	st, e := s.Backend.Read(ctx)
	if e != nil {
		return nil, e
	}
	out := []Order{}
	for _, o := range st.Orders {
		if o.Kind == "shop" && o.UserID == user && o.ShopOrder == id {
			out = append(out, o)
		}
	}
	return out, nil
}
