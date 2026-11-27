package audit

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultReadLimit = 20
	MaxReadLimit     = 100
	maxCursorBytes   = 128
	maxFilterBytes   = 160
)

var (
	ErrInvalidQuery  = errors.New("invalid audit query")
	ErrInvalidCursor = errors.New("invalid audit cursor")
	publicToken      = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,160}$`)
	publicObjectID   = regexp.MustCompile(`^[0-9]{1,20}$`)
	publicCircleID   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}(:[1-9][0-9]{0,18})?$`)
)

// Reader is the common, read-only audit surface used by the administration
// layer. Implementations must return newest events first and must never expose
// raw metadata, request identifiers, or storage-specific hash-chain details.
type Reader interface {
	List(context.Context, Query) (Page, error)
}

type Query struct {
	Limit      int
	Cursor     string
	Action     string
	ObjectType string
}

type Page struct {
	Events     []Entry
	Total      int
	NextCursor string
	HasMore    bool
}

// Entry is already a safe projection. Actor is intentionally a coarse label
// (system/operator) so a username, email address, or other identity metadata
// cannot escape through a generic audit reader.
type Entry struct {
	ID         uint64
	CreatedAt  time.Time
	Action     string
	ObjectType string
	ObjectID   string
	Actor      string
}

func NormalizeQuery(query Query) (Query, error) {
	if query.Limit == 0 {
		query.Limit = DefaultReadLimit
	}
	if query.Limit < 1 || query.Limit > MaxReadLimit {
		return Query{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidQuery, MaxReadLimit)
	}
	if len(query.Cursor) > maxCursorBytes {
		return Query{}, fmt.Errorf("%w: cursor is too long", ErrInvalidQuery)
	}
	if strings.TrimSpace(query.Cursor) != "" {
		if _, _, err := DecodeCursor(query.Cursor); err != nil {
			return Query{}, fmt.Errorf("%w: %v", ErrInvalidQuery, err)
		}
		query.Cursor = strings.TrimSpace(query.Cursor)
	} else {
		query.Cursor = ""
	}
	for name, value := range map[string]string{"action": query.Action, "object_type": query.ObjectType} {
		value = strings.TrimSpace(value)
		if len(value) > maxFilterBytes || (value != "" && !publicToken.MatchString(value)) {
			return Query{}, fmt.Errorf("%w: invalid %s filter", ErrInvalidQuery, name)
		}
		if name == "action" {
			query.Action = value
		} else {
			query.ObjectType = value
		}
	}
	return query, nil
}

func EncodeCursor(at time.Time, id uint64) string {
	if at.IsZero() || id == 0 {
		return ""
	}
	var raw [16]byte
	binary.BigEndian.PutUint64(raw[:8], uint64(at.UnixNano()))
	binary.BigEndian.PutUint64(raw[8:], id)
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func DecodeCursor(value string) (time.Time, uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxCursorBytes {
		return time.Time{}, 0, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 16 {
		return time.Time{}, 0, ErrInvalidCursor
	}
	nanos := int64(binary.BigEndian.Uint64(raw[:8]))
	id := binary.BigEndian.Uint64(raw[8:])
	if nanos <= 0 || id == 0 {
		return time.Time{}, 0, ErrInvalidCursor
	}
	at := time.Unix(0, nanos).UTC()
	if at.IsZero() {
		return time.Time{}, 0, ErrInvalidCursor
	}
	return at, id, nil
}

func HasCursor(query Query) bool { return strings.TrimSpace(query.Cursor) != "" }

// SafeEntry converts storage fields into the intentionally small public
// projection. It is used by both file and PostgreSQL readers.
func SafeEntry(id uint64, createdAt time.Time, action, objectType, objectID, actor string) Entry {
	visibleID := safeObjectID(objectID)
	if (objectType == "community_circle" || objectType == "community_topic") && publicCircleID.MatchString(objectID) {
		visibleID = objectID
	}
	return Entry{
		ID:         id,
		CreatedAt:  createdAt.UTC(),
		Action:     safeToken(action, "unknown.action"),
		ObjectType: safeToken(objectType, "unknown"),
		ObjectID:   visibleID,
		Actor:      safeActor(actor),
	}
}

func safeToken(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxFilterBytes || !publicToken.MatchString(value) {
		return fallback
	}
	return value
}

func safeObjectID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if publicObjectID.MatchString(value) {
		return value
	}
	switch value {
	case "all", "bulk", FactUsers, FactComments, FactNavigation, FactCommunity:
		return value
	default:
		return "redacted"
	}
}

func safeActor(value string) string {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "system") || strings.EqualFold(value, "migration") {
		return strings.ToLower(value)
	}
	if value == "" {
		return "unknown"
	}
	// Keep a deterministic coarse category without returning usernames,
	// addresses, request labels, or other identity-bearing strings.
	return "operator"
}

func eventCursor(event Entry) string {
	return EncodeCursor(event.CreatedAt, event.ID)
}

func finishPage(events []Entry, total, limit int) Page {
	page := Page{Events: events, Total: total}
	if len(events) > limit {
		page.Events = events[:limit]
		page.HasMore = true
		page.NextCursor = eventCursor(page.Events[len(page.Events)-1])
	}
	return page
}

func beforeCursor(event Entry, at time.Time, id uint64) bool {
	return event.CreatedAt.Before(at) || (event.CreatedAt.Equal(at) && event.ID < id)
}

func insertNewest(events []Entry, event Entry, maximum int) []Entry {
	index := 0
	for index < len(events) {
		current := events[index]
		if event.CreatedAt.After(current.CreatedAt) || (event.CreatedAt.Equal(current.CreatedAt) && event.ID > current.ID) {
			break
		}
		index++
	}
	if index >= maximum {
		return events
	}
	events = append(events, Entry{})
	copy(events[index+1:], events[index:])
	events[index] = event
	if len(events) > maximum {
		events = events[:maximum]
	}
	return events
}
