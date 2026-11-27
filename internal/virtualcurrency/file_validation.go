package virtualcurrency

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
)

func rejectDuplicateJSON(body []byte) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return ErrInvalid
		}
		token, err := d.Token()
		if err != nil {
			return ErrInvalid
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				// encoding/json otherwise treats differently cased field names as
				// aliases, allowing a duplicate to bypass a literal-name check.
				if err != nil || !ok || seen[name] || name != strings.ToLower(name) {
					return ErrInvalid
				}
				seen[name] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return ErrInvalid
			}
		case '[':
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

// Validation is linear in ledger size. Every operation owns exactly its listed
// entries, each transfer conserves value, and all cached balances are replayed.
func validateFileState(d fileState) error {
	cfg, err := NormalizeSettings(d.Settings)
	if err != nil || d.Schema != 3 || cfg.Version < 1 || d.Balances == nil || d.Entries == nil || d.Operations == nil ||
		d.NextEntryID != int64(len(d.Entries))+1 || d.NextOpID != int64(len(d.Operations))+1 {
		return ErrInvalid
	}
	if cfg.Enabled && (cfg.RegistrationEnabled && cfg.RegistrationSince.IsZero() || cfg.PublicationEnabled && cfg.PublicationSince.IsZero()) {
		return ErrInvalid
	}
	for key, balance := range d.Balances {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil || id < 1 || strconv.FormatInt(id, 10) != key || balance < 0 || balance > cfg.MaxBalance {
			return ErrInvalid
		}
	}
	balances := make(map[string]int64, len(d.Balances))
	seen := make(map[string]bool, len(d.Operations))
	position := 0
	for index, op := range d.Operations {
		key := opKey(op.ActorID, op.RequestID)
		if op.ID != int64(index)+1 || op.ActorID < 0 || !ValidRequestID(op.RequestID) || seen[key] || len(op.EntryIDs) < 1 || len(op.EntryIDs) > 2 || position+len(op.EntryIDs) > len(d.Entries) {
			return ErrInvalid
		}
		seen[key] = true
		entries := d.Entries[position : position+len(op.EntryIDs)]
		for i, e := range entries {
			if e.ID != int64(position+i)+1 || op.EntryIDs[i] != e.ID || e.OperationID != op.ID || e.ActorID != op.ActorID || e.UserID < 1 || e.PeerID < 0 || e.SourceID < 0 || e.CreatedAt.IsZero() || (e.Delta == 0 && e.Kind != PurchaseCanceled) || e.Delta == math.MinInt64 || !safeText(e.Reason, 0, MaxReasonRunes) {
				return ErrInvalid
			}
			if e.Kind != PurchasePayment && e.Kind != PurchaseCanceled && e.PurchaseAmount != 0 {
				return ErrInvalid
			}
			userKey := strconv.FormatInt(e.UserID, 10)
			previous := balances[userKey]
			if e.Delta < 0 && previous < -e.Delta || e.Delta > 0 && previous > math.MaxInt64-e.Delta {
				return ErrInvalid
			}
			if previous+e.Delta != e.Balance {
				return ErrInvalid
			}
			balances[userKey] = e.Balance
		}
		first := entries[0]
		var fingerprint string
		if len(entries) == 2 {
			second := entries[1]
			if first.Kind != "transfer_out" || second.Kind != "transfer_in" || first.Delta >= 0 || second.Delta != -first.Delta ||
				first.UserID == second.UserID || first.PeerID != second.UserID || second.PeerID != first.UserID ||
				first.ActorID != first.UserID || first.SourceID != 0 || second.SourceID != 0 || first.Reason != "" || second.Reason != "" || !first.CreatedAt.Equal(second.CreatedAt) {
				return ErrInvalid
			}
			fingerprint = TransferFingerprint(second.UserID, second.Delta)
		} else {
			if first.PeerID != 0 {
				return ErrInvalid
			}
			switch first.Kind {
			case PurchasePayment, PurchaseCanceled:
				in := SpendInput{UserID: first.UserID, Amount: first.PurchaseAmount, ExpectedVersion: 1, RequestID: op.RequestID, Reason: first.Reason}
				if !ValidSpend(in) || !ValidateSpendReceipt(first, in) {
					return ErrInvalid
				}
				fingerprint = SpendFingerprint(in.UserID, in.Amount, in.Reason)
			case "admin_credit", "admin_debit":
				if first.ActorID < 1 || first.SourceID != 0 || !ValidReason(first.Reason) || (first.Kind == "admin_credit") != (first.Delta > 0) {
					return ErrInvalid
				}
				fingerprint = AdjustmentFingerprint(first.UserID, first.Delta, first.Reason)
			case "credit":
				if first.ActorID != 0 || first.SourceID != 0 || first.Delta < 1 || !ValidReason(first.Reason) {
					return ErrInvalid
				}
				fingerprint = CreditFingerprint(first.UserID, first.Delta, first.Reason)
			case RewardCheckIn, RewardRegistration, RewardPublication:
				if first.Delta < 1 || first.Reason != RewardReason(first.Kind) {
					return ErrInvalid
				}
				actor, request, fp, err := RewardIdentity(RewardInput{UserID: first.UserID, SourceID: first.SourceID, Kind: first.Kind, RequestID: op.RequestID, ExpectedVersion: 1, OccurredAt: first.CreatedAt})
				if err != nil || actor != op.ActorID || request != op.RequestID {
					return ErrInvalid
				}
				fingerprint = fp
			default:
				return ErrInvalid
			}
		}
		if op.Fingerprint != fingerprint {
			return ErrInvalid
		}
		position += len(entries)
	}
	if position != len(d.Entries) || len(balances) != len(d.Balances) {
		return ErrInvalid
	}
	for key, balance := range balances {
		if value, ok := d.Balances[key]; !ok || value != balance {
			return ErrInvalid
		}
	}
	return nil
}
