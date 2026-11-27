package database

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/idempotency"
	"newpress.local/newpress/internal/navigation"
)

func TestPostgreSQLNavigationAtomicCreation(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := pgx.Identifier{fmt.Sprintf("navigation_creation_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, err := admin.Exec(cleanup, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	config := admin.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal("migration replay", err)
	}
	otherPool, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	repository, other := NewNavigationRepository(pool), NewNavigationRepository(otherPool)
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	request := navigation.Creation{Key: digest("create"), Fingerprint: digest("body"), Input: navigation.MenuInput{Name: "Atomic menu", Slug: "atomic-menu", Locations: []string{navigation.LocationPrimary}}, Actor: "owner"}
	results := make(chan navigation.CreationResult, 8)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			store := repository
			if i%2 != 0 {
				store = other
			}
			<-start
			result, err := store.CreateOnce(ctx, request)
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
	created := 0
	var menuID int64
	for result := range results {
		if menuID != 0 && menuID != result.Receipt.MenuID {
			t.Fatal("multiple menu identities")
		}
		menuID = result.Receipt.MenuID
		if !result.Replayed {
			created++
		}
	}
	if created != 1 || menuID <= 0 {
		t.Fatal(created, menuID)
	}
	var menuCount, locationCount, auditCount int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_navigation_menus),(SELECT count(*) FROM np_navigation_locations),(SELECT count(*) FROM np_audit_events WHERE action='navigation.menu.create')`).Scan(&menuCount, &locationCount, &auditCount); err != nil || menuCount != 1 || locationCount != 1 || auditCount != 1 {
		t.Fatal(menuCount, locationCount, auditCount, err)
	}
	changed := request
	changed.Fingerprint = digest("changed")
	if _, err := other.CreateOnce(ctx, changed); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatal(err)
	}
	updated, err := repository.Update(ctx, menuID, navigation.Mutation{ExpectedVersion: 1}, navigation.MenuInput{Name: "Current", Slug: "current", Locations: []string{navigation.LocationMobileAction}})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := other.CreateOnce(ctx, request); err != nil || !result.Replayed || result.Receipt.MenuID != updated.ID {
		t.Fatal(result, err)
	}
	badge, _ := navigation.NormalizeBadge(navigation.Badge{Text: "最新", Style: "gradient", Radius: 6, OffsetY: -2, HideOnMobile: true})
	updated, err = repository.ReplaceItems(ctx, menuID, navigation.Mutation{ExpectedVersion: updated.Version}, []navigation.ItemInput{{ID: -1, Label: "发现", URL: "/explore/", Badge: badge}})
	if err != nil {
		t.Fatal("insert badge", err)
	}
	fromOther, err := other.Get(ctx, menuID)
	if err != nil || len(fromOther.Items) != 1 || fromOther.Items[0].Badge != badge {
		t.Fatal("JSONB badge round trip", fromOther, err)
	}
	badge.Text, badge.Style = "交流", "bubble"
	updated, err = repository.ReplaceItems(ctx, menuID, navigation.Mutation{ExpectedVersion: updated.Version}, []navigation.ItemInput{{ID: fromOther.Items[0].ID, Label: "发现", URL: "/explore/", Badge: badge}})
	if err != nil || updated.Items[0].Badge != badge {
		t.Fatal("update badge", updated, err)
	}
	// Receipt failure is injected after menu, location and audit writes.
	failing := navigation.Creation{Key: digest("rollback"), Fingerprint: digest("rollback-body"), Input: navigation.MenuInput{Name: "Rollback", Slug: "rollback", Locations: []string{navigation.LocationMobile}}}
	if _, err := pool.Exec(ctx, `ALTER TABLE np_navigation_creations ADD CONSTRAINT fail_receipt CHECK (key_hash <> '`+failing.Key+`')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateOnce(ctx, failing); err == nil {
		t.Fatal("receipt failure committed")
	}
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_navigation_menus),(SELECT count(*) FROM np_navigation_locations),(SELECT count(*) FROM np_audit_events WHERE action='navigation.menu.create')`).Scan(&menuCount, &locationCount, &auditCount); err != nil || menuCount != 1 || locationCount != 1 || auditCount != 1 {
		t.Fatal("partial commit", menuCount, locationCount, auditCount, err)
	}
	if _, err := other.FindCreation(ctx, failing.Key, failing.Fingerprint); !errors.Is(err, navigation.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE np_navigation_creations DROP CONSTRAINT fail_receipt`); err != nil {
		t.Fatal(err)
	}
	second, err := other.CreateOnce(ctx, failing)
	if err != nil || second.Replayed {
		t.Fatal(second, err)
	}
	if err := repository.Delete(ctx, menuID, navigation.Mutation{ExpectedVersion: updated.Version}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := repository.ReplaceAll(ctx, navigation.RegistryMutation{ExpectedRevision: navigation.SnapshotRevision(snapshot)}, []navigation.RegistryMenuInput{{Menu: request.Input, Items: []navigation.ItemInput{{ID: -1, Label: "导入", URL: "/", Badge: badge}}}})
	if err != nil || len(replacement.Menus) != 1 || replacement.Menus[0].ID <= second.Receipt.MenuID {
		t.Fatal(replacement, err)
	}
	if replacement.Menus[0].Items[0].Badge != badge {
		t.Fatal("registry replacement lost badge")
	}
	if _, err := pool.Exec(ctx, `UPDATE np_navigation_creations SET created_at=$1`, time.Now().AddDate(-10, 0, 0)); err != nil {
		t.Fatal(err)
	}
	for _, original := range []navigation.Creation{request, failing} {
		result, err := other.CreateOnce(ctx, original)
		if err != nil || !result.Replayed {
			t.Fatal(result, err)
		}
		if _, err := other.Get(ctx, result.Receipt.MenuID); !errors.Is(err, navigation.ErrNotFound) {
			t.Fatal("deleted/replaced menu resurrected", err)
		}
	}
	// Permanent receipts bound total keyed creations without evicting old keys.
	if _, err := pool.Exec(ctx, `INSERT INTO np_navigation_creations(key_hash,fingerprint,menu_id,created_at)
		SELECT md5('fixture-'||n)||md5('fixture-'||n),repeat('b',64),1000000+n,now() FROM generate_series(1,$1::integer) n`, navigation.MaxCreationReceipts-2); err != nil {
		t.Fatal(err)
	}
	if _, err := other.CreateOnce(ctx, navigation.Creation{Key: digest("capacity"), Fingerprint: digest("body"), Input: navigation.MenuInput{Name: "Overflow", Slug: "overflow"}}); !errors.Is(err, idempotency.ErrCapacity) {
		t.Fatal(err)
	}
	if result, err := other.CreateOnce(ctx, request); err != nil || !result.Replayed || result.Receipt.MenuID != menuID {
		t.Fatal(result, err)
	}
	t.Log("two pools, eight concurrent creates, atomic menu/location/audit rollback, deletion/import tombstones and permanent receipt capacity passed")
}
