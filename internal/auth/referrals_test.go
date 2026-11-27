package auth

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFileReferralAtomicAdmissionAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "users.json")
	s, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureRoles(ctx, BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := HashPassword("Referral test password 2026")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.CreateUser(ctx, UserMutation{}, UserInput{Username: "owner", DisplayName: "Owner", Status: UserStatusActive, RoleSlugs: []string{"subscriber"}}, hash)
	if err != nil {
		t.Fatal(err)
	}
	p := DefaultReferralPolicy()
	p.Enabled = true
	p.CodeUses = 1
	p.RewardEnabled = true
	p.InviterReward = 100
	p.InviteeReward = 20
	d, err := s.GenerateReferral(ctx, owner.ID, p, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GenerateReferral(ctx, owner.ID, p, time.Now().UTC()); err == nil {
		t.Fatal("daily limit bypassed")
	}
	input := func(name string) UserInput {
		return UserInput{Username: name, DisplayName: name, Status: UserStatusActive, RoleSlugs: []string{"subscriber"}, ReferralHash: RegistrationCodeHash(d.Codes[0].Code), ReferralPolicy: p}
	}
	if _, err = s.CreateUser(ctx, UserMutation{}, input("owner"), hash); err == nil {
		t.Fatal("duplicate accepted")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"new-one", "new-two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := s.CreateUser(ctx, UserMutation{}, input(name), hash)
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("single-use code admitted %d users", success)
	}
	s, err = NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	users, err := s.ReferralUsers(ctx, owner.ID)
	if err != nil || len(users) != 1 {
		t.Fatal(users, err)
	}
	if users[0].Referral.InviterReward != 100 || users[0].Referral.InviteeReward != 20 {
		t.Fatal("reward snapshot lost", users[0].Referral)
	}
	owner, err = s.GetUser(ctx, owner.ID)
	if err != nil || owner.Referral.Codes[0].Used != 1 {
		t.Fatal("usage lost", err)
	}
	owner.Referral.Codes[0].Used = 999
	owner, _ = s.GetUser(ctx, owner.ID)
	if owner.Referral.Codes[0].Used != 1 {
		t.Fatal("snapshot mutated store")
	}
}

func TestReferralGenerationEligibility(t *testing.T) {
	now := time.Now().UTC()
	u := User{ID: 1, Status: UserStatusActive, CreatedAt: now.Add(-48 * time.Hour), Roles: []Role{{Slug: "subscriber"}}}
	p := DefaultReferralPolicy()
	p.Enabled = true
	for _, mutate := range []func(*ReferralPolicy){func(p *ReferralPolicy) { p.MinAgeDays = 3 }, func(p *ReferralPolicy) { p.VerifiedEmail = true }, func(p *ReferralPolicy) { p.VerifiedPhone = true }, func(p *ReferralPolicy) { p.Roles = []string{"author"} }} {
		q := p
		mutate(&q)
		if _, err := GenerateReferral(u, q, now); err == nil {
			t.Fatal("eligibility condition bypassed")
		}
	}
	d, err := GenerateReferral(u, p, now)
	if err != nil {
		t.Fatal(err)
	}
	u.Referral = d
	if _, _, err := ReferralAttribution(u, RegistrationCodeHash(d.Codes[0].Code), p, now.Add(31*24*time.Hour)); err == nil {
		t.Fatal("expired code accepted")
	}
	u.Referral.Codes[0].Enabled = false
	if _, _, err := ReferralAttribution(u, RegistrationCodeHash(d.Codes[0].Code), p, now); err == nil {
		t.Fatal("disabled code accepted")
	}
}
