package database

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/auth"
)

// A private schema isolates global capacity/revocation assertions from other
// packages' concurrent PostgreSQL integration fixtures.
func isolatedSessionPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("np_sessions_test_%d", time.Now().UnixNano())
	schema := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
		if err != nil {
			t.Errorf("drop isolated session schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("repeat session migration: %v", err)
	}
	return pool
}

func TestPostgreSQLPersistentSessionsCrossManagerRevocationAndPasswordReset(t *testing.T) {
	pool := isolatedSessionPool(t)
	ctx := t.Context()
	users := NewUserRepository(pool)
	const password = "PostgreSQL session password 2026"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	one, err := auth.NewWithContext(ctx, auth.Config{Username: "owner", PasswordHash: hash, Users: users, Sessions: NewSessionRepository(pool)})
	if err != nil {
		t.Fatal(err)
	}
	two, err := auth.NewWithContext(ctx, auth.Config{Users: NewUserRepository(pool), Sessions: NewSessionRepository(pool)})
	if err != nil {
		t.Fatal(err)
	}
	token, principal, err := one.AuthenticateContext(ctx, "ip", "owner", password)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := two.ResolveSession(ctx, token)
	if err != nil || loaded.UserID != principal.UserID || loaded.CSRFToken != principal.CSRFToken {
		t.Fatalf("independent manager did not share persisted identity/CSRF: %#v, %v", loaded, err)
	}
	other, _, err := two.AuthenticateContext(ctx, "other-ip", "owner", password)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := one.RevokeOtherSessionsContext(ctx, principal, token); err != nil || count != 1 {
		t.Fatalf("cross-manager revoke others = %d, %v", count, err)
	}
	if _, err := two.ResolveSession(ctx, other); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("other manager cached a revoked login: %v", err)
	}
	u, err := users.GetUser(ctx, principal.UserID)
	if err != nil {
		t.Fatal(err)
	}
	newHash, err := auth.HashPassword("Changed PostgreSQL session password 2026")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.UpdateUserPassword(ctx, u.ID, auth.UserMutation{ExpectedVersion: u.Version}, newHash); err != nil {
		t.Fatal(err)
	}
	if _, err := two.ResolveSession(ctx, token); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("offline credential reset left persisted login usable: %v", err)
	}
}

func TestPostgreSQLSessionQuotasAndRevocationAreAtomic(t *testing.T) {
	pool := isolatedSessionPool(t)
	ctx := t.Context()
	s := NewSessionRepository(pool)
	other := NewSessionRepository(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	key := func(value string) string {
		sum := sha256.Sum256([]byte(value))
		return base64.RawURLEncoding.EncodeToString(sum[:])
	}
	p := auth.SessionPolicy{Now: now, IdleTTL: time.Hour, MaxActive: 3, MaxPerIdentity: 2}
	record := func(i int, owner string) auth.SessionRecord {
		return auth.SessionRecord{ID: key(fmt.Sprint(i)), Username: owner, DisplayName: owner, RoleName: "author",
			CredentialDigest: key("credential"), CSRFToken: key(fmt.Sprint("csrf", i)),
			CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			if err := s.Create(ctx, record(i, "owner"), p); err != nil {
				t.Errorf("concurrent quota create: %v", err)
			}
		})
	}
	wg.Wait()
	items, err := other.List(ctx, now, p.IdleTTL)
	if err != nil || len(items) != 2 {
		t.Fatalf("cross-connection identity capacity = %d, %v", len(items), err)
	}
	for i := 12; i < 24; i++ {
		wg.Go(func() {
			if err := other.Create(ctx, record(i, fmt.Sprint("user", i)), p); err != nil {
				t.Errorf("concurrent global quota create: %v", err)
			}
		})
	}
	wg.Wait()
	items, err = s.List(ctx, now, p.IdleTTL)
	if err != nil || len(items) != 3 {
		t.Fatalf("cross-connection global capacity = %d, %v", len(items), err)
	}
	if len(items) == 0 {
		t.Fatal("no live session fixture")
	}
	id := items[0].ID
	for range 12 {
		wg.Go(func() {
			err := other.Touch(ctx, id, now.Add(time.Minute), p.IdleTTL)
			if err != nil && !errors.Is(err, auth.ErrSessionNotFound) {
				t.Errorf("concurrent SQL touch: %v", err)
			}
		})
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err := other.Get(ctx, id); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("SQL renewal resurrected a deleted login: %v", err)
	}
	if err := other.Touch(ctx, items[1].ID, now.Add(2*time.Hour), p.IdleTTL); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("SQL renewal accepted an absolutely expired login: %v", err)
	}
	// Delete all matching rows but do not include an expired login in the
	// operator-visible count of newly revoked active sessions.
	bulkPolicy := p
	bulkPolicy.IdleTTL, bulkPolicy.MaxActive = time.Minute, 10
	if err := s.Create(ctx, record(100, "bulk-owner"), bulkPolicy); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, record(101, "bulk-owner"), bulkPolicy); err != nil {
		t.Fatal(err)
	}
	if err := s.Touch(ctx, key("101"), now.Add(45*time.Second), bulkPolicy.IdleTTL); err != nil {
		t.Fatal(err)
	}
	count, err := other.DeleteUser(ctx, 0, "BULK-OWNER", "", now.Add(time.Minute), bulkPolicy.IdleTTL)
	if err != nil || count != 1 {
		t.Fatalf("SQL live revocation count = %d, %v", count, err)
	}
	for _, i := range []string{"100", "101"} {
		if _, err := s.Get(ctx, key(i)); !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("SQL bulk revoke retained an expired row: %v", err)
		}
	}
}
