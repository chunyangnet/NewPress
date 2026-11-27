package progression

import (
	"encoding/hex"
	"math"
	"newpress.local/newpress/internal/virtualcurrency"
	"strconv"
	"strings"
	"time"
)

func isReward(kind string) bool  { return virtualcurrency.IsReward(kind) }
func validTime(t time.Time) bool { return !t.IsZero() && t.Year() >= 1970 && t.Year() <= 9999 }
func validMember(m Membership) bool {
	if m.Version == 0 {
		return m == (Membership{})
	}
	return m.Version > 0 && virtualcurrency.ValidCurrencyKey(m.Plan) && validTime(m.Since) && validTime(m.Until) && m.Until.After(m.Since)
}
func entryFingerprint(e Entry) (string, error) {
	switch e.Kind {
	case "experience_adjust":
		return fingerprint("experience-adjust", number(e.UserID), number(e.Delta), e.Reason), nil
	case virtualcurrency.RewardCheckIn:
		return fingerprint("experience-check-in", number(e.UserID)), nil
	case virtualcurrency.RewardRegistration:
		return fingerprint("experience-registration", number(e.UserID)), nil
	case virtualcurrency.RewardPublication:
		return fingerprint("experience-publication", number(e.SourceID), number(e.UserID)), nil
	case "membership_grant", "membership_renew", "membership_revoke":
		plan := e.Membership.Plan
		if e.Kind == "membership_revoke" {
			plan = ""
		}
		return fingerprint("membership-manage", number(e.UserID), strings.TrimPrefix(e.Kind, "membership_"), plan, strconv.Itoa(e.Days), e.Reason), nil
	case "membership_set":
		return fingerprint("membership-set", number(e.UserID), e.Membership.Plan, e.Membership.Since.Format(time.RFC3339Nano), e.Membership.Until.Format(time.RFC3339Nano), e.Reason), nil
	}
	return "", ErrInvalid
}
func validateState(d fileState) error {
	if d.Schema != 1 || ValidateSettings(d.Settings) != nil || d.Accounts == nil || d.Entries == nil || d.Receipts == nil || d.Orders == nil || d.NextEntryID != int64(len(d.Entries))+1 {
		return ErrInvalid
	}
	if d.Settings.Registration.Enabled && !validTime(d.Settings.Registration.Since) || d.Settings.Publication.Enabled && !validTime(d.Settings.Publication.Since) {
		return ErrInvalid
	}
	accounts := map[string]Account{}
	covered := map[int64]bool{}
	sources := map[string]bool{}
	for i, e := range d.Entries {
		if e.ID != int64(i)+1 || e.UserID < 1 || e.ActorID < 0 || !validTime(e.CreatedAt) || !virtualcurrency.ValidReason(e.Reason) || strings.TrimSpace(e.Reason) != e.Reason || e.SourceID < 0 {
			return ErrInvalid
		}
		k := number(e.UserID)
		a := accounts[k]
		a.UserID = e.UserID
		if e.Delta == math.MinInt64 || e.Delta < 0 && a.Experience < -e.Delta || e.Delta > 0 && e.Delta > math.MaxInt64-a.Experience {
			return ErrInvalid
		}
		a.Experience += e.Delta
		if e.Balance != a.Experience || e.Balance < 0 {
			return ErrInvalid
		}
		switch e.Kind {
		case "experience_adjust", virtualcurrency.RewardCheckIn, virtualcurrency.RewardRegistration, virtualcurrency.RewardPublication:
			if e.Delta == 0 || e.Membership != (Membership{}) || e.Days != 0 || e.OrderRequest != "" || e.PaymentCurrency != "" || e.PaymentEntry != 0 {
				return ErrInvalid
			}
			switch e.Kind {
			case "experience_adjust":
				if e.ActorID < 1 || e.SourceID != 0 {
					return ErrInvalid
				}
			case virtualcurrency.RewardCheckIn:
				if e.ActorID != e.UserID || e.SourceID != 0 || e.Delta < 1 {
					return ErrInvalid
				}
			case virtualcurrency.RewardRegistration:
				if e.ActorID != 0 || e.SourceID != e.UserID || e.Delta < 1 {
					return ErrInvalid
				}
			case virtualcurrency.RewardPublication:
				if e.ActorID != 0 || e.SourceID < 1 || e.Delta < 1 {
					return ErrInvalid
				}
			}
			if e.Kind == virtualcurrency.RewardRegistration || e.Kind == virtualcurrency.RewardPublication {
				source := e.Kind + ":" + number(e.SourceID)
				if sources[source] {
					return ErrInvalid
				}
				sources[source] = true
			}
		case "membership_grant", "membership_renew", "membership_revoke", "membership_purchase", "membership_set":
			if e.Delta != 0 || e.ActorID < 1 || e.SourceID != 0 || !validMember(e.Membership) {
				return ErrInvalid
			}
			if _, found := d.Settings.Plan(e.Membership.Plan); !found {
				return ErrInvalid
			}
			action := strings.TrimPrefix(e.Kind, "membership_")
			plan := e.Membership.Plan
			if action == "revoke" {
				plan = ""
				if e.Days != 0 {
					return ErrInvalid
				}
			}
			if action == "purchase" {
				o, found := d.Orders[key(e.UserID, e.OrderRequest)]
				if !found || o.Status != "completed" || o.EntryID != e.ID || o.PaymentEntry != e.PaymentEntry || o.Currency != e.PaymentCurrency || o.Plan != plan || o.Days != e.Days || o.PaymentReason != e.Reason || e.ActorID != e.UserID || !o.CompletedAt.Equal(e.CreatedAt) || a.Membership.Version != o.MemberVersion {
					return ErrInvalid
				}
				action = o.Mode
				covered[e.ID] = true
			} else if e.OrderRequest != "" || e.PaymentCurrency != "" || e.PaymentEntry != 0 {
				return ErrInvalid
			}
			var m Membership
			var err error
			if action == "set" {
				if e.Days != 0 {
					return ErrInvalid
				}
				m, err = setMembership(a.Membership, plan, e.Membership.Since, e.Membership.Until, e.CreatedAt)
			} else {
				m, err = memberAction(a.Membership, action, plan, e.Days, e.CreatedAt)
			}
			if err != nil || m != e.Membership {
				return ErrInvalid
			}
			a.Membership = m
		default:
			return ErrInvalid
		}
		accounts[k] = a
	}
	for k, r := range d.Receipts {
		if k != key(r.ActorID, r.RequestID) || r.EntryID < 1 || r.EntryID >= d.NextEntryID {
			return ErrInvalid
		}
		e := d.Entries[r.EntryID-1]
		fp, err := entryFingerprint(e)
		if err != nil || fp != r.Fingerprint || r.ActorID != e.ActorID {
			return ErrInvalid
		}
		switch e.Kind {
		case virtualcurrency.RewardRegistration:
			if r.RequestID != "xp-registration-"+number(e.UserID) {
				return ErrInvalid
			}
		case virtualcurrency.RewardPublication:
			if r.RequestID != "xp-publication-"+number(e.SourceID) {
				return ErrInvalid
			}
		default:
			if !virtualcurrency.ValidRequestID(r.RequestID) {
				return ErrInvalid
			}
		}
		if covered[e.ID] && e.Kind != virtualcurrency.RewardCheckIn {
			return ErrInvalid
		}
		covered[e.ID] = true
	}
	if len(covered) != len(d.Entries) {
		return ErrInvalid
	}
	payments := map[string]bool{}
	paidEntries := map[string]bool{}
	for k, o := range d.Orders {
		if o.TermsHash != orderTermsHash(o) {
			return ErrInvalid
		}
		in := PurchaseInput{UserID: o.UserID, ExpectedVersion: o.ConfigVersion, MemberVersion: o.MemberVersion, Plan: o.Plan, Mode: o.Mode, RequestID: o.RequestID}
		nonce, err := hex.DecodeString(strings.TrimPrefix(o.PaymentRequest, "membership-"))
		if k != key(o.UserID, o.RequestID) || !validPurchase(in) || o.Fingerprint != purchaseFingerprint(in) || o.Days < 1 || o.Days > MaxMembershipDays || !textOK(o.PlanName, 1, 40) || !virtualcurrency.ValidCurrencyKey(o.Currency) || o.CurrencyVersion < 1 || o.CurrencyPrecision < 0 || o.CurrencyPrecision > 6 || o.Amount < 1 || !textOK(o.CurrencySymbol, 1, 12) || !strings.HasPrefix(o.PaymentRequest, "membership-") || err != nil || len(nonce) != 24 || !virtualcurrency.ValidSpend(o.Payment()) || !validTime(o.CreatedAt) {
			return ErrInvalid
		}
		if _, found := d.Settings.Plan(o.Plan); !found {
			return ErrInvalid
		}
		payment := o.Currency + ":" + key(o.UserID, o.PaymentRequest)
		if payments[payment] {
			return ErrInvalid
		}
		payments[payment] = true
		a := accounts[number(o.UserID)]
		a.UserID = o.UserID
		switch o.Status {
		case "pending":
			if o.PaymentEntry != 0 || o.EntryID != 0 || !o.CompletedAt.IsZero() || a.PendingRequest != "" || a.Membership.Version != o.MemberVersion {
				return ErrInvalid
			}
			a.PendingRequest = o.RequestID
		case "completed", "cancelled":
			if o.PaymentEntry < 1 || !validTime(o.CompletedAt) || o.CompletedAt.Before(o.CreatedAt) {
				return ErrInvalid
			}
			pk := o.Currency + ":" + number(o.PaymentEntry)
			if paidEntries[pk] {
				return ErrInvalid
			}
			paidEntries[pk] = true
			if o.Status == "completed" {
				if o.EntryID < 1 || o.EntryID >= d.NextEntryID {
					return ErrInvalid
				}
				e := d.Entries[o.EntryID-1]
				if e.Kind != "membership_purchase" || e.UserID != o.UserID || e.OrderRequest != o.RequestID {
					return ErrInvalid
				}
			} else if o.EntryID != 0 {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		accounts[number(o.UserID)] = a
	}
	if len(accounts) != len(d.Accounts) {
		return ErrInvalid
	}
	for k, a := range d.Accounts {
		if k != number(a.UserID) || a.UserID < 1 || a.Experience > d.Settings.Experience.Maximum || !validMember(a.Membership) || accounts[k] != a {
			return ErrInvalid
		}
	}
	return nil
}
