package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPasswordWorkSaturationRejectsWithoutAccountOrSessionChanges(t *testing.T) {
	ctx := t.Context()
	password := "Password admission fixture 2026"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewWithContext(ctx, Config{Username: "owner", PasswordHash: hash, Users: store, BootstrapEmail: "owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.FindUserByLogin(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	raw, record, err := m.IssueAccess(ctx, AccessInvite, "invite@example.test", "owner")
	if err != nil {
		t.Fatal(err)
	}
	principal := principalFromUser(owner)
	input := UserInput{Username: "invited-reader", Email: "invite@example.test", DisplayName: "读者", Status: UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"subscriber"}}
	for range cap(m.verificationCh) {
		m.verificationCh <- struct{}{}
	}
	calls := []struct {
		name string
		run  func(context.Context) error
	}{
		{"login", func(ctx context.Context) error {
			_, _, err := m.AuthenticateContext(ctx, "source", "owner", password)
			return err
		}},
		{"registration", func(ctx context.Context) error { _, err := m.RegistrationPassword(ctx, password); return err }},
		{"invitation", func(ctx context.Context) error {
			_, err := m.CompleteAccess(ctx, raw, AccessInvite, input, password)
			return err
		}},
		{"create user", func(ctx context.Context) error { _, err := m.CreateUser(ctx, principal, input, password); return err }},
		{"rotate password", func(ctx context.Context) error {
			_, err := m.UpdateUserPassword(ctx, principal, owner.ID, owner.Version, password)
			return err
		}},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			// A deadline also makes a regression to blocking admission fail
			// promptly instead of waiting for the test process timeout.
			bounded, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if err := call.run(bounded); !errors.Is(err, ErrPasswordBusy) || !errors.Is(err, ErrRateLimited) {
				t.Fatalf("saturated operation = %v, want temporary password capacity limit", err)
			}
			cancelled, stop := context.WithCancel(ctx)
			stop()
			if err := call.run(cancelled); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled operation = %v, want cancellation", err)
			}
		})
	}
	if m.ActiveSessionCount() != 0 || len(m.attempts) != 0 {
		t.Fatal("overload or cancellation changed login state")
	}
	users, err := store.ListUsers(ctx, UserQuery{Limit: 20})
	if err != nil || users.Total != 1 || users.Items[0].Version != owner.Version || users.Items[0].PasswordHash != owner.PasswordHash {
		t.Fatal("overload changed the accounts", err)
	}
	if _, err := store.LookupAccess(ctx, record.Hash, AccessInvite, m.now()); err != nil {
		t.Fatal("overload consumed the invitation", err)
	}
	for range cap(m.verificationCh) {
		<-m.verificationCh
	}
	if _, _, err := m.AuthenticateContext(ctx, "source", "owner", password); err != nil {
		t.Fatal("login did not recover when capacity became available", err)
	}
}

func TestPasswordWorkCancellationAndValidationReleaseCapacity(t *testing.T) {
	m, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.RegistrationPassword(ctx, "valid password input"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled hash = %v", err)
	}
	if _, err := m.RegistrationPassword(t.Context(), "short"); len(UserFieldErrors(err)) == 0 {
		t.Fatalf("invalid password = %v", err)
	}
	if len(m.verificationCh) != 0 {
		t.Fatal("cancelled or invalid work leaked a password slot")
	}
}

type cancelLoginRecordStore struct {
	UserStore
	cancel context.CancelFunc
}

func (s cancelLoginRecordStore) RecordUserLogin(context.Context, int64, time.Time) error {
	s.cancel()
	return context.Canceled
}

func TestCancelledSuccessfulAuthenticationDoesNotCreateSession(t *testing.T) {
	password := "Cancelled successful login 2026"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m, err := NewWithContext(ctx, Config{Username: "owner", PasswordHash: hash, Users: cancelLoginRecordStore{store, cancel}, BootstrapEmail: "owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if token, _, err := m.AuthenticateContext(ctx, "source", "owner", password); token != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled authentication returned token=%t, err=%v", token != "", err)
	}
	if m.ActiveSessionCount() != 0 || len(m.verificationCh) != 0 {
		t.Fatal("cancelled login created a session or leaked password capacity")
	}
}
