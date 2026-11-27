package virtualcurrency

import (
	"context"
	"math"
	"strconv"
	"strings"
)

const PurchasePayment = "purchase"
const PurchaseCanceled = "purchase_cancel"

// Spending is a trusted server capability, not a general-purpose public debit
// endpoint. Payment and cancellation compete for the same immutable receipt.
type SpendingStore interface {
	Spend(context.Context, SpendInput) (Entry, bool, error)
	CancelSpend(context.Context, SpendInput) (Entry, bool, error)
	SpendReceipt(context.Context, SpendInput) (Entry, bool, error)
}

// PurchaseHistoryStore is an optional, read-only migration capability. It
// avoids paging unrelated rewards and transfers when locating legacy receipts.
type PurchaseHistoryStore interface {
	PurchaseHistory(context.Context, int64) ([]Entry, error)
}

func (s *FileStore) PurchaseHistory(ctx context.Context, user int64) ([]Entry, error) {
	if user < 1 {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.unchanged(); err != nil {
		return nil, err
	}
	entries := []Entry{}
	for i := len(s.data.Entries) - 1; i >= 0; i-- {
		e := s.data.Entries[i]
		if e.UserID == user && e.Kind == PurchasePayment {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

type SpendInput struct {
	UserID, Amount, ExpectedVersion int64
	RequestID, Reason               string
}

func ValidSpend(in SpendInput) bool {
	return in.UserID > 0 && in.Amount > 0 && in.ExpectedVersion > 0 && ValidRequestID(in.RequestID) && ValidReason(in.Reason)
}

func SpendFingerprint(user, amount int64, reason string) string {
	return FingerprintForStore("purchase-v1", strconv.FormatInt(user, 10), strconv.FormatInt(amount, 10), strings.TrimSpace(reason))
}

func ValidateSpendReceipt(e Entry, in SpendInput) bool {
	return e.ID > 0 && e.OperationID > 0 && e.UserID == in.UserID && e.ActorID == in.UserID && e.PeerID == 0 && e.SourceID == 0 && e.PurchaseAmount == in.Amount && e.Reason == strings.TrimSpace(in.Reason) &&
		(e.Kind == PurchasePayment && e.Delta == -in.Amount || e.Kind == PurchaseCanceled && e.Delta == 0)
}

func (s *FileStore) spendReceipt(in SpendInput) (Entry, bool, error) {
	op, found := findOperation(s.data.Operations, in.UserID, in.RequestID)
	if !found {
		return Entry{}, false, nil
	}
	if op.Fingerprint != SpendFingerprint(in.UserID, in.Amount, in.Reason) {
		return Entry{}, false, ErrIdempotencyConflict
	}
	if len(op.EntryIDs) != 1 {
		return Entry{}, false, ErrStoreUnavailable
	}
	e := findEntry(s.data.Entries, op.EntryIDs[0])
	if !ValidateSpendReceipt(e, in) {
		return Entry{}, false, ErrStoreUnavailable
	}
	return e, true, nil
}

func (s *FileStore) SpendReceipt(ctx context.Context, in SpendInput) (Entry, bool, error) {
	if !ValidSpend(in) {
		return Entry{}, false, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.unchanged(); err != nil {
		return Entry{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return Entry{}, false, err
	}
	return s.spendReceipt(in)
}

func (s *FileStore) Spend(ctx context.Context, in SpendInput) (Entry, bool, error) {
	return s.spend(ctx, in, false)
}

func (s *FileStore) CancelSpend(ctx context.Context, in SpendInput) (Entry, bool, error) {
	return s.spend(ctx, in, true)
}

func (s *FileStore) spend(ctx context.Context, in SpendInput, cancel bool) (Entry, bool, error) {
	if !ValidSpend(in) {
		return Entry{}, false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unchanged(); err != nil {
		return Entry{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return Entry{}, false, err
	}
	if prior, found, err := s.spendReceipt(in); found || err != nil {
		return prior, found, err
	}
	key := strconv.FormatInt(in.UserID, 10)
	balance := s.data.Balances[key]
	if !cancel {
		if s.data.Settings.Version != in.ExpectedVersion {
			return Entry{}, false, ErrConflict
		}
		if !s.data.Settings.Enabled {
			return Entry{}, false, ErrDisabled
		}
		if balance < in.Amount {
			return Entry{}, false, ErrInsufficient
		}
	}
	if s.data.NextEntryID >= math.MaxInt64 || s.data.NextOpID >= math.MaxInt64 {
		return Entry{}, false, ErrStoreUnavailable
	}
	d := cloneState(s.data)
	e := Entry{ID: d.NextEntryID, OperationID: d.NextOpID, UserID: in.UserID, ActorID: in.UserID, PurchaseAmount: in.Amount, Kind: PurchasePayment, Delta: -in.Amount, Balance: balance - in.Amount, Reason: strings.TrimSpace(in.Reason), CreatedAt: s.now().UTC()}
	if cancel {
		e.Kind, e.Delta, e.Balance = PurchaseCanceled, 0, balance
	}
	d.NextEntryID++
	d.NextOpID++
	d.Balances[key] = e.Balance
	d.Entries = append(d.Entries, e)
	d.Operations = append(d.Operations, Operation{ID: e.OperationID, ActorID: in.UserID, RequestID: in.RequestID, Fingerprint: SpendFingerprint(in.UserID, in.Amount, in.Reason), EntryIDs: []int64{e.ID}})
	if err := s.persist(ctx, d); err != nil {
		return Entry{}, false, err
	}
	return e, false, nil
}
