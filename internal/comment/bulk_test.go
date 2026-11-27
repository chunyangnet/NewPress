package comment

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeBulkStatusChangeBoundsPairsAndSorts(t *testing.T) {
	change, fields := NormalizeBulkStatusChange(BulkStatusChange{
		Status: " APPROVED ", Actor: " moderator ",
		Items: []BulkMutation{{ID: 9, ExpectedVersion: 3}, {ID: 2, ExpectedVersion: 1}},
	})
	if len(fields) != 0 || change.Status != StatusApproved || change.Actor != "moderator" ||
		len(change.Items) != 2 || change.Items[0].ID != 2 || change.Items[1].ID != 9 {
		t.Fatalf("normalized change = %#v, fields=%#v", change, fields)
	}

	for name, candidate := range map[string]BulkStatusChange{
		"empty":     {Status: StatusPending},
		"duplicate": {Status: StatusPending, Items: []BulkMutation{{ID: 1, ExpectedVersion: 1}, {ID: 1, ExpectedVersion: 2}}},
		"invalid":   {Status: StatusPending, Items: []BulkMutation{{ID: 0, ExpectedVersion: 1}}},
		"status":    {Status: "execute", Items: []BulkMutation{{ID: 1, ExpectedVersion: 1}}},
		"actor":     {Status: StatusPending, Actor: "bad\nactor", Items: []BulkMutation{{ID: 1, ExpectedVersion: 1}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, candidateFields := NormalizeBulkStatusChange(candidate); len(candidateFields) == 0 {
				t.Fatalf("accepted invalid bulk change %#v", candidate)
			}
		})
	}
	tooMany := BulkStatusChange{Status: StatusPending, Items: make([]BulkMutation, MaxBulkMutations+1)}
	for index := range tooMany.Items {
		tooMany.Items[index] = BulkMutation{ID: int64(index + 1), ExpectedVersion: 1}
	}
	if _, candidateFields := NormalizeBulkStatusChange(tooMany); candidateFields["selection"] == "" {
		t.Fatalf("missing selection limit error: %#v", candidateFields)
	}
	longActor := BulkStatusChange{Status: StatusPending, Actor: strings.Repeat("审", 161), Items: []BulkMutation{{ID: 1, ExpectedVersion: 1}}}
	if _, candidateFields := NormalizeBulkStatusChange(longActor); candidateFields["actor"] == "" {
		t.Fatalf("missing actor bound error: %#v", candidateFields)
	}
}

func TestFileStoreBulkChangeStatusIsAtomicVersionedAndPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "comments.json")
	store, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	create := func(name string) Comment {
		item, createErr := store.Create(t.Context(), CreateInput{
			PostID: 1, AuthorName: name, AuthorEmail: strings.ToLower(name) + "@example.test",
			Status: StatusPending, ContentSource: "原子审核 " + name,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return item
	}
	first := create("Alice")
	second := create("Bob")

	_, err = store.BulkChangeStatus(t.Context(), BulkStatusChange{
		Status: StatusApproved, Actor: "qaadmin",
		Items: []BulkMutation{{ID: first.ID, ExpectedVersion: first.Version}, {ID: second.ID, ExpectedVersion: second.Version + 1}},
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale bulk error = %v", err)
	}
	for _, original := range []Comment{first, second} {
		current, getErr := store.Get(t.Context(), original.ID)
		if getErr != nil || current.Status != original.Status || current.Version != original.Version || current.ModeratedBy != "" {
			t.Fatalf("stale bulk partially changed %#v, err=%v", current, getErr)
		}
	}

	_, err = store.BulkChangeStatus(t.Context(), BulkStatusChange{
		Status: StatusSpam, Actor: "qaadmin",
		Items: []BulkMutation{{ID: first.ID, ExpectedVersion: first.Version}, {ID: 999999, ExpectedVersion: 1}},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing bulk error = %v", err)
	}
	currentFirst, _ := store.Get(t.Context(), first.ID)
	if currentFirst.Status != first.Status || currentFirst.Version != first.Version {
		t.Fatalf("missing bulk partially changed first = %#v", currentFirst)
	}

	updated, err := store.BulkChangeStatus(t.Context(), BulkStatusChange{
		Status: StatusApproved, Actor: " qaadmin ",
		Items: []BulkMutation{{ID: second.ID, ExpectedVersion: second.Version}, {ID: first.ID, ExpectedVersion: first.Version}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 2 || updated[0].ID != first.ID || updated[1].ID != second.ID {
		t.Fatalf("bulk result order = %#v", updated)
	}
	for _, item := range updated {
		if item.Status != StatusApproved || item.Version != 2 || item.ModeratedBy != "qaadmin" || item.ModeratedAt.IsZero() || item.UpdatedAt != item.ModeratedAt {
			t.Fatalf("bulk moderated item = %#v", item)
		}
	}

	reloaded, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range updated {
		persisted, getErr := reloaded.Get(t.Context(), item.ID)
		if getErr != nil || persisted.Status != StatusApproved || persisted.Version != 2 || persisted.ModeratedBy != "qaadmin" {
			t.Fatalf("reloaded bulk item = %#v, err=%v", persisted, getErr)
		}
	}
}
