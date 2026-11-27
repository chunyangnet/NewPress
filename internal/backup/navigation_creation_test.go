package backup

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/navigation"
)

func TestBackupRestoresNavigationReceiptsIncludingDeletedMenu(t *testing.T) {
	fixture := fixtureSite(t, true)
	ledger, err := audit.Open(filepath.Join(fixture.cfg.DataDir, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	store, err := navigation.NewFileStore(filepath.Join(fixture.cfg.DataDir, "navigation.json"), false, ledger)
	if err != nil {
		t.Fatal(err)
	}
	live := navigation.Creation{Key: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), Input: navigation.MenuInput{Name: "Backup live", Slug: "backup-live"}}
	deleted := navigation.Creation{Key: strings.Repeat("c", 64), Fingerprint: strings.Repeat("d", 64), Input: navigation.MenuInput{Name: "Backup deleted", Slug: "backup-deleted"}}
	first, err := store.CreateOnce(t.Context(), live)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateOnce(t.Context(), deleted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(t.Context(), first.Receipt.MenuID, navigation.Mutation{ExpectedVersion: 1}, navigation.MenuInput{Name: "Current backup menu", Slug: "backup-live"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), second.Receipt.MenuID, navigation.Mutation{ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(fixture.parent, "navigation-receipts.zip")
	if _, err := Create(t.Context(), fixture.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(fixture.parent, "restored-navigation"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatal(restored, err)
	}
	dataDir := restored.Environment["NEWPRESS_DATA_DIR"]
	ledger, err = audit.Open(filepath.Join(dataDir, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	store, err = navigation.NewFileStore(filepath.Join(dataDir, "navigation.json"), false, ledger)
	if err != nil {
		t.Fatal(err)
	}
	for i, request := range []navigation.Creation{live, deleted} {
		want := []int64{first.Receipt.MenuID, second.Receipt.MenuID}[i]
		result, err := store.CreateOnce(t.Context(), request)
		if err != nil || !result.Replayed || result.Receipt.MenuID != want {
			t.Fatal(result, err)
		}
	}
	if menu, err := store.Get(t.Context(), first.Receipt.MenuID); err != nil || menu.Name != "Current backup menu" || menu.Version != 2 {
		t.Fatal(menu, err)
	}
	if _, err := store.Get(t.Context(), second.Receipt.MenuID); !errors.Is(err, navigation.ErrNotFound) {
		t.Fatal("deleted menu resurrected", err)
	}
	if next, err := store.Create(t.Context(), navigation.MenuInput{Name: "After restore", Slug: "after-navigation-restore"}, ""); err != nil || next.ID <= second.Receipt.MenuID {
		t.Fatal(next, err)
	}
}
