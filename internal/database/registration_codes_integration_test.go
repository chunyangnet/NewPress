package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
)

func TestPostgreSQLRegistrationCodeTransactions(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewUserRepository(pool)
	if err = r.EnsureRoles(ctx, auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	stamp := fmt.Sprintf("rc-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		clean, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		cleanupPool, e := Open(clean, dsn)
		if e != nil {
			return
		}
		defer cleanupPool.Close()
		_, _ = cleanupPool.Exec(clean, `DELETE FROM np_users WHERE username LIKE $1`, stamp+"%")
		_, _ = cleanupPool.Exec(clean, `DELETE FROM np_account_access WHERE email=$1`, stamp+"@example.test")
		_, _ = cleanupPool.Exec(clean, `DELETE FROM np_registration_codes WHERE code LIKE $1`, stamp+"%")
	})
	now := time.Now().UTC().Truncate(time.Second)
	codes, err := r.MutateRegistrationCodes(ctx, auth.RegistrationCodeMutation{Action: "create", Codes: []string{stamp + "-single", stamp + "-mail", stamp + "-expired"}, Rules: auth.RegistrationCodeRules{Enabled: true, MaxUses: 1}}, "test", now)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("PostgreSQL code test password 2026")
	if err != nil {
		t.Fatal(err)
	}
	input := func(name, code string) auth.UserInput {
		return auth.UserInput{Username: stamp + name, RoleSlugs: []string{"subscriber"}, RegistrationCodeHash: auth.RegistrationCodeHash(stamp + code)}
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := r.CreateUser(ctx, auth.UserMutation{}, input(fmt.Sprint("-race-", i), "-single"), hash)
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, auth.ErrRegistrationCodeInvalid) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("oversubscribed code", success)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_users WHERE username LIKE $1`, stamp+"-race-%").Scan(&count); err != nil || count != 1 {
		t.Fatal("transaction leaked users", count, err)
	}
	note := "stale update"
	_, err = r.MutateRegistrationCodes(ctx, auth.RegistrationCodeMutation{Action: "update", Targets: []auth.RegistrationCodeTarget{{ID: codes[1].ID, Version: 1}, {ID: codes[0].ID, Version: 1}}, Patch: auth.RegistrationCodePatch{Note: &note}}, "test", now)
	if !errors.Is(err, auth.ErrRegistrationCodeConflict) {
		t.Fatal("stale batch accepted", err)
	}
	_, token, err := auth.NewAccessToken(auth.AccessInvite, stamp+"@example.test", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.IssueAccess(ctx, token, "test"); err != nil {
		t.Fatal(err)
	}
	// Invalid admission rolls back the inserted user and leaves the mail token.
	if _, err = r.RedeemAccess(ctx, token.Hash, auth.AccessInvite, input("-failed-mail", "-single"), hash, now); !errors.Is(err, auth.ErrRegistrationCodeInvalid) {
		t.Fatal(err)
	}
	if _, err = r.LookupAccess(ctx, token.Hash, auth.AccessInvite, now); err != nil {
		t.Fatal("failed mail token consumed", err)
	}
	if _, err = r.RedeemAccess(ctx, token.Hash, auth.AccessInvite, input("-mail-user", "-mail"), hash, now); err != nil {
		t.Fatal(err)
	}
	if _, err = r.LookupAccess(ctx, token.Hash, auth.AccessInvite, now); !errors.Is(err, auth.ErrAccessInvalid) {
		t.Fatal("mail token unconsumed")
	}
	expires := now.Add(-time.Second).Format(time.RFC3339)
	if _, err = r.MutateRegistrationCodes(ctx, auth.RegistrationCodeMutation{Action: "update", Targets: []auth.RegistrationCodeTarget{{ID: codes[2].ID, Version: 1}}, Patch: auth.RegistrationCodePatch{ExpiresAt: &expires}}, "test", now); err != nil {
		t.Fatal(err)
	}
	if _, err = r.CreateUser(ctx, auth.UserMutation{}, input("-expired-user", "-expired"), hash); !errors.Is(err, auth.ErrRegistrationCodeInvalid) {
		t.Fatal("expired code accepted", err)
	}
	list, err := r.ListRegistrationCodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.ID == codes[1].ID && (c.Used != 1 || c.Note != "") {
			t.Fatal("batch rollback or usage lost")
		}
	}
}
