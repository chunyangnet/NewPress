package commerce

import (
	"context"
	"errors"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/payment"
	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/virtualcurrency"
)

type Service struct {
	Backend         Backend
	Vault           *Vault
	Currencies      virtualcurrency.Catalog
	Members         *progression.Store
	Users           virtualcurrency.Users
	PublicURL       string
	Client          *http.Client
	ProviderFactory func(payment.Config) (payment.Provider, error)
	// SettleShop acknowledges a verified payment using an idempotent shop receipt.
	SettleShop func(context.Context, ShopSettlement) error
	ReviewShop func(context.Context, ShopSettlement) error
	// ValidateResource checks the live article before granting a verified order.
	// ErrResourceDelivery is terminal; transient storage errors remain recoverable.
	ValidateResource func(context.Context, Order) error
}

func stringID(n int64) string { return strconv.FormatInt(n, 10) }
func paymentScope(c payment.Config) string {
	provider := c.Provider
	if provider == "alipay_f2f" {
		provider = "alipay"
	}
	if provider == "epay" {
		return virtualcurrency.FingerprintForStore(provider, strings.TrimRight(c.Endpoint, "/"), c.MerchantID)
	}
	return virtualcurrency.FingerprintForStore(provider)
}
func (s *Service) provider(c payment.Config) (payment.Provider, error) {
	if s.ProviderFactory != nil {
		return s.ProviderFactory(c)
	}
	return payment.New(c, s.Client)
}
func (s *Service) Ready(ctx context.Context) error {
	state, e := s.Backend.Read(ctx)
	if e != nil {
		return e
	}
	if e = ValidateState(state); e != nil {
		return e
	}
	for _, c := range state.Settings.Channels {
		if _, e = s.Vault.Open(c.SealedConfig); e != nil {
			return e
		}
	}
	for _, o := range state.Orders {
		if o.SealedConfig != "" {
			if _, e = s.Vault.Open(o.SealedConfig); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	state, e := s.Backend.Read(ctx)
	return state.Settings, e
}
func (s *Service) UpdateSettings(ctx context.Context, c Settings) (Settings, error) {
	if e := ValidateSettings(c); e != nil {
		return Settings{}, e
	}
	for _, ch := range c.Channels {
		cfg, e := s.Vault.Open(ch.SealedConfig)
		if e != nil || cfg.Provider != ch.Provider {
			return Settings{}, ErrInvalid
		}
		if ch.Enabled {
			if _, e = s.provider(cfg); e != nil {
				return Settings{}, e
			}
		}
	}
	for _, p := range c.Products {
		if !p.Enabled {
			continue
		}
		if p.Kind == "recharge" {
			coin, e := s.Currencies.Store(ctx, p.TargetCurrency)
			if e != nil {
				return Settings{}, e
			}
			cfg, e := coin.Settings(ctx)
			if e != nil {
				return Settings{}, e
			}
			if !cfg.Enabled || p.Credit > cfg.MaxBalance {
				return Settings{}, ErrInvalid
			}
		} else {
			cfg, e := s.Members.Settings(ctx)
			if e != nil {
				return Settings{}, e
			}
			plan, ok := cfg.Plan(p.Plan)
			if !ok || !plan.Enabled || !cfg.Membership.Enabled {
				return Settings{}, ErrInvalid
			}
		}
	}
	for _, x := range c.Exchanges {
		for _, key := range []string{x.From, x.To} {
			if _, e := s.Currencies.Store(ctx, key); e != nil {
				return Settings{}, e
			}
		}
	}
	e := s.Backend.Update(ctx, func(st *State) error {
		if c.Version != st.Settings.Version {
			return ErrConflict
		}
		if c.Version == math.MaxInt64 {
			return ErrUnavailable
		}
		c.Version++
		st.Settings = c
		return nil
	})
	return c, e
}
func (s *Service) user(ctx context.Context, id int64, verified bool) error {
	if s.Users == nil {
		return ErrUnavailable
	}
	u, e := s.Users.GetUser(ctx, id)
	if e != nil {
		return e
	}
	if u.Status != auth.UserStatusActive || verified && u.EmailVerifiedAt == nil {
		return progression.ErrIneligible
	}
	return nil
}
func (s *Service) Order(ctx context.Context, user int64, id string) (Order, error) {
	state, e := s.Backend.Read(ctx)
	if e != nil {
		return Order{}, e
	}
	o, ok := state.Orders[id]
	if !ok || user != 0 && o.UserID != user {
		return Order{}, ErrNotFound
	}
	return o, nil
}
func (s *Service) Orders(ctx context.Context, user int64) ([]Order, error) {
	state, e := s.Backend.Read(ctx)
	if e != nil {
		return nil, e
	}
	out := []Order{}
	for _, o := range state.Orders {
		if user == 0 || o.UserID == user {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}

type Purchase struct {
	UserID                      int64
	Version                     int64
	Product, Channel, RequestID string
	Accepted                    bool
	ClientIP                    string
}

func (s *Service) Create(ctx context.Context, in Purchase) (Order, error) {
	if in.UserID < 1 || !virtualcurrency.ValidRequestID(in.RequestID) || !in.Accepted {
		return Order{}, ErrInvalid
	}
	state, e := s.Backend.Read(ctx)
	if e != nil {
		return Order{}, e
	}
	cfg := state.Settings
	if e = s.user(ctx, in.UserID, cfg.RequireVerifiedEmail); e != nil {
		return Order{}, e
	}
	fp := virtualcurrency.FingerprintForStore("payment", in.Product, in.Channel, stringID(in.Version))
	for _, o := range state.Orders {
		if o.UserID == in.UserID && o.RequestID == in.RequestID {
			if o.Fingerprint != fp {
				return Order{}, ErrReused
			}
			return s.checkout(ctx, o, in.ClientIP)
		}
	}
	if !cfg.Enabled {
		return Order{}, ErrDisabled
	}
	if in.Version != cfg.Version {
		return Order{}, ErrConflict
	}
	var p Product
	var ch Channel
	for _, v := range cfg.Products {
		if v.Key == in.Product {
			p = v
		}
	}
	for _, v := range cfg.Channels {
		if v.Key == in.Channel {
			ch = v
		}
	}
	if !p.Enabled || !ch.Enabled || p.Currency != ch.Currency {
		return Order{}, ErrInvalid
	}
	pc, e := s.Vault.Open(ch.SealedConfig)
	if e != nil {
		return Order{}, e
	}
	if _, e = s.provider(pc); e != nil {
		return Order{}, e
	}
	precision, e := Exponent(p.Currency)
	if e != nil {
		return Order{}, e
	}
	now := time.Now().UTC()
	o := Order{ID: NewID(), UserID: in.UserID, RequestID: in.RequestID, Fingerprint: fp, Kind: p.Kind, Title: p.Title, Product: p.Key, Channel: ch.Key, Provider: ch.Provider, PaymentScope: paymentScope(pc), SealedConfig: ch.SealedConfig, Status: "pending", Amount: p.Price, Currency: p.Currency, Precision: precision, TargetCurrency: p.TargetCurrency, Credit: p.Credit, Plan: p.Plan, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Duration(cfg.OrderMinutes) * time.Minute), Disclaimer: cfg.Disclaimer, RefundPolicy: cfg.RefundPolicy, SettingsVersion: cfg.Version}
	if p.Kind == "recharge" {
		coin, e := s.Currencies.Store(ctx, p.TargetCurrency)
		if e != nil {
			return o, e
		}
		c, e := coin.Settings(ctx)
		if e != nil {
			return o, e
		}
		balance, e := coin.Balance(ctx, in.UserID)
		if e != nil {
			return o, e
		}
		if !c.Enabled || o.Credit > c.MaxBalance-balance {
			return o, virtualcurrency.ErrBalanceLimit
		}
		o.TargetPrecision = c.Precision
	} else {
		snap, e := s.Members.Snapshot(ctx, in.UserID, 0)
		if e != nil {
			return o, e
		}
		plan, ok := snap.Settings.Plan(p.Plan)
		if !ok || !plan.Enabled || !snap.Settings.Membership.Enabled {
			return o, ErrDisabled
		}
		if snap.Account.PendingRequest != "" {
			return o, progression.ErrPending
		}
		if snap.Account.Membership.Active(now) && snap.Account.Membership.Plan != p.Plan {
			return o, progression.ErrMemberConflict
		}
		o.Days = plan.Days
	}
	e = s.Backend.Update(ctx, func(st *State) error {
		if st.Settings.Version != cfg.Version {
			return ErrConflict
		}
		pending := 0
		for _, old := range st.Orders {
			if old.UserID == in.UserID {
				if old.RequestID == in.RequestID {
					if old.Fingerprint != fp {
						return ErrReused
					}
					o = old
					return nil
				}
				if old.Status == "pending" && !old.ExpiresAt.Before(now) {
					pending++
				}
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
func (s *Service) checkout(ctx context.Context, o Order, ip string) (Order, error) {
	var err error
	o, err = s.Order(ctx, 0, o.ID)
	if err != nil {
		return o, err
	}
	if o.Status == "pending" && !o.ExpiresAt.After(time.Now()) {
		return o, ErrPending
	}
	if o.Kind == "resource" && o.Status == "pending" && s.ValidateResource != nil {
		if err := s.ValidateResource(ctx, o); err != nil {
			if !errors.Is(err, ErrResourceDelivery) {
				return o, err
			}
			closed, persist := s.closeUncreated(ctx, o.ID)
			if persist != nil {
				return o, persist
			}
			if !closed {
				persist = s.Backend.Update(ctx, func(st *State) error {
					v := st.Orders[o.ID]
					if v.Status == "pending" {
						now := time.Now().UTC()
						if v.ExpiresAt.After(now) {
							v.ExpiresAt = now
						}
						v.ErrorCode, v.UpdatedAt = "resource_delivery_unavailable", now
						st.Orders[v.ID] = v
					}
					return nil
				})
				if persist != nil {
					return o, persist
				}
			}
			o, persist = s.Order(ctx, 0, o.ID)
			if persist != nil {
				return o, persist
			}
			return o, ErrResourceDelivery
		}
	}
	if o.Status != "pending" || o.Checkout.URL != "" || o.Checkout.QRCode != "" {
		return o, nil
	}
	claimed := false
	e := s.Backend.Update(ctx, func(st *State) error {
		v, ok := st.Orders[o.ID]
		if !ok {
			return ErrNotFound
		}
		o = v
		if v.Status != "pending" || v.Checkout.URL != "" || v.Checkout.QRCode != "" || v.LeaseUntil.After(time.Now()) {
			return nil
		}
		if v.ExpiresAt.Before(time.Now()) {
			return ErrPending
		}
		if v.CheckoutAttempts > 0 && (v.Provider == "twocheckout" || v.Provider == "paypal" && time.Since(v.CreatedAt) > 5*time.Hour) {
			return ErrPending
		}
		if v.CheckoutAttempts >= 100 {
			return ErrPending
		}
		v.CheckoutAttempts++
		v.LeaseUntil = time.Now().Add(90 * time.Second)
		st.Orders[o.ID] = v
		claimed = true
		return nil
	})
	if e != nil || !claimed {
		return o, e
	}
	cfg, e := s.Vault.Open(o.SealedConfig)
	if e != nil {
		return o, e
	}
	provider, e := s.provider(cfg)
	if e != nil {
		return o, e
	}
	base := strings.TrimRight(s.PublicURL, "/")
	returnURL := base + "/billing/?order=" + o.ID
	if o.Kind == "shop" {
		returnURL = base + "/shop/orders/?order=" + o.ShopOrder
	}
	expires := time.Time{}
	if o.Kind == "resource" {
		expires = o.ExpiresAt
	}
	checkout, e := provider.Create(ctx, payment.Request{OrderID: o.ID, Amount: o.Amount, Currency: o.Currency, Description: o.Title, NotifyURL: base + "/api/v1/payments/notify/" + o.ID, ReturnURL: returnURL, CancelURL: returnURL, ClientIP: ip, ExpiresAt: expires})
	if e == nil && (checkout.URL != "" && !SafeCheckoutURL(checkout.URL) || checkout.URL == "" && checkout.QRCode == "") {
		e = payment.ErrResponse
	}
	cause := e
	persist := s.Backend.Update(ctx, func(st *State) error {
		v := st.Orders[o.ID]
		v.LeaseUntil = time.Time{}
		v.UpdatedAt = time.Now().UTC()
		if cause != nil {
			v.ErrorCode = "checkout_pending"
			if v.Provider == "twocheckout" {
				v.ErrorCode = "checkout_unknown"
			}
		} else {
			v.Checkout = checkout
			v.ErrorCode = ""
		}
		st.Orders[o.ID] = v
		o = v
		return nil
	})
	if persist != nil {
		return o, persist
	}
	return o, cause
}
func (s *Service) apply(ctx context.Context, id string, n payment.Notification) (Order, error) {
	var o Order
	e := s.Backend.Update(ctx, func(st *State) error {
		v, ok := st.Orders[id]
		if !ok {
			return ErrNotFound
		}
		o = v
		// Bind every verified event before acknowledging it, including events that
		// do not change payment state. Re-read checkout binding under this lock.
		if n.OrderID != id || n.Amount != v.Amount || n.Currency != v.Currency || n.Status == payment.Paid && n.TransactionID == "" {
			return ErrUnverified
		}
		if v.Checkout.ProviderOrderID != "" && v.Checkout.ProviderOrderID != n.ProviderOrderID {
			return ErrUnverified
		}
		if n.TransactionID != "" {
			if v.TransactionID != "" && v.TransactionID != n.TransactionID {
				return ErrUnverified
			}
			for other, x := range st.Orders {
				if other != id && x.PaymentScope == v.PaymentScope && x.TransactionID == n.TransactionID {
					return ErrUnverified
				}
			}
		}
		if n.Status == payment.Paid {
			if v.Status == "refunded" || v.Status == "review" {
				return nil
			}
			if v.Status != "completed" {
				v.Status = "paid"
				if v.PaidAt.IsZero() {
					v.PaidAt = time.Now().UTC()
				}
			}
		} else if n.Status == payment.Refunded {
			if v.Status == "refunded" {
				return nil
			}
			v.Status = "review"
			v.ErrorCode = "refund_requires_reconciliation"
		} else if n.Status == payment.Failed && n.Closed && v.Kind == "resource" {
			if v.Status == "completed" || v.Status == "paid" || !v.PaidAt.IsZero() {
				v.Status, v.ErrorCode = "review", "payment_closed_after_payment"
			} else if v.Status == "pending" || v.Status == "cancelled" {
				v.Status, v.ErrorCode = "cancelled", "payment_closed"
				v.PaymentClosedAt = time.Now().UTC()
			} else {
				return nil
			}
		} else {
			return nil
		}
		if n.TransactionID != "" {
			v.TransactionID = n.TransactionID
		}
		v.UpdatedAt = time.Now().UTC()
		st.Orders[id] = v
		o = v
		return nil
	})
	if e != nil {
		return o, e
	}
	if n.Status == payment.Paid && o.Status == "paid" {
		return s.Fulfill(ctx, id)
	}
	if n.Status == payment.Refunded && o.Kind == "shop" && s.ReviewShop != nil {
		return o, s.ReviewShop(ctx, shopSettlement(o))
	}
	return o, nil
}
func (s *Service) Notify(ctx context.Context, id string, h http.Header, body []byte) (payment.Notification, error) {
	o, e := s.Order(ctx, 0, id)
	if e != nil {
		return payment.Notification{}, e
	}
	cfg, e := s.Vault.Open(o.SealedConfig)
	if e != nil {
		return payment.Notification{}, e
	}
	p, e := s.provider(cfg)
	if e != nil {
		return payment.Notification{}, e
	}
	var n payment.Notification
	inspected := false
	if reader, ok := p.(payment.NotificationInspector); o.Kind == "resource" && ok {
		n, e = reader.InspectNotification(ctx, h, body)
		inspected = true
	} else if o.Kind == "resource" && o.Provider == "paypal" {
		return n, ErrUnavailable
	} else {
		n, e = p.Verify(ctx, h, body)
	}
	if e != nil {
		return n, e
	}
	_, e = s.apply(ctx, id, n)
	if e == nil && inspected && n.Status == payment.Pending {
		// PayPal approval is verified first. Sync checks current delivery and
		// rechecks the order immediately before any capture request.
		_, e = s.Sync(ctx, o.UserID, id)
		if errors.Is(e, ErrResourceDelivery) {
			e = nil
		}
	}
	return n, e
}
func (s *Service) Sync(ctx context.Context, user int64, id string) (Order, error) {
	o, e := s.Order(ctx, user, id)
	if e != nil {
		return o, e
	}
	if e = s.user(ctx, o.UserID, false); e != nil {
		return o, e
	}
	if o.Kind == "exchange" {
		return s.finishExchange(ctx, o)
	}
	if o.Status == "paid" {
		return s.Fulfill(ctx, id)
	}
	if o.Status == "completed" || o.Status == "review" || o.Status == "refunded" {
		return o, nil
	}
	cfg, e := s.Vault.Open(o.SealedConfig)
	if e != nil {
		return o, e
	}
	p, e := s.provider(cfg)
	if e != nil {
		return o, e
	}
	inspect := o.Kind == "resource" && (o.Status == "cancelled" || !o.ExpiresAt.After(time.Now()))
	if o.Kind == "resource" && !inspect && s.ValidateResource != nil {
		if err := s.ValidateResource(ctx, o); err != nil {
			if !errors.Is(err, ErrResourceDelivery) {
				return o, err
			}
			inspect = true
		}
	}
	var n payment.Notification
	if reader, ok := p.(payment.Inspector); inspect && ok {
		n, e = reader.Inspect(ctx, o.Checkout.ProviderOrderID, id)
	} else if inspect && o.Provider == "paypal" {
		return o, ErrUnavailable
	} else {
		queryCtx := ctx
		if o.Kind == "resource" {
			var cancel context.CancelFunc
			queryCtx, cancel = context.WithDeadline(ctx, o.ExpiresAt)
			defer cancel()
			queryCtx = payment.WithCaptureCheck(queryCtx, func(ctx context.Context, n payment.Notification) error {
				return s.checkResourceCapture(ctx, id, n)
			})
		}
		n, e = p.Query(queryCtx, o.Checkout.ProviderOrderID, id)
	}
	if e != nil {
		return o, e
	}
	return s.apply(ctx, id, n)
}

// Fulfillment is a recoverable saga: retain paid state until the independent
// ledger acknowledges the deterministic receipt. A crash cannot double-credit.
func (s *Service) Fulfill(ctx context.Context, id string) (Order, error) {
	o, err := s.Order(ctx, 0, id)
	if err != nil || o.Status != "paid" {
		return o, err
	}
	// Never hold the commerce transaction while acquiring a wallet/member pool
	// connection. Stable ledger receipts recover a crash between the two commits.
	cause := s.user(ctx, o.UserID, false)
	if cause == nil && o.Kind == "resource" {
		if !o.ResourceDeadline.IsZero() && !time.Now().Before(o.ResourceDeadline) {
			cause = ErrResourceDelivery
		} else if s.ValidateResource != nil {
			cause = s.ValidateResource(ctx, o)
		}
	} else if cause == nil && o.Kind == "shop" {
		if s.SettleShop == nil {
			cause = ErrUnavailable
		} else {
			cause = s.SettleShop(ctx, shopSettlement(o))
			o.ShopSettled = cause == nil
		}
	} else if cause == nil && o.Kind == "membership" {
		r, e := s.Members.ApplyPaidMembership(ctx, o.UserID, o.Plan, o.Days, "paid-member-"+o.ID, "支付订单 "+o.ID)
		cause = e
		o.MemberEntry = r.Entry.ID
	} else if cause == nil {
		coin, e := s.Currencies.Store(ctx, o.TargetCurrency)
		cause = e
		if cause == nil {
			cfg, e := coin.Settings(ctx)
			cause = e
			if cause == nil && cfg.Precision != o.TargetPrecision {
				cause = ErrConflict
			}
			if cause == nil {
				entry, e := coin.Credit(ctx, virtualcurrency.CreditInput{UserID: o.UserID, Amount: o.Credit, ExpectedVersion: cfg.Version, Source: "支付订单 " + o.ID, RequestID: "paid-credit-" + o.ID})
				cause = e
				o.CreditEntry = entry.ID
			}
		}
	}
	err = s.Backend.Update(ctx, func(st *State) error {
		v := st.Orders[id]
		v.LeaseUntil = time.Time{}
		v.UpdatedAt = time.Now().UTC()
		// A refund can race the independent ledger commit. Preserve both facts and
		// require reconciliation instead of losing the receipt or hiding the refund.
		if o.CreditEntry > 0 {
			v.CreditEntry = o.CreditEntry
		}
		if o.MemberEntry > 0 {
			v.MemberEntry = o.MemberEntry
		}
		if o.ShopSettled {
			v.ShopSettled = true
		}
		if v.Status == "paid" {
			if errors.Is(cause, ErrResourceDelivery) {
				v.Status, v.ErrorCode = "review", "resource_delivery_requires_reconciliation"
			} else if v.Kind == "resource" && resourceDuplicate(*st, v) {
				v.Status, v.ErrorCode = "review", "resource_duplicate_payment"
			} else if cause != nil {
				v.ErrorCode = "fulfillment_pending"
			} else {
				v.Status = "completed"
				v.CompletedAt = time.Now().UTC()
				v.ErrorCode = ""
			}
		}
		st.Orders[id] = v
		o = v
		return nil
	})
	if err != nil {
		return o, err
	}
	if o.Status == "review" && errors.Is(cause, ErrResourceDelivery) {
		// Acknowledge the durably recorded payment. It needs reconciliation, not
		// another provider retry or a falsely successful delivery notification.
		cause = nil
	}
	return o, cause
}

type ExchangeInput struct {
	UserID, Version, Amount int64
	Rule, RequestID         string
	Accepted                bool
}

func ExchangeAmount(amount int64, r Exchange) (int64, error) {
	if amount < r.Minimum || amount > r.Maximum || r.FromUnits <= 0 || r.ToUnits <= 0 || r.FeeBPS < 0 || r.FeeBPS > 5000 {
		return 0, ErrInvalid
	}
	n := new(big.Int).Mul(big.NewInt(amount), big.NewInt(r.ToUnits))
	n.Mul(n, big.NewInt(int64(10000-r.FeeBPS)))
	d := new(big.Int).Mul(big.NewInt(r.FromUnits), big.NewInt(10000))
	n.Quo(n, d)
	if !n.IsInt64() || n.Sign() <= 0 {
		return 0, ErrInvalid
	}
	return n.Int64(), nil
}

func matchesExchangeRequest(o Order, in ExchangeInput, fp string) bool {
	if o.Fingerprint == fp {
		return true
	}
	// A migrated integer order retains its original fingerprint. Accept the
	// equivalent tenths amount only when all original request terms still match.
	if o.Kind != "exchange" || o.SourceCurrency != "main" || o.SourcePrecision != 1 || o.Precision != 1 || o.Amount%10 != 0 || in.Amount != o.Amount || in.Rule != o.Product || in.Version != o.SettingsVersion {
		return false
	}
	return o.Fingerprint == virtualcurrency.FingerprintForStore("exchange", in.Rule, stringID(in.Amount/10), stringID(in.Version))
}

func (s *Service) Exchange(ctx context.Context, in ExchangeInput) (Order, error) {
	if !in.Accepted || !virtualcurrency.ValidRequestID(in.RequestID) {
		return Order{}, ErrInvalid
	}
	state, e := s.Backend.Read(ctx)
	if e != nil {
		return Order{}, e
	}
	c := state.Settings
	if e = s.user(ctx, in.UserID, c.RequireVerifiedEmail); e != nil {
		return Order{}, e
	}
	fp := virtualcurrency.FingerprintForStore("exchange", in.Rule, stringID(in.Amount), stringID(in.Version))
	for _, o := range state.Orders {
		if o.UserID == in.UserID && o.RequestID == in.RequestID {
			if !matchesExchangeRequest(o, in, fp) {
				return Order{}, ErrReused
			}
			return s.finishExchange(ctx, o)
		}
	}
	if !c.Enabled {
		return Order{}, ErrDisabled
	}
	if c.Version != in.Version {
		return Order{}, ErrConflict
	}
	var r Exchange
	for _, x := range c.Exchanges {
		if x.Key == in.Rule {
			r = x
		}
	}
	if !r.Enabled {
		return Order{}, ErrDisabled
	}
	credit, e := ExchangeAmount(in.Amount, r)
	if e != nil {
		return Order{}, e
	}
	from, e := s.Currencies.Store(ctx, r.From)
	if e != nil {
		return Order{}, e
	}
	to, e := s.Currencies.Store(ctx, r.To)
	if e != nil {
		return Order{}, e
	}
	fc, e := from.Settings(ctx)
	if e != nil {
		return Order{}, e
	}
	tc, e := to.Settings(ctx)
	if e != nil {
		return Order{}, e
	}
	balance, e := to.Balance(ctx, in.UserID)
	if e != nil {
		return Order{}, e
	}
	if !fc.Enabled || !tc.Enabled || credit > tc.MaxBalance-balance {
		return Order{}, virtualcurrency.ErrBalanceLimit
	}
	now := time.Now().UTC()
	o := Order{ID: NewID(), UserID: in.UserID, RequestID: in.RequestID, Fingerprint: fp, Kind: "exchange", Title: r.Title, Product: r.Key, Status: "pending", Amount: in.Amount, Currency: r.From, Precision: fc.Precision, SourceCurrency: r.From, SourceVersion: fc.Version, SourcePrecision: fc.Precision, TargetCurrency: r.To, TargetPrecision: tc.Precision, Credit: credit, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Duration(c.OrderMinutes) * time.Minute), SettingsVersion: c.Version, Disclaimer: c.Disclaimer, RefundPolicy: c.RefundPolicy}
	e = s.Backend.Update(ctx, func(st *State) error {
		if st.Settings.Version != c.Version {
			return ErrConflict
		}
		for _, v := range st.Orders {
			if v.UserID == in.UserID && v.RequestID == in.RequestID {
				if !matchesExchangeRequest(v, in, fp) {
					return ErrReused
				}
				o = v
				return nil
			}
		}
		st.Orders[o.ID] = o
		return nil
	})
	if e != nil {
		return o, e
	}
	return s.finishExchange(ctx, o)
}
func (s *Service) finishExchange(ctx context.Context, o Order) (Order, error) {
	current, e := s.Order(ctx, 0, o.ID)
	if e != nil {
		return o, e
	}
	o = current
	if o.Status == "paid" {
		return s.Fulfill(ctx, o.ID)
	}
	if o.Status != "pending" {
		return o, nil
	}
	from, e := s.Currencies.Store(ctx, o.SourceCurrency)
	if e != nil {
		return o, e
	}
	spend, ok := from.(virtualcurrency.SpendingStore)
	if !ok {
		return o, ErrUnavailable
	}
	cfg, e := from.Settings(ctx)
	if e != nil {
		return o, e
	}
	in := virtualcurrency.SpendInput{UserID: o.UserID, Amount: o.Amount, ExpectedVersion: o.SourceVersion, RequestID: "exchange-debit-" + o.ID, Reason: "币种兑换 " + o.ID}
	var entry virtualcurrency.Entry
	if o.ExpiresAt.Before(time.Now()) || cfg.Precision != o.SourcePrecision {
		entry, _, e = spend.CancelSpend(ctx, in)
	} else {
		entry, _, e = spend.Spend(ctx, in)
		if errors.Is(e, virtualcurrency.ErrInsufficient) || errors.Is(e, virtualcurrency.ErrConflict) || errors.Is(e, virtualcurrency.ErrDisabled) {
			entry, _, e = spend.CancelSpend(ctx, in)
		}
	}
	if e != nil {
		return o, e
	}
	e = s.Backend.Update(ctx, func(st *State) error {
		v := st.Orders[o.ID]
		if v.Status != "pending" {
			o = v
			return nil
		}
		v.SourceEntry = entry.ID
		v.UpdatedAt = time.Now().UTC()
		if entry.Kind == virtualcurrency.PurchaseCanceled {
			v.Status = "cancelled"
		} else {
			v.Status = "paid"
			v.PaidAt = time.Now().UTC()
		}
		st.Orders[o.ID] = v
		o = v
		return nil
	})
	if e != nil {
		return o, e
	}
	if o.Status == "paid" {
		return s.Fulfill(ctx, o.ID)
	}
	return o, nil
}
