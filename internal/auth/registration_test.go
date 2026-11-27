package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
)

func TestFileRegistrationIdentityPersistenceAndBinding(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "users.json")
	store, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureRoles(ctx, BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := HashPassword("Registration fixture password 2026")
	if err != nil {
		t.Fatal(err)
	}
	digest := func(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }
	input := UserInput{Username: "oauth-reader", DisplayName: "读者", RoleSlugs: []string{"subscriber"}, Phone: "+8613800138000", PhoneVerified: true, ExternalKey: digest("provider:subject"), RegistrationKey: digest("browser:request"), RegistrationDigest: digest("payload"), DisablePassword: true}
	input.OAuthProvider = "github"
	u, err := store.CreateUser(ctx, UserMutation{}, input, hash)
	if err != nil {
		t.Fatal(err)
	}
	if u.PasswordLogin || u.PhoneVerifiedAt == nil || u.RegistrationVersion != u.Version {
		t.Fatal("registration flags were not saved")
	}
	if _, err = store.CreateUser(ctx, UserMutation{}, UserInput{Username: "empty-email-two", RoleSlugs: []string{"subscriber"}}, hash); err != nil {
		t.Fatal("multiple email-free accounts", err)
	}
	duplicate := input
	duplicate.Username = "phone-duplicate"
	duplicate.ExternalKey = ""
	duplicate.OAuthProvider = ""
	duplicate.RegistrationKey = ""
	duplicate.RegistrationDigest = ""
	if _, err = store.CreateUser(ctx, UserMutation{}, duplicate, hash); !errors.Is(err, ErrPhoneConflict) {
		t.Fatal("phone uniqueness", err)
	}
	duplicate.Phone = ""
	duplicate.ExternalKey = input.ExternalKey
	duplicate.OAuthProvider = input.OAuthProvider
	if _, err = store.CreateUser(ctx, UserMutation{}, duplicate, hash); !errors.Is(err, ErrRegistrationConflict) {
		t.Fatal("external identity uniqueness", err)
	}
	reopened, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal("reopen", err)
	}
	u, err = reopened.FindUserByLogin(ctx, input.Phone)
	if err != nil || u.PasswordLogin || u.OAuthBindings[input.OAuthProvider] != input.ExternalKey || u.RegistrationVersion != 1 {
		t.Fatal("reopened identity mismatch", err)
	}
	input.Email = "bound@example.test"
	input.EmailVerified = true
	input.RegistrationKey = digest("bind request")
	input.RegistrationDigest = digest("bind payload")
	bound, err := reopened.BindRegistration(ctx, u.ID, u.Version, input, "")
	if err != nil {
		t.Fatal(err)
	}
	if bound.PasswordLogin || bound.EmailVerifiedAt == nil || bound.PhoneVerifiedAt == nil || bound.RegistrationVersion != bound.Version {
		t.Fatal("binding lost existing verification or enabled a random password")
	}
	if _, err = reopened.BindRegistration(ctx, u.ID, u.Version, input, hash); !errors.Is(err, ErrUserVersionConflict) {
		t.Fatal("stale binding", err)
	}
	changed, err := reopened.UpdateUserPassword(ctx, u.ID, UserMutation{ExpectedVersion: bound.Version}, hash)
	if err != nil || !changed.PasswordLogin || changed.RegistrationVersion == changed.Version {
		t.Fatal("password rotation did not invalidate registration receipt", err)
	}
	if _, err = NewFileUserStore(path); err != nil {
		t.Fatal("binding/rotation cannot be reopened", err)
	}
}
