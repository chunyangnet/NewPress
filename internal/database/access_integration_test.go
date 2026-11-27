package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
)

func TestPostgreSQLAccountAccess(t *testing.T) {
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
	stamp := fmt.Sprintf("access-%d", time.Now().UnixNano())
	email := stamp + "@example.test"
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool.Exec(clean, `DELETE FROM np_account_access WHERE email=$1`, email)
		pool.Exec(clean, `DELETE FROM np_users WHERE email=$1`, email)
	}()
	now := time.Now().UTC().Truncate(time.Second)
	_, token, _ := auth.NewAccessToken(auth.AccessInvite, email, now)
	if err = r.IssueAccess(ctx, token, "integration"); err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.HashPassword("PostgreSQL access fixture password")
	input := auth.UserInput{Username: stamp, DisplayName: "邀请读者", Locale: "zh-CN", RoleSlugs: []string{"administrator"}}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Go(func() {
			_, err := r.RedeemAccess(ctx, token.Hash, token.Kind, input, hash, now)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, auth.ErrAccessInvalid) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("redemption count", successes.Load())
	}
	u, err := r.FindUserByLogin(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if !u.HasRole("subscriber") || u.HasRole("administrator") || u.EmailVerifiedAt == nil {
		t.Fatal("role or verification mismatch")
	}
	_, reset, _ := auth.NewAccessToken(auth.AccessRecover, email, now.Add(time.Minute))
	reset.UserID, reset.UserVersion = u.ID, u.Version
	if err = r.IssueAccess(ctx, reset, "integration"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.RedeemAccess(ctx, reset.Hash, reset.Kind, auth.UserInput{}, hash, reset.ExpiresAt); !errors.Is(err, auth.ErrAccessInvalid) {
		t.Fatal("expiry not enforced", err)
	}
	changed, err := r.RedeemAccess(ctx, reset.Hash, reset.Kind, auth.UserInput{}, hash, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if changed.Version != u.Version+1 {
		t.Fatal("password version not advanced")
	}
	if _, err = r.RedeemAccess(ctx, reset.Hash, reset.Kind, auth.UserInput{}, hash, now.Add(2*time.Minute)); !errors.Is(err, auth.ErrAccessInvalid) {
		t.Fatal("recovery replay", err)
	}
	_, invite, _ := auth.NewAccessToken(auth.AccessInvite, email, now.Add(3*time.Minute))
	if err = r.IssueAccess(ctx, invite, "integration"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.RedeemAccess(ctx, invite.Hash, invite.Kind, input, hash, now.Add(3*time.Minute)); !errors.Is(err, auth.ErrEmailConflict) && !errors.Is(err, auth.ErrUsernameConflict) {
		t.Fatal("expected conflict", err)
	}
	if _, err = r.LookupAccess(ctx, invite.Hash, invite.Kind, now.Add(3*time.Minute)); err != nil {
		t.Fatal("conflict consumed invitation", err)
	}
	if err = r.RevokeInvitation(ctx, invite.Hash, "integration", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = r.LookupAccess(ctx, invite.Hash, invite.Kind, now.Add(4*time.Minute)); !errors.Is(err, auth.ErrAccessInvalid) {
		t.Fatal("revoked link usable", err)
	}
	// Concurrent requests share one durable cooldown even across repository instances.
	_, next, _ := auth.NewAccessToken(auth.AccessRegister, email, now.Add(5*time.Minute))
	successes.Store(0)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			err := NewUserRepository(pool).IssueAccess(ctx, next, "integration")
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, auth.ErrRateLimited) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("concurrent mail cooldown", successes.Load())
	}
}
