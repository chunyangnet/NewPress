package httpserver

import (
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/virtualcurrency"
	"testing"
)

func TestReferralRegistrationAdmissionRewardsAndCheck(t *testing.T) {
	s, h, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	store, _ := currencyHTTPStore(t, s, false)
	p := site.DefaultRegistrationPolicy()
	p.EmailVerification = false
	p.Fields.Email = "hidden"
	p.Fields.InvitationCode = "required"
	rp := auth.DefaultReferralPolicy()
	rp.Enabled = true
	rp.CodeUses = 1
	rp.RewardEnabled = true
	rp.RewardVerifiedEmail = false
	rp.InviterReward = 100
	rp.InviteeReward = 20
	p.Referral = &rp
	setEnhancedRegistration(t, s, "invite", p)
	d, err := manager.GenerateReferral(t.Context(), owner.UserID, rp)
	if err != nil {
		t.Fatal(err)
	}
	browser := registrationBrowser(t, h)
	request, _ := auth.RandomCredential()
	input := registrationInput{RequestID: request, Username: "invited-reader", DisplayName: "受邀读者", Password: "Referral password 2026", ConfirmPassword: "Referral password 2026"}
	post := func(want int) {
		t.Helper()
		r := serveContentAPI(h, browser, "POST", "https://example.test/api/v1/social/access/register", registrationBody(t, input), registrationHeaders)
		if r.Code != want {
			t.Fatalf("want %d got %d: %s", want, r.Code, r.Body.String())
		}
	}
	post(422)
	input.InvitationCode = "I-invalid"
	post(422)
	input.InvitationCode = d.Codes[0].Code
	post(200)
	post(200)
	u, err := manager.FindRegistration(t.Context(), "username", input.Username)
	if err != nil {
		users, e := manager.ReferralUsers(t.Context(), owner.UserID)
		if e != nil || len(users) != 1 {
			t.Fatal(users, e)
		}
		u = users[0]
	}
	if u.Referral.InviterID != owner.UserID {
		t.Fatal("attribution missing")
	}
	for range 2 {
		if err = s.claimReferralRewards(t.Context(), owner.UserID); err != nil {
			t.Fatal(err)
		}
	}
	if balance, _ := store.Balance(t.Context(), owner.UserID); balance != 100 {
		t.Fatal("inviter reward duplicated", balance)
	}
	if balance, _ := store.Balance(t.Context(), u.ID); balance != 20 {
		t.Fatal("invitee reward duplicated", balance)
	}
	r := serveContentAPI(h, nil, "GET", "https://example.test/api/v1/admin/access/check", "", nil)
	if r.Code != 401 {
		t.Fatal(r.Code)
	}
	r = serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/access/check", "", nil)
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(r.Code, r.Body.String())
	}
	r = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/referrals", `{"action":"generate"}`, registrationHeaders)
	if r.Code != 403 {
		t.Fatal("missing CSRF accepted", r.Code)
	}
}

func TestReferralRewardsPartialFailureDoesNotBlockInvitee(t *testing.T) {
	s, _, manager, _, _, owner := newPersistentAdminTestServer(t)
	store, cfg := currencyHTTPStore(t, s, false)
	rp := auth.DefaultReferralPolicy()
	rp.Enabled, rp.RewardEnabled, rp.RewardVerifiedEmail = true, true, false
	rp.InviterReward, rp.InviteeReward = 100, 20
	d, err := manager.GenerateReferral(t.Context(), owner.UserID, rp)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := auth.RandomCredential()
	u, err := manager.RegisterAccount(t.Context(), auth.UserInput{Username: "partial-referral", ReferralHash: auth.RegistrationCodeHash(d.Codes[0].Code), ReferralPolicy: rp, RegistrationKey: secretDigest(key), RegistrationDigest: secretDigest(key)}, "Referral password 2026")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Credit(t.Context(), virtualcurrency.CreditInput{UserID: owner.UserID, Amount: cfg.MaxBalance, ExpectedVersion: cfg.Version, Source: "测试余额上限", RequestID: "referral_owner_balance_limit"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.claimReferralRewards(t.Context(), owner.UserID); err == nil {
		t.Fatal("expected inviter balance limit")
	}
	if balance, _ := store.Balance(t.Context(), u.ID); balance != 20 {
		t.Fatalf("eligible invitee blocked by inviter failure: %d", balance)
	}
	_ = s.claimReferralRewards(t.Context(), owner.UserID)
	if balance, _ := store.Balance(t.Context(), u.ID); balance != 20 {
		t.Fatal("retry duplicated reward", balance)
	}
}
