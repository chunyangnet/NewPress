package audit

import (
	"fmt"
	"time"
)

// Resolve closes pending transactions after startup. A matching fact hash is
// treated as a completed mutation; any other hash is explicitly aborted.
// Unknown fact keys are left pending so a caller can resolve stores in stages.
func (ledger *Ledger) Resolve(facts map[string]string) error {
	if ledger == nil {
		return ErrLedgerUnavailable
	}
	ledger.applyMu.Lock()
	defer ledger.applyMu.Unlock()
	return ledger.resolveFacts(facts)
}

func (ledger *Ledger) resolveFacts(facts map[string]string) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.fault != nil {
		return ledger.fault
	}
	pending := make([]pendingRecord, 0, len(ledger.pending))
	for _, item := range ledger.pending {
		pending = append(pending, item)
	}
	for index := 1; index < len(pending); index++ {
		for cursor := index; cursor > 0 && pending[cursor].record.Sequence < pending[cursor-1].record.Sequence; cursor-- {
			pending[cursor], pending[cursor-1] = pending[cursor-1], pending[cursor]
		}
	}
	for _, item := range pending {
		currentHash, known := facts[item.record.FactKey]
		if !known {
			continue
		}
		resolved := item.record
		resolved.Sequence = ledger.sequence + 1
		resolved.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
		resolved.PreviousHash = ledger.lastHash
		if currentHash == item.record.FactHash {
			resolved.Phase = "commit"
		} else {
			resolved.Phase = "abort"
		}
		resolved.Hash = ""
		if err := ledger.appendLocked(resolved); err != nil {
			ledger.fault = fmt.Errorf("%w: resolve: %v", ErrLedgerUnavailable, err)
			return ledger.fault
		}
	}
	return nil
}

// ResolvePaths hashes each primary fact file, falling back to its .bak only
// when the primary is absent. It resolves interrupted transactions and then
// verifies every fact that already has a committed audit record. A corrupt
// primary is never silently replaced by an older backup.
func (ledger *Ledger) ResolvePaths(paths map[string]string) error {
	if ledger == nil {
		return ErrLedgerUnavailable
	}
	ledger.applyMu.Lock()
	defer ledger.applyMu.Unlock()
	facts := make(map[string]string, len(paths))
	for key, path := range paths {
		digest, err := hashPathOrMissing(path)
		if err != nil {
			return fmt.Errorf("hash audit fact %s: %w", key, err)
		}
		facts[key] = digest
	}
	if err := ledger.resolveFacts(facts); err != nil {
		return err
	}
	return ledger.verifyFacts(facts)
}

// EnsureBaselinePath anchors an already validated legacy fact the first time
// auditing is enabled. Later calls verify rather than rewrite the baseline.
func (ledger *Ledger) HasFact(key string) bool {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	_, exists := ledger.latestCommittedLocked(key)
	return exists
}

func (ledger *Ledger) EnsureBaselinePath(factKey, path string) error {
	if ledger == nil {
		return ErrLedgerUnavailable
	}
	ledger.applyMu.Lock()
	defer ledger.applyMu.Unlock()
	digest, err := hashPathOrMissing(path)
	if err != nil {
		return err
	}
	ledger.mu.Lock()
	if err := ledger.readyLocked(); err != nil {
		ledger.mu.Unlock()
		return err
	}
	latest, exists := ledger.latestCommittedLocked(factKey)
	ledger.mu.Unlock()
	if exists {
		if latest != digest {
			return fmt.Errorf("%w: %s", ErrFactMismatch, factKey)
		}
		return nil
	}
	transaction, err := ledger.Prepare(factKey, Event{
		Action: "audit.fact.baseline", ObjectType: "storage_fact", ObjectID: factKey, Actor: "system",
	}, digest)
	if err != nil {
		return err
	}
	return ledger.Commit(transaction)
}

// VerifyFacts compares supplied current hashes against the latest committed
// record for each fact. Facts without a baseline are intentionally ignored.
func (ledger *Ledger) VerifyFacts(facts map[string]string) error {
	if ledger == nil {
		return ErrLedgerUnavailable
	}
	ledger.applyMu.Lock()
	defer ledger.applyMu.Unlock()
	return ledger.verifyFacts(facts)
}

func (ledger *Ledger) verifyFacts(facts map[string]string) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.fault != nil {
		return ledger.fault
	}
	for factKey, currentHash := range facts {
		if latest, exists := ledger.latestCommittedLocked(factKey); exists && latest != currentHash {
			ledger.fault = fmt.Errorf("%w: %s", ErrFactMismatch, factKey)
			return ledger.fault
		}
	}
	return nil
}

func (ledger *Ledger) latestCommittedLocked(factKey string) (string, bool) {
	for index := len(ledger.records) - 1; index >= 0; index-- {
		item := ledger.records[index]
		if item.FactKey == factKey && item.Phase == "commit" {
			return item.FactHash, true
		}
	}
	return "", false
}

func (ledger *Ledger) setFault(err error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.fault == nil {
		ledger.fault = err
	}
}
