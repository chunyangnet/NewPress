package database

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"newpress.local/newpress/internal/navigation"
)

func TestNavigationReplaceAllKeepsPostgreSQLAtomicContract(t *testing.T) {
	source, err := os.ReadFile("navigation.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "func (repository *NavigationRepository) ReplaceAll")
	end := strings.Index(text, "func (repository *NavigationRepository) Delete")
	if start < 0 || end <= start {
		t.Fatal("could not isolate NavigationRepository.ReplaceAll")
	}
	body := text[start:end]
	markers := []string{
		"repository.pool.Begin(ctx)",
		"defer tx.Rollback(ctx)",
		"pg_advisory_xact_lock",
		"lockNavigationRegistry(ctx, tx)",
		"loadNavigationMenus(ctx, tx)",
		"navigation.SnapshotRevision(current)",
		"DELETE FROM np_navigation_menus",
		"INSERT INTO np_navigation_menus",
		"INSERT INTO np_navigation_items",
		"UPDATE np_navigation_items SET parent_id",
		"recordNavigationRegistryAudit(ctx, tx",
		"tx.Commit(ctx)",
	}
	position := -1
	for _, marker := range markers {
		next := strings.Index(body, marker)
		if next < 0 {
			t.Fatalf("ReplaceAll lost transaction marker %q", marker)
		}
		if next <= position {
			t.Fatalf("ReplaceAll transaction marker %q is out of order", marker)
		}
		position = next
	}
	for _, forbidden := range []string{"repository.pool.Exec", "repository.pool.Query", "repository.pool.QueryRow"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("ReplaceAll escaped its transaction through %q", forbidden)
		}
	}
	if !strings.Contains(text, "'navigation.registry.replace','navigation_registry','all'") {
		t.Fatal("registry replacement audit action is missing")
	}
}

func TestNavigationSnapshotFromMenusCountsLocationsAndLatestUpdate(t *testing.T) {
	firstUpdate := time.Date(2026, time.August, 11, 8, 0, 0, 0, time.UTC)
	latestUpdate := firstUpdate.Add(2 * time.Minute)
	snapshot := navigationSnapshotFromMenus([]navigation.Menu{
		{
			ID: 7, Version: 2, Name: "主导航", Slug: "primary", Locations: []string{navigation.LocationPrimary, navigation.LocationMobile},
			UpdatedAt: firstUpdate,
			Items:     []navigation.Item{{ID: 70, MenuID: 7, Label: "首页", URL: "/"}, {ID: 71, MenuID: 7, Label: "发现", URL: "/explore/"}},
		},
		{
			ID: 8, Version: 1, Name: "页脚", Slug: "footer", Locations: []string{navigation.LocationFooter},
			UpdatedAt: latestUpdate,
			Items:     []navigation.Item{{ID: 80, MenuID: 8, Label: "关于", URL: "/pages/about/"}},
		},
	})
	if snapshot.MenuCount != 2 || snapshot.ItemCount != 3 || !snapshot.UpdatedAt.Equal(latestUpdate) ||
		snapshot.Locations[navigation.LocationPrimary] != 7 || snapshot.Locations[navigation.LocationMobile] != 7 ||
		snapshot.Locations[navigation.LocationFooter] != 8 {
		t.Fatalf("navigation database snapshot = %#v", snapshot)
	}
	if revision := navigation.SnapshotRevision(snapshot); len(revision) != 16 {
		t.Fatalf("navigation database snapshot revision = %q", revision)
	}
}

func TestTranslateNavigationUniqueConstraints(t *testing.T) {
	for name, test := range map[string]struct {
		constraint string
		want       error
	}{
		"slug":     {constraint: "np_navigation_menus_slug_key", want: navigation.ErrSlugConflict},
		"location": {constraint: "np_navigation_locations_pkey", want: navigation.ErrLocationConflict},
	} {
		t.Run(name, func(t *testing.T) {
			err := translateNavigationError(&pgconn.PgError{Code: "23505", ConstraintName: test.constraint})
			if !errors.Is(err, test.want) {
				t.Fatalf("translated %s constraint = %v", name, err)
			}
		})
	}
	original := &pgconn.PgError{Code: "23514", ConstraintName: "np_navigation_items_type_check"}
	if translated := translateNavigationError(original); translated != original {
		t.Fatalf("non-unique navigation error changed: %v", translated)
	}
}
