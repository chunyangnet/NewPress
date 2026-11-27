package navigation

import (
	"context"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/audit"
)

func TestFileNavigationStoreWritesAuditEvents(t *testing.T) {
	directory := t.TempDir()
	ledger, err := audit.Open(filepath.Join(directory, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(filepath.Join(directory, "navigation.json"), false, ledger)
	if err != nil {
		t.Fatal(err)
	}
	menu, err := store.Create(context.Background(), MenuInput{Name: "审计导航", Slug: "audit-navigation", Locations: []string{LocationFooter}}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	menu, err = store.Update(context.Background(), menu.ID, Mutation{ExpectedVersion: menu.Version, Actor: "owner"}, MenuInput{
		Name: "审计导航更新", Slug: "audit-navigation", Locations: []string{LocationFooter},
	})
	if err != nil {
		t.Fatal(err)
	}
	menu, err = store.ReplaceItems(context.Background(), menu.ID, Mutation{ExpectedVersion: menu.Version, Actor: "owner"}, []ItemInput{{
		ID: -1, Type: TypeCustom, Label: "首页", URL: "/", Target: TargetSelf, Presentation: PresentationStandard,
	}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := store.ReplaceAll(context.Background(), RegistryMutation{ExpectedRevision: SnapshotRevision(snapshot), Actor: "importer"}, []RegistryMenuInput{{
		Menu:  MenuInput{Name: "恢复导航", Slug: "restored-navigation", Locations: []string{LocationPrimary}},
		Items: []ItemInput{{ID: -1, Type: TypeCustom, Label: "恢复首页", URL: "/", Target: TargetSelf, Presentation: PresentationStandard}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), replaced.Menus[0].ID, Mutation{ExpectedVersion: replaced.Menus[0].Version, Actor: "owner"}); err != nil {
		t.Fatal(err)
	}
	actions := make(map[string]bool)
	for _, record := range ledger.Records() {
		if record.Phase == "commit" {
			actions[record.Action] = true
		}
	}
	for _, action := range []string{
		"navigation.menu.create", "navigation.menu.update", "navigation.items.replace", "navigation.registry.replace", "navigation.menu.delete",
	} {
		if !actions[action] {
			t.Fatalf("missing committed action %s in %#v", action, actions)
		}
	}
}
