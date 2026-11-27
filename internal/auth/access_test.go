package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/audit"
)

func TestFileAccessAtomicLifecycleAndAudit(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	ledger, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewFileUserStore(path, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureRoles(ctx, BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := HashPassword("Access testing password 2026")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	raw, token, err := NewAccessToken(AccessInvite, "private-reader@example.test", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.IssueAccess(ctx, token, "admin"); err != nil {
		t.Fatal(err)
	}
	_, recovery, _ := NewAccessToken(AccessRecover, token.Email, now.Add(time.Minute))
	if err = s.IssueAccess(ctx, recovery, "self-service"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LookupAccess(ctx, token.Hash, AccessInvite, now.Add(time.Minute)); err != nil {
		t.Fatalf("unrelated request invalidated invitation: %v", err)
	}
	input := UserInput{Username: "invited-reader", DisplayName: "读者", Status: UserStatusSuspended, RoleSlugs: []string{"administrator"}, Locale: "zh-CN"}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			_, err := s.RedeemAccess(ctx, token.Hash, AccessInvite, input, hash, now.Add(2*time.Minute))
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrAccessInvalid) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("redemptions=%d", successes.Load())
	}
	u, err := s.FindUserByLogin(ctx, input.Username)
	if err != nil {
		t.Fatal(err)
	}
	if !u.HasRole("subscriber") || u.HasRole("administrator") || u.Status != UserStatusActive || u.EmailVerifiedAt == nil || u.Email != token.Email {
		t.Fatalf("unexpected registered account: %#v", u)
	}
	u.EmailVerifiedAt = nil
	reloaded, err := NewFileUserStore(path, ledger)
	if err != nil {
		t.Fatal(err)
	}
	u, err = reloaded.GetUser(ctx, u.ID)
	if err != nil || u.EmailVerifiedAt == nil {
		t.Fatal("verification did not persist", err)
	}
	if _, err = reloaded.LookupAccess(ctx, token.Hash, AccessInvite, now.Add(3*time.Minute)); !errors.Is(err, ErrAccessInvalid) {
		t.Fatal("replayed persisted token", err)
	}
	users, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(users), raw) {
		t.Fatal("raw bearer token persisted")
	}
	log, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{raw, token.Hash, token.Email, hash} {
		if strings.Contains(string(log), secret) {
			t.Fatal("access secret leaked to audit")
		}
	}
}

func TestAccessFailureConflictExpiryAndLimits(t *testing.T) {
	ctx := t.Context()
	s, _ := NewFileUserStore("")
	_ = s.EnsureRoles(ctx, BuiltInRoles())
	hash, _ := HashPassword("Access failure fixture password")
	now := time.Now().UTC().Truncate(time.Second)
	_, token, _ := NewAccessToken(AccessRegister, "retry@example.test", now)
	if err := s.IssueAccess(ctx, token, "self-service"); err != nil {
		t.Fatal(err)
	}
	input := UserInput{Username: "retry-reader", DisplayName: "读者", Locale: "zh-CN"}
	s.path = filepath.Join(t.TempDir(), "blocked")
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemAccess(ctx, token.Hash, token.Kind, input, hash, now); err == nil {
		t.Fatal("expected save failure")
	}
	if _, err := s.LookupAccess(ctx, token.Hash, token.Kind, now); err != nil {
		t.Fatal("failed save consumed token", err)
	}
	if _, err := s.FindUserByLogin(ctx, input.Username); !errors.Is(err, ErrUserNotFound) {
		t.Fatal("failed save mutated user")
	}
	s.path = ""
	if _, err := s.RedeemAccess(ctx, token.Hash, token.Kind, input, hash, token.ExpiresAt); !errors.Is(err, ErrAccessInvalid) {
		t.Fatal("expired link accepted")
	}
	u, err := s.RedeemAccess(ctx, token.Hash, token.Kind, input, hash, now)
	if err != nil {
		t.Fatal(err)
	}
	_, conflict, _ := NewAccessToken(AccessRegister, "another@example.test", now)
	if err = s.IssueAccess(ctx, conflict, "self-service"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RedeemAccess(ctx, conflict.Hash, conflict.Kind, input, hash, now); !errors.Is(err, ErrUsernameConflict) {
		t.Fatal("expected username conflict", err)
	}
	if _, err = s.LookupAccess(ctx, conflict.Hash, conflict.Kind, now); err != nil {
		t.Fatal("conflict consumed token")
	}
	input.Username = "another-reader"
	if _, err = s.RedeemAccess(ctx, conflict.Hash, conflict.Kind, input, hash, now); err != nil {
		t.Fatal(err)
	}
	_, reset, _ := NewAccessToken(AccessRecover, u.Email, now.Add(time.Minute))
	reset.UserID, reset.UserVersion = u.ID, u.Version
	if err = s.IssueAccess(ctx, reset, "self-service"); err != nil {
		t.Fatal(err)
	}
	u, err = s.UpdateUser(ctx, u.ID, UserMutation{ExpectedVersion: u.Version}, UserInput{Username: u.Username, Email: u.Email, DisplayName: "新名字", Locale: u.Locale, Status: u.Status, RoleSlugs: []string{"subscriber"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RedeemAccess(ctx, reset.Hash, reset.Kind, UserInput{}, hash, now.Add(time.Minute)); !errors.Is(err, ErrAccessInvalid) {
		t.Fatal("stale version accepted", err)
	}
	for i := 1; i <= 5; i++ {
		_, next, _ := NewAccessToken(AccessRegister, "limited@example.test", now.Add(time.Duration(i)*time.Minute))
		if err = s.IssueAccess(ctx, next, "self-service"); err != nil {
			t.Fatal(i, err)
		}
		if err = s.IssueAccess(ctx, next, "self-service"); !errors.Is(err, ErrRateLimited) {
			t.Fatal("same-minute retry allowed")
		}
	}
	_, next, _ := NewAccessToken(AccessRecover, "limited@example.test", now.Add(7*time.Minute))
	if err = s.IssueAccess(ctx, next, "self-service"); !errors.Is(err, ErrRateLimited) {
		t.Fatal("hour limit bypassed by purpose", err)
	}
}

func TestRecoveryInvalidatesIndependentManagersAndChangedEmail(t *testing.T) {
	ctx := t.Context()
	s, _ := NewFileUserStore("")
	hash, _ := HashPassword("Independent admin password 2026")
	m, err := New(Config{Users: s, Username: "owner", PasswordHash: hash, BootstrapEmail: "owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(Config{Users: s})
	if err != nil {
		t.Fatal(err)
	}
	cookie, _, err := other.Authenticate("client", "owner", "Independent admin password 2026")
	if err != nil {
		t.Fatal(err)
	}
	raw, record, err := m.IssueAccess(ctx, AccessRecover, "owner@example.test", "self-service")
	if err != nil {
		t.Fatal(err)
	}
	u, err := m.CompleteAccess(ctx, raw, AccessRecover, UserInput{}, "New independent password 2026")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := other.ValidateSession(cookie); ok {
		t.Fatal("other manager kept pre-reset session")
	}
	if _, err = m.CompleteAccess(ctx, raw, AccessRecover, UserInput{}, "Another password 2026"); !errors.Is(err, ErrAccessInvalid) {
		t.Fatal("reset replay accepted", err)
	}
	if u.EmailVerifiedAt == nil {
		t.Fatal("email proof not stored")
	}
	u, err = s.UpdateUser(ctx, u.ID, UserMutation{ExpectedVersion: u.Version}, UserInput{Username: u.Username, Email: "changed@example.test", DisplayName: u.DisplayName, Locale: u.Locale, Status: u.Status, RoleSlugs: []string{"administrator"}})
	if err != nil {
		t.Fatal(err)
	}
	if u.EmailVerifiedAt != nil {
		t.Fatal("changed email retained verification")
	}
	if _, err = s.LookupAccess(context.Background(), record.Hash, record.Kind, time.Now()); !errors.Is(err, ErrAccessInvalid) {
		t.Fatal(err)
	}
}
