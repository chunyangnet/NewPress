package navigation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/idempotency"
)

func creationDigest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

func TestCreationConcurrentReplayRestartAndAudit(t *testing.T) {
	directory := t.TempDir()
	ledger, err := audit.Open(filepath.Join(directory, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "navigation.json")
	store, err := NewFileStore(path, false, ledger)
	if err != nil {
		t.Fatal(err)
	}
	request := Creation{Key: creationDigest("key"), Fingerprint: creationDigest("body"), Input: MenuInput{Name: "Created once", Slug: "created-once", Locations: []string{LocationMobile}}, Actor: "owner"}
	start := make(chan struct{})
	results := make(chan CreationResult, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			result, err := store.CreateOnce(t.Context(), request)
			if err != nil {
				t.Error(err)
				return
			}
			results <- result
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	var menuID int64
	created := 0
	for result := range results {
		if menuID != 0 && result.Receipt.MenuID != menuID {
			t.Fatal("multiple menus", result)
		}
		menuID = result.Receipt.MenuID
		if !result.Replayed {
			created++
		}
	}
	if created != 1 || menuID != 1 {
		t.Fatal(created, menuID)
	}
	commits := 0
	for _, record := range ledger.Records() {
		if record.Phase == "commit" && record.Action == "navigation.menu.create" {
			commits++
		}
	}
	if commits != 1 {
		t.Fatal("duplicate audit", commits)
	}
	updated, err := store.Update(t.Context(), menuID, Mutation{ExpectedVersion: 1}, MenuInput{Name: "Latest menu", Slug: "latest-menu"})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileStore(path, false, ledger)
	if err != nil {
		t.Fatal(err)
	}
	// A replay is independent of now-invalid input and previously occupied slots.
	request.Input = MenuInput{}
	result, err := reopened.CreateOnce(t.Context(), request)
	if err != nil || !result.Replayed || result.Receipt.MenuID != updated.ID {
		t.Fatal(result, err)
	}
	current, err := reopened.Get(t.Context(), result.Receipt.MenuID)
	if err != nil || current.Version != 2 || current.Name != updated.Name {
		t.Fatal(current, err)
	}
	request.Fingerprint = creationDigest("changed")
	if _, err := reopened.CreateOnce(t.Context(), request); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatal(err)
	}
}

func TestCreationReceiptsSurviveDeleteAndReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "navigation.json")
	store, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	request := Creation{Key: creationDigest("removed"), Fingerprint: creationDigest("body"), Input: MenuInput{Name: "Removed", Slug: "removed"}}
	created, err := store.CreateOnce(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), created.Receipt.MenuID, Mutation{ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Snapshot(t.Context())
	replaced, err := store.ReplaceAll(t.Context(), RegistryMutation{ExpectedRevision: SnapshotRevision(snapshot)}, []RegistryMenuInput{{Menu: request.Input}})
	if err != nil || len(replaced.Menus) != 1 || replaced.Menus[0].ID <= created.Receipt.MenuID {
		t.Fatal(replaced, err)
	}
	reopened, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func() (CreationResult, error){
		func() (CreationResult, error) {
			return reopened.FindCreation(t.Context(), request.Key, request.Fingerprint)
		},
		func() (CreationResult, error) { return reopened.CreateOnce(t.Context(), request) },
	} {
		result, err := operation()
		if err != nil || !result.Replayed || result.Receipt.MenuID != created.Receipt.MenuID {
			t.Fatal(result, err)
		}
		if _, err := reopened.Get(t.Context(), result.Receipt.MenuID); !errors.Is(err, ErrNotFound) {
			t.Fatal("old key resolved to replacement menu", err)
		}
	}
	if next, err := reopened.Create(t.Context(), MenuInput{Name: "Next", Slug: "next"}, ""); err != nil || next.ID <= replaced.Menus[0].ID {
		t.Fatal(next, err)
	}
}

func TestCreationValidationAndFileWriteFailureLeaveNoPartialState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "navigation.json")
	store, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	request := Creation{Key: creationDigest("failure"), Fingerprint: creationDigest("body")}
	if _, err := store.CreateOnce(t.Context(), request); len(FieldErrors(err)) == 0 {
		t.Fatal(err)
	}
	if _, err := store.FindCreation(t.Context(), request.Key, request.Fingerprint); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	request.Input = MenuInput{Name: "Durable", Slug: "durable", Locations: []string{LocationPrimary}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.CreateOnce(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// An occupied target fails replacement on every supported platform.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateOnce(t.Context(), request); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	if len(store.menus) != 0 || len(store.creations) != 0 || store.nextMenuID != 1 {
		t.Fatal("partial candidate leaked")
	}
	if store.Ready(t.Context()) == nil {
		t.Fatal("uncertain writer stayed ready")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(t.Context(), request.Input, ""); err == nil {
		t.Fatal("stale writer continued after uncertain persistence")
	}
	reopened, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := reopened.CreateOnce(t.Context(), request); err != nil || result.Replayed || result.Receipt.MenuID != 1 {
		t.Fatal(result, err)
	}
}

func TestCreationDocumentAndReceiptValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "navigation.json")
	legacy := `{"schema":2,"kind":"newpress/navigation","next_menu_id":5,"next_item_id":1,"updated_at":"2026-01-01T00:00:00Z","menus":[]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	request := Creation{Key: creationDigest("upgrade"), Fingerprint: creationDigest("body"), Input: MenuInput{Name: "Upgrade", Slug: "upgrade"}}
	result, err := store.CreateOnce(t.Context(), request)
	if err != nil || result.Receipt.MenuID != 5 {
		t.Fatal(result, err)
	}
	document, err := loadFileDocument(path)
	if err != nil || document.Schema != 2 || len(document.Creations) != 1 {
		t.Fatal(document.Schema, len(document.Creations), err)
	}
	for name, change := range map[string]func(*fileDocument){
		"legacy with receipts": func(d *fileDocument) { d.Schema = 1 },
		"malformed digest":     func(d *fileDocument) { d.Creations[0].Key = "invalid" },
		"nonpositive menu":     func(d *fileDocument) { d.Creations[0].MenuID = 0 },
		"reused identity":      func(d *fileDocument) { d.NextMenuID = d.Creations[0].MenuID },
		"missing timestamp":    func(d *fileDocument) { d.Creations[0].CreatedAt = time.Time{} },
		"duplicate key":        func(d *fileDocument) { d.Creations = append(d.Creations, d.Creations[0]) },
		"duplicate menu": func(d *fileDocument) {
			other := d.Creations[0]
			other.Key = creationDigest("other")
			d.Creations = append(d.Creations, other)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := document
			candidate.Creations = append([]CreationReceipt(nil), document.Creations...)
			change(&candidate)
			body, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileStore(path, false); err == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
}

func TestCreationCapacityNeverEvictsOldReceipts(t *testing.T) {
	store, err := NewFileStore(filepath.Join(t.TempDir(), "navigation.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxCreationReceipts {
		store.creations = append(store.creations, CreationReceipt{Key: creationDigest(fmt.Sprint(i)), Fingerprint: strings.Repeat("b", 64), MenuID: int64(i + 1), CreatedAt: time.Now().UTC().AddDate(-10, 0, 0)})
	}
	store.nextMenuID = MaxCreationReceipts + 1
	// Persist the bounded registry to verify that it fits the file budget and
	// that receipts for deleted menus remain valid on startup.
	if _, err := store.Create(t.Context(), MenuInput{Name: "Ordinary", Slug: "ordinary"}, ""); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileStore(store.path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.CreateOnce(t.Context(), Creation{Key: creationDigest("full"), Fingerprint: strings.Repeat("b", 64), Input: MenuInput{Name: "Full", Slug: "full"}}); !errors.Is(err, idempotency.ErrCapacity) {
		t.Fatal(err)
	}
	if result, err := reopened.CreateOnce(t.Context(), Creation{Key: creationDigest("0"), Fingerprint: strings.Repeat("b", 64)}); err != nil || !result.Replayed || result.Receipt.MenuID != 1 {
		t.Fatal(result, err)
	}
}
