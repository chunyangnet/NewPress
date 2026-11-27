package audit

import (
	"errors"
	"fmt"
	"time"
)

// Prepare appends a durable prepare record and returns a transaction handle.
func (ledger *Ledger) Prepare(factKey string, event Event, factHash string) (Txn, error) {
	if ledger == nil {
		return Txn{}, ErrLedgerUnavailable
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.readyLocked(); err != nil {
		return Txn{}, err
	}
	normalized, err := normalizeEvent(factKey, event, factHash)
	if err != nil {
		return Txn{}, err
	}
	for _, pending := range ledger.pending {
		if pending.record.FactKey == factKey {
			return Txn{}, fmt.Errorf("%w: fact %s", ErrPending, factKey)
		}
	}
	transaction, err := newTransactionID()
	if err != nil {
		return Txn{}, err
	}
	item := record{
		Schema: schema, Kind: kind, Sequence: ledger.sequence + 1, Transaction: transaction,
		Phase: "prepare", CreatedAt: time.Now().UTC().Truncate(time.Microsecond), FactKey: factKey,
		FactHash: factHash, Action: normalized.Action, ObjectType: normalized.ObjectType, ObjectID: normalized.ObjectID,
		Actor: normalized.Actor, Metadata: normalized.Metadata, PreviousHash: ledger.lastHash,
	}
	if err := ledger.appendLocked(item); err != nil {
		ledger.fault = fmt.Errorf("%w: prepare: %v", ErrLedgerUnavailable, err)
		return Txn{}, ledger.fault
	}
	return Txn{transaction: transaction, factKey: factKey}, nil
}

// Commit closes a prepared transaction after the fact file has been replaced.
func (ledger *Ledger) Commit(transaction Txn) error {
	if ledger == nil {
		return ErrLedgerUnavailable
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.fault != nil {
		return ledger.fault
	}
	pending, ok := ledger.pending[transaction.transaction]
	if !ok || pending.record.FactKey != transaction.factKey {
		return fmt.Errorf("%w: transaction is not pending", ErrPending)
	}
	item := pending.record
	item.Sequence = ledger.sequence + 1
	item.Phase = "commit"
	item.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	item.PreviousHash = ledger.lastHash
	item.Hash = ""
	if err := ledger.appendLocked(item); err != nil {
		ledger.fault = fmt.Errorf("%w: commit: %v", ErrFactCommitted, err)
		return ledger.fault
	}
	return nil
}

// Abort closes a prepared transaction whose fact replacement did not happen.
func (ledger *Ledger) Abort(transaction Txn) error {
	if ledger == nil {
		return ErrLedgerUnavailable
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.fault != nil {
		return ledger.fault
	}
	pending, ok := ledger.pending[transaction.transaction]
	if !ok || pending.record.FactKey != transaction.factKey {
		return fmt.Errorf("%w: transaction is not pending", ErrPending)
	}
	item := pending.record
	item.Sequence = ledger.sequence + 1
	item.Phase = "abort"
	item.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	item.PreviousHash = ledger.lastHash
	item.Hash = ""
	if err := ledger.appendLocked(item); err != nil {
		ledger.fault = fmt.Errorf("%w: abort: %v", ErrLedgerUnavailable, err)
		return ledger.fault
	}
	return nil
}

// Apply executes the prepare -> fact write -> commit protocol. If the fact
// write or the commit cannot be confirmed, the pending transaction is retained
// and the caller must stop accepting further mutations until Resolve runs.
func (ledger *Ledger) Apply(factKey string, event Event, payload []byte, write func() error) error {
	if ledger == nil {
		if write == nil {
			return errors.New("audit fact writer is nil")
		}
		return write()
	}
	ledger.applyMu.Lock()
	defer ledger.applyMu.Unlock()
	transaction, err := ledger.Prepare(factKey, event, Digest(payload))
	if err != nil {
		return &ApplyError{Phase: "prepare", Err: err}
	}
	if write == nil {
		return &ApplyError{Phase: "fact", Err: errors.New("audit fact writer is nil")}
	}
	if err := write(); err != nil {
		return &ApplyError{Phase: "fact", Err: err}
	}
	if err := ledger.Commit(transaction); err != nil {
		return &ApplyError{Phase: "commit", Err: err}
	}
	return nil
}

// ApplyFile executes the two-file protocol and returns whether the fact write
// became visible. A late writer error is checked against the candidate digest;
// this avoids reporting a clean abort when an atomic replacement actually
// completed before a final chmod/close error.
func (ledger *Ledger) ApplyFile(factKey string, event Event, payload []byte, path string, write func() error) (bool, error) {
	if ledger == nil {
		if write == nil {
			return false, errors.New("audit fact writer is nil")
		}
		if err := write(); err != nil {
			return false, err
		}
		return true, nil
	}
	ledger.applyMu.Lock()
	defer ledger.applyMu.Unlock()
	transaction, err := ledger.Prepare(factKey, event, Digest(payload))
	if err != nil {
		return false, &ApplyError{Phase: "prepare", Err: err}
	}
	if write == nil {
		return false, &ApplyError{Phase: "fact", Err: errors.New("audit fact writer is nil")}
	}
	writeErr := write()
	if writeErr != nil {
		currentHash, hashErr := hashPathOrMissing(path)
		if hashErr != nil {
			ledger.setFault(fmt.Errorf("%w: inspect fact after write failure: %v", ErrLedgerUnavailable, hashErr))
			return false, &ApplyError{Phase: "fact", Err: errors.Join(writeErr, hashErr)}
		}
		if currentHash != Digest(payload) {
			if abortErr := ledger.Abort(transaction); abortErr != nil {
				return false, &ApplyError{Phase: "abort", Err: errors.Join(writeErr, abortErr)}
			}
			return false, &ApplyError{Phase: "fact", Err: writeErr}
		}
		return true, &ApplyError{Phase: "fact", Err: writeErr}
	}
	if err := ledger.Commit(transaction); err != nil {
		return true, &ApplyError{Phase: "commit", Err: err}
	}
	return true, nil
}
