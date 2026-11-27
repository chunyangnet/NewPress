package audit

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLedgerListReturnsCommittedSafeEventsWithCursorPagination(t *testing.T) {
	t.Parallel()
	ledger, err := Open(t.TempDir() + string(filepath.Separator) + FileName)
	if err != nil {
		t.Fatal(err)
	}
	for index, event := range []Event{
		{Action: "user.created", ObjectType: "user", ObjectID: "41", Actor: "private-owner@example.test", Metadata: map[string]any{"roles": []any{"author"}}},
		{Action: "comment.status.approved", ObjectType: "comment", ObjectID: "9", Actor: "private-moderator", Metadata: map[string]any{"status": "approved"}},
		{Action: "navigation.menu.create", ObjectType: "navigation_menu", ObjectID: "7", Actor: "system", Metadata: map[string]any{"item_count": 0}},
	} {
		payload := []byte{byte(index + 1)}
		if err := ledger.Apply(FactUsers, event, payload, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := ledger.Prepare(FactComments, Event{Action: "comment.delete", ObjectType: "comment", ObjectID: "10", Actor: "operator"}, Digest([]byte("pending")))
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Abort(pending); err != nil {
		t.Fatal(err)
	}

	first, err := ledger.List(t.Context(), Query{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 3 || len(first.Events) != 2 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	if first.Events[0].Action != "navigation.menu.create" || first.Events[0].Actor != "system" ||
		first.Events[1].Action != "comment.status.approved" || first.Events[1].Actor != "operator" {
		t.Fatalf("first page projection = %#v", first.Events)
	}
	second, err := ledger.List(t.Context(), Query{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if second.Total != 3 || len(second.Events) != 1 || second.HasMore || second.NextCursor != "" || second.Events[0].Action != "user.created" {
		t.Fatalf("second page = %#v", second)
	}
	if second.Events[0].Actor != "operator" || strings.Contains(second.Events[0].Actor, "private") {
		t.Fatalf("actor was not coarsened: %#v", second.Events[0])
	}

	entryType := reflect.TypeOf(Entry{})
	for _, forbidden := range []string{"Metadata", "RequestID", "Transaction", "Hash", "FactHash", "PreviousHash"} {
		if _, ok := entryType.FieldByName(forbidden); ok {
			t.Fatalf("safe entry unexpectedly exposes %s", forbidden)
		}
	}
}

func TestLedgerListFiltersAndRejectsInvalidQueries(t *testing.T) {
	t.Parallel()
	ledger, err := Open(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []Event{
		{Action: "user.created", ObjectType: "user", ObjectID: "1", Actor: "system"},
		{Action: "user.updated", ObjectType: "user", ObjectID: "1", Actor: "owner"},
		{Action: "comment.created", ObjectType: "comment", ObjectID: "2", Actor: "system"},
	} {
		if err := ledger.Apply(FactUsers, event, []byte(event.Action), func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	page, err := ledger.List(t.Context(), Query{ObjectType: "user", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Events) != 2 || page.Events[0].Action != "user.updated" || page.Events[1].Action != "user.created" {
		t.Fatalf("filtered page = %#v", page)
	}
	for _, query := range []Query{
		{Limit: MaxReadLimit + 1},
		{Cursor: "not-a-cursor"},
		{Action: "user created"},
		{ObjectType: strings.Repeat("x", maxFilterBytes+1)},
	} {
		if _, err := ledger.List(t.Context(), query); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("query %#v error = %v", query, err)
		}
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ledger.List(cancelled, Query{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled list error = %v", err)
	}
}

func TestCursorRoundTripAndSafeEntryRedaction(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 22, 1, 2, 3, 456789, time.UTC)
	encoded := EncodeCursor(at, 42)
	decodedAt, decodedID, err := DecodeCursor(encoded)
	if err != nil || !decodedAt.Equal(at) || decodedID != 42 {
		t.Fatalf("cursor round trip = %s %s %d %v", encoded, decodedAt, decodedID, err)
	}
	entry := SafeEntry(1, at, "bad action", "", "private@example.test", "private@example.test")
	if entry.Action != "unknown.action" || entry.ObjectType != "unknown" || entry.ObjectID != "redacted" || entry.Actor != "operator" {
		t.Fatalf("safe entry = %#v", entry)
	}
}
