package migrations

import (
	"strings"
	"testing"
)

func TestNavigationRuntimeMigrationDefinesLocationHierarchyAndSafetyBoundaries(t *testing.T) {
	source, err := Files.ReadFile("0007_navigation_runtime.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	checks := []string{
		"CREATE TABLE IF NOT EXISTS np_navigation_menus",
		"np_navigation_menus_slug_lower_uidx",
		"CREATE TABLE IF NOT EXISTS np_navigation_locations",
		"location IN ('primary', 'utility', 'mobile', 'footer')",
		"CREATE TABLE IF NOT EXISTS np_navigation_items",
		"FOREIGN KEY (menu_id, parent_id)",
		"DEFERRABLE INITIALLY DEFERRED",
		"item_type IN ('custom', 'post', 'page', 'category', 'tag')",
		"target IN ('self', 'blank')",
		"presentation IN ('standard', 'dropdown', 'panel', 'cards')",
		"np_navigation_items_parent_order_idx",
	}
	for _, check := range checks {
		if !strings.Contains(text, check) {
			t.Fatalf("migration missing %q", check)
		}
	}
}
