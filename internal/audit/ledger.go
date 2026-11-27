// Package audit provides a small, append-only, tamper-evident ledger for the
// local file-storage mode. It deliberately records bounded metadata rather
// than application payloads. A prepare record is written before a fact file is
// replaced and a commit record is written afterwards; startup can therefore
// distinguish an abandoned prepare from a fact replacement that completed just
// before a process failure.
package audit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

const (
	schema       = 1
	kind         = "newpress/audit"
	maxLineBytes = 256 << 10
	maxFileBytes = 256 << 20
	maxMetadata  = 32 << 10
	maxRecords   = 1_000_000

	FactUsers      = "users"
	FactComments   = "comments"
	FactNavigation = "navigation"
	FactCommunity  = "community"
	FileName       = "audit.jsonl"
)

var (
	ErrCorrupt           = errors.New("audit ledger is corrupt")
	ErrPending           = errors.New("audit ledger has an unresolved transaction")
	ErrFactCommitted     = errors.New("fact was committed but audit commit is pending")
	ErrFactMismatch      = errors.New("audit fact does not match its last committed hash")
	ErrLedgerUnavailable = errors.New("audit ledger is unavailable")
	identifierPattern    = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
	hexPattern           = regexp.MustCompile(`^[0-9a-f]{64}$`)
	transactionPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Event is the non-sensitive description of a durable mutation. Metadata is
// intentionally bounded and is checked for sensitive field names before it is
// written to disk.
type Event struct {
	Action     string
	ObjectType string
	ObjectID   string
	Actor      string
	Metadata   map[string]any
}

// Record is a read-only projection useful for diagnostics and tests.
type Record struct {
	Schema       int
	Kind         string
	Sequence     uint64
	Transaction  string
	Phase        string
	CreatedAt    time.Time
	FactKey      string
	FactHash     string
	Action       string
	ObjectType   string
	ObjectID     string
	Actor        string
	Metadata     map[string]any
	PreviousHash string
	Hash         string
}

// Txn identifies a prepared mutation. Values are opaque to callers.
type Txn struct {
	transaction string
	factKey     string
}

type record struct {
	Schema       int            `json:"schema"`
	Kind         string         `json:"kind"`
	Sequence     uint64         `json:"sequence"`
	Transaction  string         `json:"transaction"`
	Phase        string         `json:"phase"`
	CreatedAt    time.Time      `json:"created_at"`
	FactKey      string         `json:"fact_key"`
	FactHash     string         `json:"fact_hash"`
	Action       string         `json:"action"`
	ObjectType   string         `json:"object_type"`
	ObjectID     string         `json:"object_id"`
	Actor        string         `json:"actor"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	PreviousHash string         `json:"previous_hash,omitempty"`
	Hash         string         `json:"hash"`
}

type pendingRecord struct {
	record record
}

// ApplyError identifies which part of the two-file commit protocol failed.
// A fact or audit failure leaves the ledger pending until the next startup
// resolves it; callers should enter a read-only/fault state in that case.
type ApplyError struct {
	Phase string
	Err   error
}

func (err *ApplyError) Error() string { return fmt.Sprintf("audit apply %s: %v", err.Phase, err.Err) }

func (err *ApplyError) Unwrap() error { return err.Err }

// Ledger is safe for concurrent callers. Store-level locks still serialize
// candidate construction and fact replacement; the ledger lock only protects
// the shared append stream.
type Ledger struct {
	applyMu  sync.Mutex
	mu       sync.Mutex
	path     string
	identity os.FileInfo
	sequence uint64
	lastHash string
	records  []record
	pending  map[string]pendingRecord
	fault    error
}

// Open validates or creates an append-only ledger. Existing data is never
// silently truncated or repaired.
func Open(path string) (*Ledger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("audit ledger path must not be empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve audit ledger path: %w", err)
	}
	if err := packagefs.MkdirAllNoSymlink(filepath.Dir(absolute), 0o750); err != nil {
		return nil, fmt.Errorf("create audit ledger directory: %w", err)
	}
	if err := ensureLedgerFile(absolute); err != nil {
		return nil, err
	}
	ledger := &Ledger{path: absolute, pending: make(map[string]pendingRecord)}
	if err := ledger.load(); err != nil {
		return nil, err
	}
	return ledger, nil
}

// Pending reports whether a fact still has an unresolved prepare record.
func (ledger *Ledger) Pending(factKey string) bool {
	if ledger == nil {
		return false
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	for _, item := range ledger.pending {
		if item.record.FactKey == factKey {
			return true
		}
	}
	return false
}

// Ready waits for an active two-file transaction to finish, then returns the
// current chain/fault state without mutating it. A pending transaction observed
// after acquiring applyMu is abandoned or faulted rather than merely in flight.
func (ledger *Ledger) Ready() error {
	if ledger == nil {
		return ErrLedgerUnavailable
	}
	ledger.applyMu.Lock()
	defer ledger.applyMu.Unlock()
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.readyLocked()
}

func (ledger *Ledger) readyLocked() error {
	if ledger.fault != nil {
		return ledger.fault
	}
	if len(ledger.pending) > 0 {
		return ErrPending
	}
	return nil
}

// Records returns a defensive copy of the validated ledger.
func (ledger *Ledger) Records() []Record {
	if ledger == nil {
		return nil
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	result := make([]Record, 0, len(ledger.records))
	for _, item := range ledger.records {
		result = append(result, projectRecord(item))
	}
	return result
}

// List returns only committed mutation events from the already validated
// in-memory ledger. Prepare/abort records, transaction identifiers, metadata,
// fact hashes, and chain hashes never enter the public projection.
func (ledger *Ledger) List(ctx context.Context, query Query) (Page, error) {
	if ledger == nil {
		return Page{}, ErrLedgerUnavailable
	}
	normalized, err := NormalizeQuery(query)
	if err != nil {
		return Page{}, err
	}
	var cursorAt time.Time
	var cursorID uint64
	if HasCursor(normalized) {
		cursorAt, cursorID, err = DecodeCursor(normalized.Cursor)
		if err != nil {
			return Page{}, err
		}
	}

	ledger.applyMu.Lock()
	defer ledger.applyMu.Unlock()
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.readyLocked(); err != nil {
		return Page{}, err
	}

	total := 0
	events := make([]Entry, 0, normalized.Limit+1)
	for index := len(ledger.records) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return Page{}, err
		}
		item := ledger.records[index]
		if item.Phase != "commit" {
			continue
		}
		if normalized.Action != "" && item.Action != normalized.Action {
			continue
		}
		if normalized.ObjectType != "" && item.ObjectType != normalized.ObjectType {
			continue
		}
		total++
		event := SafeEntry(item.Sequence, item.CreatedAt, item.Action, item.ObjectType, item.ObjectID, item.Actor)
		if !cursorAt.IsZero() && !beforeCursor(event, cursorAt, cursorID) {
			continue
		}
		events = insertNewest(events, event, normalized.Limit+1)
	}
	return finishPage(events, total, normalized.Limit), nil
}
