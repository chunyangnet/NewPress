package progression

import (
	"context"
	"newpress.local/newpress/internal/virtualcurrency"
	"strings"
)

// ApplyPaidMembership is only called by the trusted commerce fulfiller after a
// verified payment. A stable receipt makes retries safe across process crashes.
// Disabled plans still honor their already purchased term; different active
// plans and wallet reservations require resolution instead of losing time.
func (s *Store) ApplyPaidMembership(ctx context.Context, user int64, plan string, days int, request, reason string) (Result, error) {
	if user < 1 || !virtualcurrency.ValidCurrencyKey(plan) || days < 1 || days > MaxMembershipDays || !virtualcurrency.ValidRequestID(request) || !virtualcurrency.ValidReason(reason) {
		return Result{}, ErrInvalid
	}
	reason = strings.TrimSpace(reason)
	var result Result
	err := s.backend.Write(ctx, func(tx Transaction) error {
		receipt, found, err := tx.Receipt(user, request)
		if err != nil {
			return err
		}
		if found {
			e, err := tx.Entry(receipt.EntryID)
			if err != nil {
				return err
			}
			if e.UserID != user || e.Membership.Plan != plan || e.Days != days || e.Reason != reason || (e.Kind != "membership_grant" && e.Kind != "membership_renew") {
				return ErrReused
			}
			result = Result{Entry: e, Replayed: true}
			return nil
		}
		cfg, err := tx.Settings()
		if err != nil {
			return err
		}
		if _, ok := cfg.Plan(plan); !ok {
			return ErrInvalid
		}
		a, err := tx.Account(user)
		if err != nil {
			return err
		}
		if a.PendingRequest != "" {
			return ErrPending
		}
		now := s.now().UTC()
		action := "grant"
		if a.Membership.Active(now) {
			if a.Membership.Plan != plan {
				return ErrMemberConflict
			}
			action = "renew"
		}
		a.Membership, err = memberAction(a.Membership, action, plan, days, now)
		if err != nil {
			return err
		}
		e := Entry{UserID: user, ActorID: user, Kind: "membership_" + action, Membership: a.Membership, Days: days, Reason: reason, CreatedAt: now}
		fp, err := entryFingerprint(e)
		if err != nil {
			return err
		}
		result, err = appendReceipt(tx, a, e, user, request, fp)
		return err
	})
	return result, err
}
