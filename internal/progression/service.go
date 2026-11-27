package progression

import (
	"context"
	"errors"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/virtualcurrency"
	"time"
)

type Service struct {
	Store      *Store
	Users      virtualcurrency.Users
	Content    content.Repository
	Currencies virtualcurrency.Catalog
}

// ValidatePayments verifies restored ledgers together. It only reads receipts;
// even an interrupted pending order never causes a charge at startup.
func (s *Service) ValidatePayments(ctx context.Context) error {
	if err := s.Store.Ready(ctx); err != nil {
		return err
	}
	var orders []Order
	if err := s.Store.backend.Read(ctx, func(tx Transaction) error { var err error; orders, err = tx.Orders(); return err }); err != nil {
		return err
	}
	for _, o := range orders {
		if o.TermsHash != orderTermsHash(o) {
			return ErrUnavailable
		}
		coin, err := s.spending(ctx, o)
		if err != nil {
			return err
		}
		e, found, err := coin.SpendReceipt(ctx, o.Payment())
		if err != nil {
			return err
		}
		if o.Status == "pending" {
			continue
		}
		if !found || e.ID != o.PaymentEntry || o.Status == "completed" && e.Kind != virtualcurrency.PurchasePayment || o.Status == "cancelled" && e.Kind != virtualcurrency.PurchaseCanceled {
			return ErrUnavailable
		}
	}
	return nil
}

func (s *Service) user(ctx context.Context, id int64) (auth.User, error) {
	if s.Users == nil {
		return auth.User{}, ErrUnavailable
	}
	u, err := s.Users.GetUser(ctx, id)
	if errors.Is(err, auth.ErrUserNotFound) || err == nil && u.Status != auth.UserStatusActive {
		return u, ErrIneligible
	}
	return u, err
}
func RewardPolicyError(err error) bool {
	return errors.Is(err, ErrDisabled) || errors.Is(err, ErrIneligible) || errors.Is(err, ErrLimit) || errors.Is(err, ErrReused)
}
func (s *Service) Registration(ctx context.Context, id int64) (Result, error) {
	u, err := s.user(ctx, id)
	if err != nil {
		return Result{}, err
	}
	return s.Store.Reward(ctx, RewardInput{UserID: id, SourceID: id, Kind: virtualcurrency.RewardRegistration, OccurredAt: u.CreatedAt, VerifiedEmail: u.EmailVerifiedAt != nil})
}
func (s *Service) CheckIn(ctx context.Context, id, version int64, request string) (Result, error) {
	u, err := s.user(ctx, id)
	if err != nil {
		return Result{}, err
	}
	return s.Store.Reward(ctx, RewardInput{UserID: id, Kind: virtualcurrency.RewardCheckIn, ExpectedVersion: version, RequestID: request, VerifiedEmail: u.EmailVerifiedAt != nil})
}
func (s *Service) Observe(ctx context.Context, event activity.Event) error {
	if event.Kind != activity.Publication {
		return nil
	}
	if err := event.Validate(); err != nil {
		return err
	}
	cfg, err := s.Store.Settings(ctx)
	if err != nil {
		return err
	}
	if !cfg.Experience.Enabled || !cfg.Publication.Enabled {
		return nil
	}
	post, err := s.Content.GetByID(ctx, event.PostID)
	if errors.Is(err, content.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	now := time.Now()
	if event.Due.After(now) || !content.PublicAt(post, now) || post.Type != content.TypePost || post.AuthorID != event.UserID || post.PublishedAt.After(event.Due) {
		return nil
	}
	u, err := s.user(ctx, post.AuthorID)
	if RewardPolicyError(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.Store.Reward(ctx, RewardInput{UserID: u.ID, SourceID: post.ID, Kind: virtualcurrency.RewardPublication, OccurredAt: post.CreatedAt, VerifiedEmail: u.EmailVerifiedAt != nil, ContentLength: virtualcurrency.VisibleLength(post.ContentHTML)})
	if RewardPolicyError(err) {
		return nil
	}
	return err
}
func (s *Service) UpdateSettings(ctx context.Context, expected int64, cfg Settings) (Settings, error) {
	if err := ValidateSettings(cfg); err != nil {
		return Settings{}, err
	}
	for _, p := range cfg.Membership.Plans {
		if !p.PurchaseEnabled {
			continue
		}
		if s.Currencies == nil {
			return Settings{}, ErrUnavailable
		}
		coin, err := s.Currencies.Store(ctx, p.Currency)
		if err != nil {
			return Settings{}, err
		}
		c, err := coin.Settings(ctx)
		if err != nil {
			return Settings{}, err
		}
		amount, err := virtualcurrency.ParseAmount(p.Price, c.Precision)
		if err != nil || amount < 1 || amount > c.MaxBalance {
			return Settings{}, ErrInvalid
		}
	}
	return s.Store.UpdateSettings(ctx, expected, cfg)
}

// PreviewSettings evaluates a complete draft against the current store state.
// It is intentionally read-only: it cannot change settings, grant rewards,
// create an order, or charge a currency.
func (s *Service) PreviewSettings(ctx context.Context, cfg Settings) (SettingsPreview, error) {
	if s.Store == nil {
		return SettingsPreview{}, ErrUnavailable
	}
	current, err := s.Store.Settings(ctx)
	if err != nil {
		return SettingsPreview{}, err
	}
	cfg = CloneSettings(cfg)
	cfg.Registration.Since = current.Registration.Since
	cfg.Publication.Since = current.Publication.Since
	facts := PreviewContext{Current: current, UsedPlans: map[string]bool{}}
	if err = s.Store.backend.Read(ctx, func(tx Transaction) error {
		var readErr error
		facts.MaximumExperience, readErr = tx.MaximumExperience()
		if readErr != nil {
			return readErr
		}
		for _, old := range current.Membership.Plans {
			if _, found := cfg.Plan(old.Key); found {
				continue
			}
			used, readErr := tx.PlanUsed(old.Key)
			if readErr != nil {
				return readErr
			}
			facts.UsedPlans[old.Key] = used
		}
		return nil
	}); err != nil {
		return SettingsPreview{}, err
	}
	if s.Currencies != nil {
		items, err := s.Currencies.List(ctx)
		if err != nil {
			return SettingsPreview{}, err
		}
		for _, item := range items {
			facts.Currencies = append(facts.Currencies, CurrencyFacts{Key: item.Key, Name: item.Settings.Name, Symbol: item.Settings.Symbol, Precision: item.Settings.Precision, Enabled: item.Settings.Enabled, MaxBalance: item.Settings.MaxBalance})
		}
	}
	return AnalyzeSettings(cfg, facts), nil
}
func (s *Service) spending(ctx context.Context, order Order) (virtualcurrency.SpendingStore, error) {
	if s.Currencies == nil {
		return nil, ErrUnavailable
	}
	store, err := s.Currencies.Store(ctx, order.Currency)
	if err != nil {
		return nil, err
	}
	if err = store.Ready(ctx); err != nil {
		return nil, err
	}
	spender, ok := store.(virtualcurrency.SpendingStore)
	if !ok {
		return nil, ErrUnavailable
	}
	return spender, nil
}
func (s *Service) Purchase(ctx context.Context, in PurchaseInput) (Order, error) {
	if _, err := s.user(ctx, in.UserID); err != nil {
		return Order{}, err
	}
	order, found, err := s.Store.ExistingOrder(ctx, in)
	if err != nil {
		return order, err
	}
	if !found {
		cfg, err := s.Store.Settings(ctx)
		if err != nil {
			return order, err
		}
		p, ok := cfg.Plan(in.Plan)
		if !ok {
			return order, ErrInvalid
		}
		if s.Currencies == nil {
			return order, ErrUnavailable
		}
		coin, err := s.Currencies.Store(ctx, p.Currency)
		if err != nil {
			return order, err
		}
		c, err := coin.Settings(ctx)
		if err != nil {
			return order, err
		}
		order, err = s.Store.BeginPurchase(ctx, in, CurrencyQuote{Key: p.Currency, Symbol: c.Symbol, Version: c.Version, Precision: c.Precision, Enabled: c.Enabled})
		if err != nil {
			return order, err
		}
	}
	if order.Status != "pending" {
		return order, nil
	}
	coin, err := s.spending(ctx, order)
	if err != nil {
		return order, err
	}
	payment, _, err := coin.Spend(ctx, order.Payment())
	if err != nil {
		// Only known no-debit outcomes can initiate cancellation. A timeout or
		// failed commit leaves the reservation intact until a receipt is found.
		if !errors.Is(err, virtualcurrency.ErrInsufficient) && !errors.Is(err, virtualcurrency.ErrConflict) && !errors.Is(err, virtualcurrency.ErrDisabled) {
			return order, err
		}
		cause := err
		payment, _, err = coin.CancelSpend(ctx, order.Payment())
		if err != nil {
			return order, err
		}
		order, err = s.Store.FinishPurchase(ctx, order, payment)
		if err != nil {
			return order, err
		}
		if order.Status == "completed" {
			return order, nil
		}
		return order, cause
	}
	return s.Store.FinishPurchase(ctx, order, payment)
}
func (s *Service) Cancel(ctx context.Context, user int64, request string) (Order, error) {
	if _, err := s.Users.GetUser(ctx, user); err != nil {
		return Order{}, err
	}
	if !virtualcurrency.ValidRequestID(request) {
		return Order{}, ErrInvalid
	}
	var order Order
	var found bool
	err := s.Store.backend.Read(ctx, func(tx Transaction) error { var e error; order, found, e = tx.Order(user, request); return e })
	if err != nil {
		return order, err
	}
	if !found {
		return order, ErrInvalid
	}
	if order.Status != "pending" {
		return order, nil
	}
	coin, err := s.spending(ctx, order)
	if err != nil {
		return order, err
	}
	payment, _, err := coin.CancelSpend(ctx, order.Payment())
	if err != nil {
		return order, err
	}
	return s.Store.FinishPurchase(ctx, order, payment)
}

// Recover never authorizes a payment. Loading or synchronizing an account can
// only settle a receipt that already exists in its selected currency ledger.
func (s *Service) Recover(ctx context.Context, user int64) (Order, bool, error) {
	order, found, err := s.Store.Pending(ctx, user)
	if err != nil || !found {
		return order, false, err
	}
	return s.recoverPayment(ctx, order)
}

// Administrative review names the exact order. A newer pending purchase must
// never be substituted for the order the administrator inspected.
func (s *Service) RecoverRequest(ctx context.Context, user int64, request string) (Order, bool, error) {
	if user < 1 || !virtualcurrency.ValidRequestID(request) {
		return Order{}, false, ErrInvalid
	}
	var order Order
	var found bool
	err := s.Store.backend.Read(ctx, func(tx Transaction) error { var err error; order, found, err = tx.Order(user, request); return err })
	if err != nil {
		return order, false, err
	}
	if !found {
		return order, false, ErrInvalid
	}
	if order.Status != "pending" {
		return order, false, nil
	}
	return s.recoverPayment(ctx, order)
}

func (s *Service) recoverPayment(ctx context.Context, order Order) (Order, bool, error) {
	coin, err := s.spending(ctx, order)
	if err != nil {
		return order, false, err
	}
	payment, found, err := coin.SpendReceipt(ctx, order.Payment())
	if err != nil || !found {
		return order, false, err
	}
	order, err = s.Store.FinishPurchase(ctx, order, payment)
	return order, err == nil, err
}
func (s *Service) Sync(ctx context.Context, user int64) error {
	if _, err := s.user(ctx, user); err != nil {
		return err
	}
	_, rewardErr := s.Registration(ctx, user)
	if RewardPolicyError(rewardErr) {
		rewardErr = nil
	}
	_, _, recoveryErr := s.Recover(ctx, user)
	return errors.Join(rewardErr, recoveryErr)
}
