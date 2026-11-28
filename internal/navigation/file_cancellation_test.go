package navigation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/audit"
)

type navigationCancelFixture struct {
	store   *FileStore
	ledger  *audit.Ledger
	menu    Menu
	receipt Creation
}

func newNavigationCancelFixture(t *testing.T) navigationCancelFixture {
	t.Helper()
	directory := t.TempDir()
	ledger, err := audit.Open(filepath.Join(directory, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(filepath.Join(directory, "navigation.json"), false, ledger)
	if err != nil {
		t.Fatal(err)
	}
	receipt := Creation{Key: creationDigest("original"), Fingerprint: creationDigest("original-body"), Input: MenuInput{Name: "Original", Slug: "original", Locations: []string{LocationPrimary}}, Actor: "owner"}
	result, err := store.CreateOnce(t.Context(), receipt)
	if err != nil {
		t.Fatal(err)
	}
	menu, err := store.ReplaceItems(t.Context(), result.Receipt.MenuID, Mutation{ExpectedVersion: 1, Actor: "owner"}, []ItemInput{{ID: -1, Label: "Home", URL: "/"}})
	if err != nil {
		t.Fatal(err)
	}
	return navigationCancelFixture{store, ledger, menu, receipt}
}

func (f navigationCancelFixture) call(ctx context.Context, operation string) error {
	var err error
	mutation := Mutation{ExpectedVersion: f.menu.Version, Actor: "owner"}
	switch operation {
	case "ready":
		err = f.store.Ready(ctx)
	case "list":
		_, err = f.store.List(ctx)
	case "get":
		_, err = f.store.Get(ctx, f.menu.ID)
	case "location":
		_, err = f.store.GetLocation(ctx, LocationPrimary)
	case "snapshot":
		_, err = f.store.Snapshot(ctx)
	case "receipt":
		_, err = f.store.FindCreation(ctx, f.receipt.Key, f.receipt.Fingerprint)
	case "create":
		_, err = f.store.Create(ctx, MenuInput{Name: "Other", Slug: "other"}, "owner")
	case "create-once":
		_, err = f.store.CreateOnce(ctx, Creation{Key: creationDigest("other"), Fingerprint: creationDigest("other-body"), Input: MenuInput{Name: "Other", Slug: "other"}, Actor: "owner"})
	case "update":
		_, err = f.store.Update(ctx, f.menu.ID, mutation, MenuInput{Name: "Changed", Slug: "changed", Locations: []string{LocationFooter}})
	case "items":
		_, err = f.store.ReplaceItems(ctx, f.menu.ID, mutation, []ItemInput{{ID: -1, Label: "Changed", URL: "/changed/"}})
	case "delete":
		err = f.store.Delete(ctx, f.menu.ID, mutation)
	case "registry":
		before := snapshotFromMenus([]Menu{f.menu}, f.store.updatedAt)
		_, err = f.store.ReplaceAll(ctx, RegistryMutation{ExpectedRevision: SnapshotRevision(before), Actor: "owner"}, []RegistryMenuInput{{Menu: MenuInput{Name: "Changed", Slug: "changed"}}})
	}
	return err
}

func TestFileNavigationCancellationLeavesNoSideEffects(t *testing.T) {
	for _, operation := range []string{"ready", "list", "get", "location", "snapshot", "receipt", "create", "create-once", "update", "items", "delete", "registry"} {
		for _, boundary := range []string{"before", "waiting", "deadline"} {
			t.Run(operation+"/"+boundary, func(t *testing.T) {
				t.Parallel()
				f := newNavigationCancelFixture(t)
				before, err := f.store.Snapshot(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(f.store.path)
				if err != nil {
					t.Fatal(err)
				}
				auditBefore := f.ledger.Records()
				nextMenu, nextItem := f.store.nextMenuID, f.store.nextItemID
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				want := context.Canceled
				if boundary == "before" {
					cancel()
					err = f.call(ctx, operation)
				} else {
					f.store.mu.Lock()
					var once sync.Once
					unlock := func() { once.Do(f.store.mu.Unlock) }
					defer unlock()
					if boundary == "deadline" {
						var end context.CancelFunc
						ctx, end = context.WithTimeout(ctx, 25*time.Millisecond)
						defer end()
						want = context.DeadlineExceeded
					}
					started, done := make(chan struct{}), make(chan error, 1)
					go func() { close(started); done <- f.call(ctx, operation) }()
					<-started
					if boundary == "waiting" {
						cancel()
					}
					select {
					case err = <-done:
					case <-time.After(500 * time.Millisecond):
						t.Error("cancelled operation still waits for the writer lock")
						unlock()
						select {
						case err = <-done:
						case <-time.After(3 * time.Second):
							t.Fatal("operation did not drain after unlock")
						}
					}
					unlock()
				}
				if !errors.Is(err, want) {
					t.Errorf("cancelled %s: got %v, want %v", operation, err, want)
				}
				after, err := f.store.Snapshot(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				afterRaw, err := os.ReadFile(f.store.path)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) || !bytes.Equal(raw, afterRaw) || !reflect.DeepEqual(auditBefore, f.ledger.Records()) || f.store.nextMenuID != nextMenu || f.store.nextItemID != nextItem {
					t.Fatal("cancelled operation changed memory, durable file, identities or audit")
				}
				if f.store.persistFault != nil || f.store.auditFault != nil {
					t.Fatal("cancellation poisoned the store")
				}
				if err := f.call(t.Context(), operation); err != nil {
					t.Fatal("live retry failed", err)
				}
			})
		}
	}
}

func TestFileNavigationCancelledReadsReturnNoData(t *testing.T) {
	f := newNavigationCancelFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	list, listErr := f.store.List(ctx)
	menu, getErr := f.store.Get(ctx, f.menu.ID)
	location, locationErr := f.store.GetLocation(ctx, LocationPrimary)
	snapshot, snapshotErr := f.store.Snapshot(ctx)
	receipt, receiptErr := f.store.FindCreation(ctx, f.receipt.Key, f.receipt.Fingerprint)
	for _, err := range []error{listErr, getErr, locationErr, snapshotErr, receiptErr} {
		if !errors.Is(err, context.Canceled) {
			t.Error("cancelled read succeeded", err)
		}
	}
	if len(list) != 0 || menu.ID != 0 || location.ID != 0 || snapshot.MenuCount != 0 || len(snapshot.Menus) != 0 || receipt.Receipt.MenuID != 0 {
		t.Fatal("cancelled read returned private data")
	}
}

func TestFileNavigationReadersShareAndWriterWaits(t *testing.T) {
	f := newNavigationCancelFixture(t)
	f.store.mu.RLock()
	var once sync.Once
	unlock := func() { once.Do(f.store.mu.RUnlock) }
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	reads := make(chan error, 12)
	for range 12 {
		go func() { _, err := f.store.Snapshot(ctx); reads <- err }()
	}
	for range 12 {
		select {
		case err := <-reads:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("shared reader blocked")
		}
	}
	done := make(chan error, 1)
	go func() { done <- f.call(ctx, "update") }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("writer ignored active reader", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("writer did not cancel behind active reader")
		unlock()
		<-done
	}
	unlock()
	if err := f.call(t.Context(), "update"); err != nil {
		t.Fatal("cancelled writer leaked lock capacity", err)
	}
}
