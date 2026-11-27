package database

import (
	"fmt"
	"newpress.local/newpress/internal/auth"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPostgreSQLReferralAtomicAdmission(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
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
	stamp := fmt.Sprintf("referral-%d", time.Now().UnixNano())
	defer pool.Exec(ctx, `DELETE FROM np_users WHERE username LIKE $1`, stamp+"%")
	hash, err := auth.HashPassword("PostgreSQL referral password 2026")
	if err != nil {
		t.Fatal(err)
	}
	u, err := r.CreateUser(ctx, auth.UserMutation{}, auth.UserInput{Username: stamp, RoleSlugs: []string{"subscriber"}}, hash)
	if err != nil {
		t.Fatal(err)
	}
	p := auth.DefaultReferralPolicy()
	p.Enabled = true
	p.CodeUses = 1
	p.RewardEnabled = true
	p.InviterReward = 100
	d, err := r.GenerateReferral(ctx, u.ID, p, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := r.CreateUser(ctx, auth.UserMutation{}, auth.UserInput{Username: fmt.Sprintf("%s-%d", stamp, i), RoleSlugs: []string{"subscriber"}, ReferralHash: auth.RegistrationCodeHash(d.Codes[0].Code), ReferralPolicy: p}, hash)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	count := 0
	for err := range results {
		if err == nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("single-use invitation admitted %d users", count)
	}
	users, err := r.ReferralUsers(ctx, u.ID)
	if err != nil || len(users) != 1 || users[0].Referral.InviterReward != 100 {
		t.Fatal(users, err)
	}
}
