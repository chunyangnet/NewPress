package database

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/migrations"
)

// Each test gets a fresh schema and cleans it before the administrative pool
// closes. Cleanup uses its own context because testing cancels t.Context first.
func openSetupIntegrationPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	adminPool, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := fmt.Sprintf("np_setup_it_%d", time.Now().UTC().UnixNano())
	schemaIdentifier := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schemaIdentifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanup, "DROP SCHEMA "+schemaIdentifier+" CASCADE"); err != nil {
			t.Errorf("clean up bootstrap integration schema: %v", err)
		}
	})
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	return ctx, pool
}

func TestPostgreSQLFirstAdminClaimIsAtomicAndPermanent(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	users := NewUserRepository(pool)
	if err := users.EnsureRoles(ctx, auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	available, err := users.FirstAdminSetupAvailable(ctx)
	if err != nil || !available {
		t.Fatalf("initial setup availability = %t, %v", available, err)
	}
	passwordHash, err := auth.HashPassword("first admin integration password")
	if err != nil {
		t.Fatal(err)
	}
	const contenders = 8
	start := make(chan struct{})
	type claimResult struct {
		user auth.User
		err  error
	}
	results := make(chan claimResult, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			user, err := users.ClaimFirstAdministrator(ctx, auth.UserMutation{Actor: "first-admin-setup"}, auth.UserInput{
				Username: fmt.Sprintf("setup-owner-%d", index), DisplayName: fmt.Sprintf("Owner %d", index),
				Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"administrator"},
			}, passwordHash)
			results <- claimResult{user: user, err: err}
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	var winner auth.User
	winners := 0
	for result := range results {
		if result.err == nil {
			winner = result.user
			winners++
			continue
		}
		if !errors.Is(result.err, auth.ErrFirstAdminSetupClosed) {
			t.Fatalf("claim contender error = %v", result.err)
		}
	}
	if winners != 1 || winner.ID == 0 || !winner.HasRole("administrator") {
		t.Fatalf("claim winners = %d, winner = %#v", winners, winner)
	}
	available, err = NewUserRepository(pool).FirstAdminSetupAvailable(ctx)
	if err != nil || available {
		t.Fatalf("post-claim setup availability = %t, %v", available, err)
	}
	restarted, err := auth.NewWithContext(ctx, auth.Config{Users: NewUserRepository(pool)})
	if err != nil {
		t.Fatal(err)
	}
	if _, principal, err := restarted.AuthenticateContext(ctx, "setup-integration", winner.Username, "first admin integration password"); err != nil || principal.UserID != winner.ID || !principal.HasRole("administrator") {
		t.Fatalf("claimed administrator could not log in after restart: user=%d err=%v", principal.UserID, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE np_users SET status='system' WHERE id=$1`, winner.ID); err != nil {
		t.Fatal(err)
	}
	var managedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM np_users WHERE status <> 'system'`).Scan(&managedCount); err != nil {
		t.Fatal(err)
	}
	if managedCount != 0 {
		t.Fatalf("managed users after fixture cleanup = %d", managedCount)
	}
	available, err = users.FirstAdminSetupAvailable(ctx)
	if err != nil || available {
		t.Fatalf("setup reopened after managed users were removed = %t, %v", available, err)
	}
	if _, err := users.ClaimFirstAdministrator(ctx, auth.UserMutation{Actor: "first-admin-setup"}, setupAdministratorInput("replacement-owner"), passwordHash); !errors.Is(err, auth.ErrFirstAdminSetupClosed) {
		t.Fatalf("permanently closed installation accepted another claim: %v", err)
	}
}

func TestPostgreSQLFirstAdminClaimRollsBackOnAuditFailure(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	users := NewUserRepository(pool)
	if err := users.EnsureRoles(ctx, auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	passwordHash, err := auth.HashPassword("first admin rollback integration password")
	if err != nil {
		t.Fatal(err)
	}
	// Fail after the user insert has fired the installation-state trigger.
	// The account, role assignment, and durable gate must all roll back.
	if _, err := pool.Exec(ctx, `ALTER TABLE np_audit_events ADD CONSTRAINT np_setup_fixture_reject CHECK (actor_label <> 'first-admin-setup')`); err != nil {
		t.Fatal(err)
	}
	input := setupAdministratorInput("retry-owner")
	mutation := auth.UserMutation{Actor: "first-admin-setup"}
	if _, err := users.ClaimFirstAdministrator(ctx, mutation, input, passwordHash); err == nil || errors.Is(err, auth.ErrFirstAdminSetupClosed) {
		t.Fatalf("injected audit failure was not returned: %v", err)
	}
	if count, err := users.CountManagedUsers(ctx); err != nil || count != 0 {
		t.Fatalf("failed claim left an account behind: count=%d err=%v", count, err)
	}
	if available, err := users.FirstAdminSetupAvailable(ctx); err != nil || !available {
		t.Fatalf("failed claim permanently closed setup: available=%t err=%v", available, err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE np_audit_events DROP CONSTRAINT np_setup_fixture_reject`); err != nil {
		t.Fatal(err)
	}
	if user, err := users.ClaimFirstAdministrator(ctx, mutation, input, passwordHash); err != nil || !user.HasRole("administrator") {
		t.Fatalf("retry after rollback failed: administrator=%t err=%v", user.HasRole("administrator"), err)
	}
}

func TestPostgreSQLFirstAdminUpgradeKeepsExistingInstallationClosed(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if _, err := pool.Exec(ctx, `CREATE TABLE newpress_schema_migrations(version text PRIMARY KEY, checksum text NOT NULL DEFAULT '', applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		if name >= "0039_initial_admin_setup.sql" {
			break
		}
		source, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(source)); err != nil {
			t.Fatal(name, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO newpress_schema_migrations(version,checksum) VALUES($1,$2)`, name, fmt.Sprintf("%x", sha256.Sum256(source))); err != nil {
			t.Fatal(err)
		}
	}
	users := NewUserRepository(pool)
	passwordHash, err := auth.HashPassword("existing admin migration integration password")
	if err != nil {
		t.Fatal(err)
	}
	// Seed through the historical schema, not today's repository projection: it
	// includes columns added after 0039 and cannot read a pre-upgrade account.
	var existingID int64
	if err := pool.QueryRow(ctx, `INSERT INTO np_users(username,email,password_hash,display_name)
		VALUES ('existing-owner','existing-owner@example.test',$1,'Setup owner') RETURNING id`, passwordHash).Scan(&existingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO np_user_roles(user_id,role_id)
		SELECT $1,id FROM np_roles WHERE slug='administrator'`, existingID); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal("upgrade", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal("migration replay", err)
	}
	if existing, err := users.GetUser(ctx, existingID); err != nil || !existing.HasRole("administrator") || existing.Username != "existing-owner" {
		t.Fatalf("upgrade lost the existing administrator: %v", err)
	}
	// Remove the old account from the managed-user set before checking the
	// gate, so the test proves the migration itself remembered initialization.
	if _, err := pool.Exec(ctx, `UPDATE np_users SET status='system' WHERE id=$1`, existingID); err != nil {
		t.Fatal(err)
	}
	if available, err := users.FirstAdminSetupAvailable(ctx); err != nil || available {
		t.Fatalf("upgraded installation reopened setup: available=%t err=%v", available, err)
	}
	if _, err := users.ClaimFirstAdministrator(ctx, auth.UserMutation{Actor: "first-admin-setup"}, setupAdministratorInput("replacement-owner"), passwordHash); !errors.Is(err, auth.ErrFirstAdminSetupClosed) {
		t.Fatalf("upgraded installation accepted another claim: %v", err)
	}
}

func setupAdministratorInput(username string) auth.UserInput {
	return auth.UserInput{
		Username: username, DisplayName: "Setup owner", Status: auth.UserStatusActive,
		Locale: "zh-CN", RoleSlugs: []string{"administrator"},
	}
}
