package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

func (ledger *Ledger) accept(item record) error {
	if err := validateRecord(item); err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if item.Sequence != ledger.sequence+1 {
		return fmt.Errorf("%w: expected sequence %d, got %d", ErrCorrupt, ledger.sequence+1, item.Sequence)
	}
	if item.PreviousHash != ledger.lastHash {
		return fmt.Errorf("%w: previous hash mismatch at sequence %d", ErrCorrupt, item.Sequence)
	}
	if got := recordHash(item); got != item.Hash {
		return fmt.Errorf("%w: hash mismatch at sequence %d", ErrCorrupt, item.Sequence)
	}
	if item.Phase == "prepare" {
		if _, exists := ledger.pending[item.Transaction]; exists {
			return fmt.Errorf("%w: duplicate pending transaction %s", ErrCorrupt, item.Transaction)
		}
		for _, pending := range ledger.pending {
			if pending.record.FactKey == item.FactKey {
				return fmt.Errorf("%w: multiple pending transactions for fact %s", ErrCorrupt, item.FactKey)
			}
		}
		ledger.pending[item.Transaction] = pendingRecord{record: item}
	} else {
		pending, exists := ledger.pending[item.Transaction]
		if !exists {
			return fmt.Errorf("%w: %s without prepare for transaction %s", ErrCorrupt, item.Phase, item.Transaction)
		}
		if !sameTransaction(pending.record, item) {
			return fmt.Errorf("%w: transaction %s changed between records", ErrCorrupt, item.Transaction)
		}
		delete(ledger.pending, item.Transaction)
	}
	ledger.sequence = item.Sequence
	ledger.lastHash = item.Hash
	ledger.records = append(ledger.records, item)
	return nil
}

func sameTransaction(left, right record) bool {
	return left.Schema == right.Schema && left.Kind == right.Kind && left.Transaction == right.Transaction &&
		left.FactKey == right.FactKey && left.FactHash == right.FactHash && left.Action == right.Action &&
		left.ObjectType == right.ObjectType && left.ObjectID == right.ObjectID && left.Actor == right.Actor &&
		metadataEqual(left.Metadata, right.Metadata)
}

func metadataEqual(left, right map[string]any) bool {
	leftBytes, _ := json.Marshal(left)
	rightBytes, _ := json.Marshal(right)
	return bytes.Equal(leftBytes, rightBytes)
}

func validateRecord(item record) error {
	if item.Schema != schema || item.Kind != kind {
		return errors.New("unsupported audit record metadata")
	}
	if item.Sequence == 0 || !validTransaction(item.Transaction) || (item.Phase != "prepare" && item.Phase != "commit" && item.Phase != "abort") {
		return errors.New("invalid audit sequence, transaction, or phase")
	}
	if item.CreatedAt.IsZero() || item.CreatedAt.Location() != time.UTC {
		return errors.New("audit timestamp must be UTC")
	}
	if !identifierPattern.MatchString(item.FactKey) || !hexPattern.MatchString(item.FactHash) ||
		!identifierPattern.MatchString(item.Action) || !identifierPattern.MatchString(item.ObjectType) {
		return errors.New("invalid audit identifiers or fact hash")
	}
	if item.ObjectID == "" || len(item.ObjectID) > 160 || containsControl(item.ObjectID) || !utf8.ValidString(item.ObjectID) {
		return errors.New("invalid audit object ID")
	}
	if item.Actor == "" || len([]rune(item.Actor)) > 160 || containsControl(item.Actor) || !utf8.ValidString(item.Actor) {
		return errors.New("invalid audit actor")
	}
	if len(item.PreviousHash) != 0 && !hexPattern.MatchString(item.PreviousHash) {
		return errors.New("invalid previous hash")
	}
	if !hexPattern.MatchString(item.Hash) {
		return errors.New("invalid record hash")
	}
	if err := validateMetadata(item.Metadata, 0); err != nil {
		return err
	}
	return nil
}

func validTransaction(value string) bool {
	return transactionPattern.MatchString(value)
}

func containsControl(value string) bool {
	for _, runeValue := range value {
		if runeValue < 0x20 || runeValue == 0x7f {
			return true
		}
	}
	return false
}

var sensitiveMetadataWords = []string{
	"password", "passwd", "secret", "token", "cookie", "session", "email", "content", "body", "ip", "useragent", "user_agent", "source", "credential",
}

func validateMetadata(value any, depth int) error {
	if depth > 4 {
		return errors.New("audit metadata is too deeply nested")
	}
	switch typed := value.(type) {
	case nil, bool, float64, json.Number:
		return nil
	case string:
		if !utf8.ValidString(typed) || containsControl(typed) || len([]rune(typed)) > 512 {
			return errors.New("audit metadata contains an invalid string")
		}
		return nil
	case []any:
		if len(typed) > 64 {
			return errors.New("audit metadata array is too large")
		}
		for _, item := range typed {
			if err := validateMetadata(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if len(typed) > 64 {
			return errors.New("audit metadata object is too large")
		}
		for key, item := range typed {
			if !utf8.ValidString(key) || containsControl(key) || len([]rune(key)) > 80 || sensitiveMetadataKey(key) {
				return fmt.Errorf("audit metadata key %q is not allowed", key)
			}
			if err := validateMetadata(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("audit metadata contains unsupported value %T", value)
	}
}

func sensitiveMetadataKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), ".", ""), "_", ""))
	for _, word := range sensitiveMetadataWords {
		if strings.Contains(normalized, strings.ReplaceAll(word, "_", "")) {
			return true
		}
	}
	return false
}

func normalizeEvent(factKey string, event Event, factHash string) (Event, error) {
	if !identifierPattern.MatchString(factKey) || !hexPattern.MatchString(factHash) {
		return Event{}, errors.New("invalid audit fact")
	}
	event.Action = strings.TrimSpace(event.Action)
	event.ObjectType = strings.TrimSpace(event.ObjectType)
	event.ObjectID = strings.TrimSpace(event.ObjectID)
	event.Actor = strings.TrimSpace(event.Actor)
	if !identifierPattern.MatchString(event.Action) || !identifierPattern.MatchString(event.ObjectType) ||
		event.ObjectID == "" || len(event.ObjectID) > 160 || containsControl(event.ObjectID) || !utf8.ValidString(event.ObjectID) ||
		event.Actor == "" || len([]rune(event.Actor)) > 160 || containsControl(event.Actor) || !utf8.ValidString(event.Actor) {
		return Event{}, errors.New("invalid audit event identifiers")
	}
	metadata, err := normalizeMetadata(event.Metadata)
	if err != nil {
		return Event{}, err
	}
	event.Metadata = metadata
	encoded, _ := json.Marshal(metadata)
	if len(encoded) > maxMetadata {
		return Event{}, fmt.Errorf("audit metadata exceeds %d bytes", maxMetadata)
	}
	return event, nil
}

func normalizeMetadata(metadata map[string]any) (map[string]any, error) {
	if metadata == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode audit metadata: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized map[string]any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("normalize audit metadata: %w", err)
	}
	if err := validateMetadata(normalized, 0); err != nil {
		return nil, err
	}
	return normalized, nil
}
