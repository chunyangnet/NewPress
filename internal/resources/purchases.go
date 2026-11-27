package resources

import (
	"context"
	"errors"
	"strconv"
	"time"
)

var ErrPurchaseChanged = errors.New("resource purchase changed")

// PurchaseTerms binds an immutable purchase to a condition, independent of its
// current price. A receipt in the original ledger is still required for access.
type PurchaseTerms struct {
	PostID    int64      `json:"post_id"`
	Resource  string     `json:"resource"`
	Variant   string     `json:"variant"`
	Condition string     `json:"condition"`
	Kind      string     `json:"kind"`
	Currency  string     `json:"currency"`
	Amount    int64      `json:"amount"`
	EndsAt    *time.Time `json:"ends_at,omitempty"`
}

func Terms(post int64, r Resource, v Variant, c Condition) PurchaseTerms {
	return PurchaseTerms{PostID: post, Resource: r.ID, Variant: v.ID, Condition: c.ID,
		Kind: c.Kind, Currency: c.Currency, Amount: c.Amount, EndsAt: r.EndsAt}
}

func (t PurchaseTerms) Valid() bool {
	return t.PostID > 0 && ValidID(t.Resource) && ValidID(t.Variant) && ValidID(t.Condition) &&
		(t.Kind == "points" || t.Kind == "payment") && ValidID(t.Currency) && t.Amount > 0 && t.Amount <= 1_000_000_000_000
}

func (t PurchaseTerms) Key() string {
	return PurchaseKey(t.PostID, Resource{ID: t.Resource}, Variant{ID: t.Variant}, Condition{ID: t.Condition, Kind: t.Kind})
}

func (t PurchaseTerms) LegacyKey() string {
	return ConditionKey(t.PostID, Resource{ID: t.Resource}, Variant{ID: t.Variant},
		Condition{ID: t.Condition, Kind: t.Kind, Currency: t.Currency, Amount: t.Amount})
}

// PointPurchase is a recoverable debit reservation. Completed and cancelled
// statuses are projections of the wallet receipt, never standalone grants.
type PointPurchase struct {
	UserID          int64         `json:"user_id"`
	Terms           PurchaseTerms `json:"terms"`
	Attempt         int           `json:"attempt"`
	Legacy          bool          `json:"legacy,omitempty"`
	CurrencyVersion int64         `json:"currency_version"`
	Precision       int           `json:"precision"`
	Status          string        `json:"status"`
	PaymentEntry    int64         `json:"payment_entry,omitempty"`
	Title           string        `json:"title"`
	CreatedAt       time.Time     `json:"created_at"`
}

func (p PointPurchase) Key() string { return Hash(p.UserID, p.Terms.Key()) }

type PointDebit struct {
	UserID, Amount, ExpectedVersion int64
	RequestID, Reason               string
}

// PointReceipt is the trusted wallet acknowledgement. The caller verifies the
// ledger fingerprint and debit before committing this projection.
type PointReceipt struct {
	ID, UserID, Amount int64
	RequestID          string
	Paid               bool
	CreatedAt          time.Time
}

func (p PointPurchase) Payment() PointDebit {
	key := Hash("resource-point-attempt-v2", p.Terms.Key(), p.Attempt)
	reason := "文章资源 v2 " + p.Terms.Key()[:48] + " " + strconv.Itoa(p.Attempt)
	if p.Legacy {
		key = p.Terms.LegacyKey()
		reason = "文章资源 " + key[:48]
	}
	return PointDebit{UserID: p.UserID, Amount: p.Terms.Amount, ExpectedVersion: p.CurrencyVersion,
		RequestID: "resource-" + key[:48], Reason: reason}
}

func (p PointPurchase) Valid() bool {
	if p.UserID < 1 || !p.Terms.Valid() || p.Terms.Kind != "points" || p.Attempt < 1 || p.Attempt > 1_000_000 ||
		p.CurrencyVersion < 1 || p.Precision < 0 || p.Precision > 6 || p.CreatedAt.IsZero() || p.PaymentEntry < 0 || !ValidID(p.Payment().RequestID) {
		return false
	}
	return p.Status == "pending" && p.PaymentEntry == 0 || (p.Status == "completed" || p.Status == "cancelled") && p.PaymentEntry > 0
}

// BeginPointPurchase pins one wallet and amount before any debit. A cancelled
// attempt can be replaced only after CancelSpend sealed its original receipt.
func (s *Store) BeginPointPurchase(ctx context.Context, p PointPurchase) (PointPurchase, error) {
	p.Status, p.PaymentEntry, p.Legacy = "pending", 0, false
	p.Attempt = 1
	err := s.Backend.Update(ctx, func(st *State) error {
		if st.Purchases == nil {
			st.Purchases = map[string]PointPurchase{}
		}
		if old, ok := st.Purchases[p.Key()]; ok {
			if old.Status != "cancelled" {
				p = old
				return nil
			}
			p.Attempt = old.Attempt + 1
		}
		if !p.Valid() {
			return ErrInvalid
		}
		st.Purchases[p.Key()] = p
		return nil
	})
	return p, err
}

func (s *Store) FinishPointPurchase(ctx context.Context, p PointPurchase, entry PointReceipt) (PointPurchase, error) {
	if !p.Valid() || entry.ID < 1 || entry.UserID != p.UserID || entry.Amount != p.Terms.Amount || entry.RequestID != p.Payment().RequestID || entry.CreatedAt.IsZero() {
		return p, ErrInvalid
	}
	err := s.Backend.Update(ctx, func(st *State) error {
		current, ok := st.Purchases[p.Key()]
		if !ok || current.Payment().RequestID != p.Payment().RequestID || Hash(current.Terms) != Hash(p.Terms) {
			return ErrPurchaseChanged
		}
		p = current
		p.PaymentEntry = entry.ID
		p.Status = "completed"
		if !entry.Paid {
			p.Status = "cancelled"
		}
		st.Purchases[p.Key()] = p
		if p.Status == "completed" {
			for _, old := range st.Unlocks {
				if old.Actor == "u:"+strconv.FormatInt(p.UserID, 10) && old.PostID == p.Terms.PostID && old.Resource == p.Terms.Resource && old.Variant == p.Terms.Variant {
					return nil
				}
			}
			st.Unlocks = append(st.Unlocks, Unlock{Actor: "u:" + strconv.FormatInt(p.UserID, 10), PostID: p.Terms.PostID,
				Resource: p.Terms.Resource, Variant: p.Terms.Variant, Title: p.Title, CreatedAt: entry.CreatedAt})
		}
		return nil
	})
	return p, err
}
