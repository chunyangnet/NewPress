package progression

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/virtualcurrency"
)

type Store struct {
	backend Backend
	now     func() time.Time
}

func NewStore(b Backend) *Store                  { return &Store{backend: b, now: time.Now} }
func (s *Store) Ready(ctx context.Context) error { return s.backend.Ready(ctx) }
func (s *Store) Settings(ctx context.Context) (value Settings, err error) {
	err = s.backend.Read(ctx, func(tx Transaction) error { var e error; value, e = tx.Settings(); return e })
	return
}

func (s *Store) UpdateSettings(ctx context.Context, expected int64, value Settings) (Settings, error) {
	var result Settings
	err := s.backend.Write(ctx, func(tx Transaction) error {
		current, err := tx.Settings()
		if err != nil {
			return err
		}
		if expected != current.Version {
			return ErrConflict
		}
		if current.Version == math.MaxInt64 {
			return ErrUnavailable
		}
		value = CloneSettings(value)
		value.Version = current.Version + 1
		value.Registration.Since = current.Registration.Since
		value.Publication.Since = current.Publication.Since
		now := s.now().UTC()
		if value.Registration.Enabled && value.Registration.Since.IsZero() {
			value.Registration.Since = now
		}
		if value.Publication.Enabled && value.Publication.Since.IsZero() {
			value.Publication.Since = now
		}
		if err = ValidateSettings(value); err != nil {
			return err
		}
		maximum, err := tx.MaximumExperience()
		if err != nil {
			return err
		}
		if maximum > value.Experience.Maximum {
			return ErrLimit
		}
		for _, old := range current.Membership.Plans {
			if _, found := value.Plan(old.Key); !found {
				used, err := tx.PlanUsed(old.Key)
				if err != nil {
					return err
				}
				if used {
					return ErrUsedPlan
				}
			}
		}
		if err = tx.SetSettings(value); err != nil {
			return err
		}
		result = value
		return nil
	})
	return result, err
}

func (s *Store) Snapshot(ctx context.Context, user, before int64) (Snapshot, error) {
	if user < 1 || before < 0 {
		return Snapshot{}, ErrInvalid
	}
	var result Snapshot
	err := s.backend.Read(ctx, func(tx Transaction) error {
		var err error
		result.Settings, err = tx.Settings()
		if err != nil {
			return err
		}
		result.Account, err = tx.Account(user)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		result.Benefits = result.Settings.PublicBenefits(result.Account.Membership, now)
		result.MembershipActive = result.Benefits.Active
		if result.Settings.Levels.Enabled {
			for i, level := range result.Settings.Levels.Items {
				if level.Threshold <= result.Account.Experience {
					v := level
					result.Level = &v
					if i+1 < len(result.Settings.Levels.Items) {
						next := result.Settings.Levels.Items[i+1]
						result.NextLevel = &next
					} else {
						result.NextLevel = nil
					}
				}
			}
		}
		result.Entries, err = tx.History(user, before, HistoryPage)
		if err != nil {
			return err
		}
		if len(result.Entries) == HistoryPage {
			result.NextBefore = result.Entries[len(result.Entries)-1].ID
		}
		rewards, err := tx.RewardsSince(user, result.Settings.CheckIn.Rule().Since(now))
		if err != nil {
			return err
		}
		for _, e := range rewards {
			if e.CreatedAt.After(now.Add(-24 * time.Hour)) {
				result.EarnedToday = saturate(result.EarnedToday, e.Delta)
			}
			if e.Kind == virtualcurrency.RewardCheckIn {
				next := result.Settings.CheckIn.Rule().Next(e.CreatedAt)
				if next.After(result.NextCheckIn) {
					result.NextCheckIn = next
				}
			}
		}
		if result.Account.PendingRequest != "" {
			order, found, err := tx.Order(user, result.Account.PendingRequest)
			if err != nil {
				return err
			}
			if !found || order.Status != "pending" {
				return ErrUnavailable
			}
			result.PendingOrder = &order
		}
		return nil
	})
	return result, err
}

func saturate(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}
func number(v int64) string                  { return strconv.FormatInt(v, 10) }
func key(actor int64, request string) string { return number(actor) + ":" + request }
func fingerprint(parts ...string) string     { return virtualcurrency.FingerprintForStore(parts...) }

type Result struct {
	Entry    Entry `json:"entry"`
	Replayed bool  `json:"replayed"`
}

func existingReceipt(tx Transaction, actor int64, request, fp string) (Result, bool, error) {
	r, found, err := tx.Receipt(actor, request)
	if err != nil || !found {
		return Result{}, false, err
	}
	if r.Fingerprint != fp {
		return Result{}, false, ErrReused
	}
	e, err := tx.Entry(r.EntryID)
	return Result{Entry: e, Replayed: true}, true, err
}
func appendReceipt(tx Transaction, a Account, e Entry, actor int64, request, fp string) (Result, error) {
	e.Balance = a.Experience
	entry, err := tx.Append(e)
	if err != nil {
		return Result{}, err
	}
	if err = tx.PutAccount(a); err != nil {
		return Result{}, err
	}
	if err = tx.PutReceipt(Receipt{ActorID: actor, RequestID: request, Fingerprint: fp, EntryID: entry.ID}); err != nil {
		return Result{}, err
	}
	return Result{Entry: entry}, nil
}

type Adjustment struct {
	ActorID, UserID, Delta, ExpectedVersion int64
	RequestID, Reason                       string
}

func (s *Store) Adjust(ctx context.Context, in Adjustment) (Result, error) {
	if in.ActorID < 1 || in.UserID < 1 || in.Delta == 0 || in.Delta == math.MinInt64 || in.ExpectedVersion < 1 || !virtualcurrency.ValidRequestID(in.RequestID) || !virtualcurrency.ValidReason(in.Reason) {
		return Result{}, ErrInvalid
	}
	in.Reason = strings.TrimSpace(in.Reason)
	fp := fingerprint("experience-adjust", number(in.UserID), number(in.Delta), in.Reason)
	var result Result
	err := s.backend.Write(ctx, func(tx Transaction) error {
		prior, found, err := existingReceipt(tx, in.ActorID, in.RequestID, fp)
		if err != nil || found {
			result = prior
			return err
		}
		cfg, err := tx.Settings()
		if err != nil {
			return err
		}
		if cfg.Version != in.ExpectedVersion {
			return ErrConflict
		}
		a, err := tx.Account(in.UserID)
		if err != nil {
			return err
		}
		if in.Delta > 0 && in.Delta > cfg.Experience.Maximum-a.Experience || in.Delta < 0 && a.Experience < -in.Delta {
			return ErrLimit
		}
		a.Experience += in.Delta
		result, err = appendReceipt(tx, a, Entry{UserID: in.UserID, ActorID: in.ActorID, Kind: "experience_adjust", Delta: in.Delta, Reason: in.Reason, CreatedAt: s.now().UTC()}, in.ActorID, in.RequestID, fp)
		return err
	})
	return result, err
}

type RewardInput struct {
	UserID, SourceID, ExpectedVersion int64
	Kind, RequestID                   string
	OccurredAt                        time.Time
	VerifiedEmail                     bool
	ContentLength                     int
}

func rewardIdentity(in RewardInput) (int64, string, string, error) {
	if in.UserID < 1 {
		return 0, "", "", ErrInvalid
	}
	switch in.Kind {
	case virtualcurrency.RewardCheckIn:
		if in.ExpectedVersion < 1 || !virtualcurrency.ValidRequestID(in.RequestID) {
			return 0, "", "", ErrInvalid
		}
		return in.UserID, in.RequestID, fingerprint("experience-check-in", number(in.UserID)), nil
	case virtualcurrency.RewardRegistration:
		if in.OccurredAt.IsZero() {
			return 0, "", "", ErrInvalid
		}
		return 0, "xp-registration-" + number(in.UserID), fingerprint("experience-registration", number(in.UserID)), nil
	case virtualcurrency.RewardPublication:
		if in.SourceID < 1 || in.OccurredAt.IsZero() {
			return 0, "", "", ErrInvalid
		}
		return 0, "xp-publication-" + number(in.SourceID), fingerprint("experience-publication", number(in.SourceID), number(in.UserID)), nil
	default:
		return 0, "", "", ErrInvalid
	}
}

func (s *Store) Reward(ctx context.Context, in RewardInput) (Result, error) {
	if in.Kind == virtualcurrency.RewardRegistration {
		in.SourceID = in.UserID
	}
	if in.Kind == virtualcurrency.RewardCheckIn {
		in.SourceID = 0
	}
	actor, request, fp, err := rewardIdentity(in)
	if err != nil {
		return Result{}, err
	}
	var result Result
	err = s.backend.Write(ctx, func(tx Transaction) error {
		prior, found, err := existingReceipt(tx, actor, request, fp)
		if err != nil || found {
			result = prior
			return err
		}
		cfg, err := tx.Settings()
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if !cfg.Experience.Enabled {
			return ErrDisabled
		}
		if cfg.Experience.RequireVerifiedEmail && !in.VerifiedEmail {
			return ErrIneligible
		}
		var amount int64
		switch in.Kind {
		case virtualcurrency.RewardCheckIn:
			if cfg.Version != in.ExpectedVersion {
				return ErrConflict
			}
			if !cfg.CheckIn.Enabled {
				return ErrDisabled
			}
			amount = cfg.CheckIn.Amount
		case virtualcurrency.RewardRegistration:
			if !cfg.Registration.Enabled || cfg.Registration.Since.IsZero() || in.OccurredAt.Unix() < cfg.Registration.Since.Unix() || in.OccurredAt.After(now) {
				return ErrIneligible
			}
			amount = cfg.Registration.Amount
		case virtualcurrency.RewardPublication:
			if !cfg.Publication.Enabled || cfg.Publication.Since.IsZero() || in.OccurredAt.Before(cfg.Publication.Since) || in.OccurredAt.After(now) || in.ContentLength < cfg.Publication.MinLength {
				return ErrIneligible
			}
			amount = cfg.Publication.Amount
		}
		a, err := tx.Account(in.UserID)
		if err != nil {
			return err
		}
		if cfg.Membership.Enabled && a.Membership.Active(now) {
			if plan, ok := cfg.Plan(a.Membership.Plan); ok {
				if amount > math.MaxInt64/int64(plan.ExperiencePercent) {
					return ErrLimit
				}
				amount = amount * int64(plan.ExperiencePercent) / 100
			} else {
				return ErrUnavailable
			}
		}
		rewards, err := tx.RewardsSince(in.UserID, cfg.CheckIn.Rule().Since(now))
		if err != nil {
			return err
		}
		var total int64
		publications := 0
		for _, e := range rewards {
			if in.Kind == virtualcurrency.RewardCheckIn && e.Kind == in.Kind && now.Before(cfg.CheckIn.Rule().Next(e.CreatedAt)) {
				if err = tx.PutReceipt(Receipt{ActorID: actor, RequestID: request, Fingerprint: fp, EntryID: e.ID}); err != nil {
					return err
				}
				result = Result{Entry: e, Replayed: true}
				return nil
			}
			if e.CreatedAt.After(now.Add(-24 * time.Hour)) {
				total = saturate(total, e.Delta)
				if e.Kind == virtualcurrency.RewardPublication {
					publications++
				}
			}
		}
		if amount > cfg.Experience.DailyLimit || total > cfg.Experience.DailyLimit-amount || amount > cfg.Experience.Maximum-a.Experience || in.Kind == virtualcurrency.RewardPublication && publications >= cfg.Publication.DailyLimit {
			return ErrLimit
		}
		a.Experience += amount
		reason := map[string]string{virtualcurrency.RewardCheckIn: "签到经验", virtualcurrency.RewardRegistration: "注册经验", virtualcurrency.RewardPublication: "发文经验"}[in.Kind]
		result, err = appendReceipt(tx, a, Entry{UserID: in.UserID, ActorID: actor, Kind: in.Kind, Delta: amount, SourceID: in.SourceID, Reason: reason, CreatedAt: now}, actor, request, fp)
		return err
	})
	return result, err
}

type MembershipInput struct {
	ActorID, UserID, ExpectedVersion, MemberVersion int64
	Action, Plan, RequestID, Reason                 string
	Days                                            int
	Since, Until                                    time.Time
}

func setMembership(m Membership, plan string, since, until, now time.Time) (Membership, error) {
	if m.Version == math.MaxInt64 {
		return Membership{}, ErrUnavailable
	}
	if !virtualcurrency.ValidCurrencyKey(plan) || !validTime(since) || !validTime(until) || !until.After(since) {
		return Membership{}, ErrInvalid
	}
	if until.After(now.AddDate(0, 0, MaxMembershipDays)) {
		return Membership{}, ErrLimit
	}
	return Membership{Version: m.Version + 1, Plan: plan, Since: since.UTC(), Until: until.UTC()}, nil
}

func memberAction(m Membership, action, plan string, days int, now time.Time) (Membership, error) {
	if m.Version == math.MaxInt64 {
		return Membership{}, ErrUnavailable
	}
	if action == "revoke" {
		if m.Plan == "" || m.Revoked {
			return Membership{}, ErrMemberConflict
		}
		m.Version++
		m.Revoked = true
		return m, nil
	}
	if days < 1 || days > MaxMembershipDays {
		return Membership{}, ErrInvalid
	}
	base := now
	switch action {
	case "grant":
		if m.Active(now) {
			return Membership{}, ErrMemberConflict
		}
		m.Since = now
	case "renew":
		if m.Plan != plan || m.Revoked || m.Version == 0 {
			return Membership{}, ErrMemberConflict
		}
		if m.Until.After(base) {
			base = m.Until
		} else {
			m.Since = now
		}
	default:
		return Membership{}, ErrInvalid
	}
	until := base.Add(time.Duration(days) * 24 * time.Hour)
	if until.After(now.Add(time.Duration(MaxMembershipDays) * 24 * time.Hour)) {
		return Membership{}, ErrLimit
	}
	m.Version++
	m.Plan = plan
	m.Revoked = false
	m.Until = until
	return m, nil
}

func (s *Store) ManageMembership(ctx context.Context, in MembershipInput) (Result, error) {
	if in.ActorID < 1 || in.UserID < 1 || in.ExpectedVersion < 1 || in.MemberVersion < 0 || !virtualcurrency.ValidRequestID(in.RequestID) || !virtualcurrency.ValidReason(in.Reason) || in.Action == "revoke" && (in.Plan != "" || in.Days != 0) {
		return Result{}, ErrInvalid
	}
	in.Reason = strings.TrimSpace(in.Reason)
	fp := fingerprint("membership-manage", number(in.UserID), in.Action, in.Plan, strconv.Itoa(in.Days), in.Reason)
	if in.Action == "set" {
		if in.Days != 0 {
			return Result{}, ErrInvalid
		}
		in.Since, in.Until = in.Since.UTC(), in.Until.UTC()
		fp = fingerprint("membership-set", number(in.UserID), in.Plan, in.Since.Format(time.RFC3339Nano), in.Until.Format(time.RFC3339Nano), in.Reason)
	} else if !in.Since.IsZero() || !in.Until.IsZero() {
		return Result{}, ErrInvalid
	}
	var result Result
	err := s.backend.Write(ctx, func(tx Transaction) error {
		prior, found, err := existingReceipt(tx, in.ActorID, in.RequestID, fp)
		if err != nil || found {
			result = prior
			return err
		}
		cfg, err := tx.Settings()
		if err != nil {
			return err
		}
		if cfg.Version != in.ExpectedVersion {
			return ErrConflict
		}
		a, err := tx.Account(in.UserID)
		if err != nil {
			return err
		}
		if a.PendingRequest != "" {
			return ErrPending
		}
		if a.Membership.Version != in.MemberVersion {
			return ErrMemberConflict
		}
		if in.Action != "revoke" {
			plan, found := cfg.Plan(in.Plan)
			if !cfg.Membership.Enabled || !found || !plan.Enabled {
				return ErrDisabled
			}
		}
		now := s.now().UTC()
		if in.Action == "set" {
			a.Membership, err = setMembership(a.Membership, in.Plan, in.Since, in.Until, now)
		} else {
			a.Membership, err = memberAction(a.Membership, in.Action, in.Plan, in.Days, now)
		}
		if err != nil {
			return err
		}
		result, err = appendReceipt(tx, a, Entry{UserID: in.UserID, ActorID: in.ActorID, Kind: "membership_" + in.Action, Membership: a.Membership, Reason: in.Reason, Days: in.Days, CreatedAt: now}, in.ActorID, in.RequestID, fp)
		return err
	})
	return result, err
}

type PurchaseInput struct {
	UserID, ExpectedVersion, MemberVersion int64
	Plan, Mode, RequestID                  string
}
type CurrencyQuote struct {
	Key, Symbol string
	Version     int64
	Precision   int
	Enabled     bool
}

func purchaseFingerprint(in PurchaseInput) string {
	return fingerprint("membership-purchase", number(in.UserID), in.Plan, in.Mode, number(in.ExpectedVersion), number(in.MemberVersion))
}
func validPurchase(in PurchaseInput) bool {
	return in.UserID > 0 && in.ExpectedVersion > 0 && in.MemberVersion >= 0 && virtualcurrency.ValidCurrencyKey(in.Plan) && (in.Mode == "grant" || in.Mode == "renew") && virtualcurrency.ValidRequestID(in.RequestID)
}
func (s *Store) ExistingOrder(ctx context.Context, in PurchaseInput) (Order, bool, error) {
	if !validPurchase(in) {
		return Order{}, false, ErrInvalid
	}
	var order Order
	var found bool
	err := s.backend.Read(ctx, func(tx Transaction) error {
		var err error
		order, found, err = tx.Order(in.UserID, in.RequestID)
		if err == nil && found && order.Fingerprint != purchaseFingerprint(in) {
			return ErrReused
		}
		return err
	})
	return order, found, err
}

func (s *Store) BeginPurchase(ctx context.Context, in PurchaseInput, quote CurrencyQuote) (Order, error) {
	if !validPurchase(in) {
		return Order{}, ErrInvalid
	}
	var result Order
	err := s.backend.Write(ctx, func(tx Transaction) error {
		prior, found, err := tx.Order(in.UserID, in.RequestID)
		if err != nil {
			return err
		}
		if found {
			if prior.Fingerprint != purchaseFingerprint(in) {
				return ErrReused
			}
			result = prior
			return nil
		}
		cfg, err := tx.Settings()
		if err != nil {
			return err
		}
		if cfg.Version != in.ExpectedVersion {
			return ErrConflict
		}
		plan, found := cfg.Plan(in.Plan)
		if !cfg.Membership.Enabled || !found || !plan.Enabled || !plan.PurchaseEnabled || !quote.Enabled {
			return ErrDisabled
		}
		if quote.Key != plan.Currency || quote.Version < 1 || quote.Precision < 0 || quote.Precision > 6 {
			return ErrInvalid
		}
		amount, err := virtualcurrency.ParseAmount(plan.Price, quote.Precision)
		if err != nil || amount < 1 {
			return ErrInvalid
		}
		a, err := tx.Account(in.UserID)
		if err != nil {
			return err
		}
		if a.PendingRequest != "" {
			return ErrPending
		}
		if a.Membership.Version != in.MemberVersion {
			return ErrMemberConflict
		}
		now := s.now().UTC()
		if _, err = memberAction(a.Membership, in.Mode, in.Plan, plan.Days, now); err != nil {
			return err
		}
		var nonce [24]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return err
		}
		result = Order{UserID: in.UserID, RequestID: in.RequestID, Fingerprint: purchaseFingerprint(in), Status: "pending", Mode: in.Mode, Plan: plan.Key, PlanName: plan.Name, Days: plan.Days, ConfigVersion: cfg.Version, MemberVersion: a.Membership.Version, Currency: quote.Key, CurrencyVersion: quote.Version, CurrencyPrecision: quote.Precision, CurrencySymbol: quote.Symbol, Amount: amount, PaymentRequest: "membership-" + hex.EncodeToString(nonce[:]), PaymentReason: "会员" + map[string]string{"grant": "开通", "renew": "续费"}[in.Mode] + "：" + plan.Name, CreatedAt: now}
		a.PendingRequest = in.RequestID
		result.TermsHash = orderTermsHash(result)
		if err = tx.PutOrder(result); err != nil {
			return err
		}
		return tx.PutAccount(a)
	})
	return result, err
}

func orderTermsHash(o Order) string {
	return fingerprint("membership-terms-v1", number(o.UserID), o.RequestID, o.Fingerprint, o.Mode, o.Plan, o.PlanName, strconv.Itoa(o.Days), number(o.ConfigVersion), number(o.MemberVersion), o.Currency, number(o.CurrencyVersion), strconv.Itoa(o.CurrencyPrecision), o.CurrencySymbol, number(o.Amount), o.PaymentRequest, o.PaymentReason, o.CreatedAt.Format(time.RFC3339Nano))
}

// FinishPurchase only accepts an immutable payment/cancellation receipt from
// the selected currency. A cancellation receipt prevents any later charge.
func (s *Store) FinishPurchase(ctx context.Context, order Order, payment virtualcurrency.Entry) (Order, error) {
	var result Order
	err := s.backend.Write(ctx, func(tx Transaction) error {
		current, found, err := tx.Order(order.UserID, order.RequestID)
		if err != nil {
			return err
		}
		if !found || current.Fingerprint != order.Fingerprint {
			return ErrReused
		}
		if !virtualcurrency.ValidateSpendReceipt(payment, current.Payment()) {
			return ErrUnavailable
		}
		if current.Status != "pending" {
			if current.PaymentEntry != payment.ID {
				return ErrUnavailable
			}
			result = current
			return nil
		}
		a, err := tx.Account(current.UserID)
		if err != nil {
			return err
		}
		if a.PendingRequest != current.RequestID || a.Membership.Version != current.MemberVersion {
			return ErrUnavailable
		}
		current.PaymentEntry = payment.ID
		current.CompletedAt = s.now().UTC()
		a.PendingRequest = ""
		if payment.Kind == virtualcurrency.PurchaseCanceled {
			current.Status = "cancelled"
		} else {
			a.Membership, err = memberAction(a.Membership, current.Mode, current.Plan, current.Days, current.CompletedAt)
			if err != nil {
				return err
			}
			e, err := tx.Append(Entry{UserID: current.UserID, ActorID: current.UserID, Kind: "membership_purchase", Balance: a.Experience, Membership: a.Membership, Reason: current.PaymentReason, Days: current.Days, OrderRequest: current.RequestID, PaymentCurrency: current.Currency, PaymentEntry: payment.ID, CreatedAt: current.CompletedAt})
			if err != nil {
				return err
			}
			current.Status = "completed"
			current.EntryID = e.ID
		}
		if err = tx.PutAccount(a); err != nil {
			return err
		}
		if err = tx.PutOrder(current); err != nil {
			return err
		}
		result = current
		return nil
	})
	return result, err
}

func (s *Store) Pending(ctx context.Context, user int64) (Order, bool, error) {
	var result Order
	var found bool
	err := s.backend.Read(ctx, func(tx Transaction) error {
		a, err := tx.Account(user)
		if err != nil {
			return err
		}
		if a.PendingRequest == "" {
			return nil
		}
		result, found, err = tx.Order(user, a.PendingRequest)
		if err == nil && (!found || result.Status != "pending") {
			return ErrUnavailable
		}
		return err
	})
	return result, found, err
}
