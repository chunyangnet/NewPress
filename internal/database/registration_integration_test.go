package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
)

func TestPostgreSQLRegistrationIdentity(t *testing.T) {
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
	stamp := fmt.Sprintf("registration-%d", time.Now().UnixNano())
	phone := fmt.Sprintf("+86%011d", time.Now().UnixNano()%100000000000)
	digest := func(value string) string {
		sum := sha256.Sum256([]byte(stamp + value))
		return hex.EncodeToString(sum[:])
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(clean, `DELETE FROM np_users WHERE username LIKE $1`, stamp+"%")
	}()
	hash, err := auth.HashPassword("PostgreSQL registration fixture 2026")
	if err != nil {
		t.Fatal(err)
	}
	input := auth.UserInput{Username: stamp, RoleSlugs: []string{"subscriber"}, Phone: phone, PhoneVerified: true, ExternalKey: digest("external"), RegistrationKey: digest("request"), RegistrationDigest: digest("payload"), DisablePassword: true}
	input.OAuthProvider = "github"
	u, err := r.CreateUser(ctx, auth.UserMutation{}, input, hash)
	if err != nil {
		t.Fatal(err)
	}
	if u.PhoneVerifiedAt == nil || u.PasswordLogin || u.Email != "" || u.RegistrationVersion != u.Version {
		t.Fatal("new registration state mismatch")
	}
	if _, err = r.CreateUser(ctx, auth.UserMutation{}, auth.UserInput{Username: stamp + "-empty", RoleSlugs: []string{"subscriber"}}, hash); err != nil {
		t.Fatal("empty email uniqueness", err)
	}
	duplicate := input
	duplicate.Username += "-duplicate"
	duplicate.ExternalKey = ""
	duplicate.OAuthProvider = ""
	duplicate.RegistrationKey = ""
	duplicate.RegistrationDigest = ""
	if _, err = r.CreateUser(ctx, auth.UserMutation{}, duplicate, hash); !errors.Is(err, auth.ErrPhoneConflict) {
		t.Fatal("phone uniqueness", err)
	}
	found, err := r.FindUserByLogin(ctx, phone)
	if err != nil || found.ID != u.ID {
		t.Fatal("phone lookup", err)
	}
	input.Email = stamp + "@example.test"
	input.EmailVerified = true
	input.RegistrationKey = digest("bind")
	input.RegistrationDigest = digest("bind-payload")
	bound, err := r.BindRegistration(ctx, u.ID, u.Version, input, "")
	if err != nil {
		t.Fatal(err)
	}
	if bound.EmailVerifiedAt == nil || bound.PhoneVerifiedAt == nil || bound.PasswordLogin || bound.RegistrationVersion != bound.Version || !bound.HasRole("subscriber") {
		t.Fatal("binding state mismatch")
	}
	if _, err = r.BindRegistration(ctx, u.ID, u.Version, input, hash); !errors.Is(err, auth.ErrUserVersionConflict) {
		t.Fatal("stale bind", err)
	}
	input.EmailVerified, input.PhoneVerified = false, false
	preserved, err := r.BindRegistration(ctx, u.ID, bound.Version, input, "")
	if err != nil || preserved.EmailVerifiedAt == nil || !preserved.EmailVerifiedAt.Equal(*bound.EmailVerifiedAt) || preserved.PhoneVerifiedAt == nil || !preserved.PhoneVerifiedAt.Equal(*bound.PhoneVerifiedAt) || preserved.PasswordLogin || preserved.PasswordHash != bound.PasswordHash || !preserved.PasswordChangedAt.Equal(bound.PasswordChangedAt) {
		t.Fatal("unchanged contacts or omitted password lost their prior state", err)
	}
	input.Email = stamp + "-changed@example.test"
	input.Phone = ""
	bound, err = r.BindRegistration(ctx, u.ID, preserved.Version, input, hash)
	if err != nil || bound.EmailVerifiedAt != nil || bound.PhoneVerifiedAt != nil || !bound.PasswordLogin || bound.PasswordHash != hash || bound.RegistrationVersion != bound.Version || !bound.HasRole("subscriber") {
		t.Fatal("changed contacts retained verification or password binding failed", err)
	}
	changed, err := r.UpdateUserPassword(ctx, u.ID, auth.UserMutation{ExpectedVersion: bound.Version}, hash)
	if err != nil || !changed.PasswordLogin || changed.RegistrationVersion == changed.Version {
		t.Fatal("password reset did not invalidate receipt", err)
	}
	linked, err := r.UpdateOAuthBinding(ctx, changed.ID, changed.Version, "google", digest("google"))
	if err != nil || linked.OAuthBindings["google"] != digest("google") {
		t.Fatal("OAuth binding", err)
	}
	found, err = r.FindRegistration(ctx, "external", digest("google"))
	if err != nil || found.ID != linked.ID {
		t.Fatal("OAuth lookup", err)
	}
	duplicate = auth.UserInput{Username: stamp + "-oauth-conflict", RoleSlugs: []string{"subscriber"}, ExternalKey: digest("google")}
	duplicate.OAuthProvider = "google"
	if _, err = r.CreateUser(ctx, auth.UserMutation{}, duplicate, hash); !errors.Is(err, auth.ErrRegistrationConflict) {
		t.Fatal("binding registration uniqueness", err)
	}
	if _, err = r.UpdateOAuthBinding(ctx, linked.ID, linked.Version-1, "google", ""); !errors.Is(err, auth.ErrUserVersionConflict) {
		t.Fatal("stale OAuth removal", err)
	}
	if _, err = r.UpdateOAuthBinding(ctx, linked.ID, linked.Version, "google", ""); err != nil {
		t.Fatal("OAuth removal", err)
	}
	if _, err = r.FindRegistration(ctx, "external", digest("google")); !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatal("removed OAuth lookup", err)
	}
}
