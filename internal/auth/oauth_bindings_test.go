package auth

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestFileOAuthBindingsPersistenceAndOwnership(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "users.json")
	s, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureRoles(ctx, BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := HashPassword("OAuth test password 2026!")
	if err != nil {
		t.Fatal(err)
	}
	key := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	u, err := s.CreateUser(ctx, UserMutation{}, UserInput{Username: "oauth-test", DisplayName: "OAuth", Locale: "zh-CN", Status: UserStatusActive, RoleSlugs: []string{"subscriber"}, DisablePassword: true, OAuthProvider: "github", ExternalKey: key("github")}, hash)
	if err != nil {
		t.Fatal(err)
	}
	u, err = s.UpdateOAuthBinding(ctx, u.ID, u.Version, "github", key("github"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateOAuthBinding(ctx, u.ID, u.Version, "github", ""); !errors.Is(err, ErrLastLoginMethod) {
		t.Fatal("last login", err)
	}
	u, err = s.UpdateOAuthBinding(ctx, u.ID, u.Version, "google", key("google"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateUser(ctx, UserMutation{}, UserInput{Username: "oauth-other", DisplayName: "Other", Locale: "zh-CN", Status: UserStatusActive, RoleSlugs: []string{"subscriber"}}, hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateOAuthBinding(ctx, other.ID, other.Version, "google", key("google")); !errors.Is(err, ErrRegistrationConflict) {
		t.Fatal("ownership", err)
	}
	if _, err = s.UpdateOAuthBinding(ctx, u.ID, u.Version-1, "github", ""); !errors.Is(err, ErrUserVersionConflict) {
		t.Fatal("stale", err)
	}
	u, err = s.UpdateOAuthBinding(ctx, u.ID, u.Version, "github", "")
	if err != nil {
		t.Fatal(err)
	}
	if u.OAuthBindings["github"] != "" {
		t.Fatal("binding retained after unlink")
	}
	s, err = NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	found, err := s.FindRegistration(ctx, "external", key("google"))
	if err != nil || found.ID != u.ID {
		t.Fatal("persisted lookup", err)
	}
	if _, err = s.FindRegistration(ctx, "external", key("github")); !errors.Is(err, ErrUserNotFound) {
		t.Fatal("unlinked login", err)
	}
	found.OAuthBindings["google"] = key("tampered")
	found, err = s.FindRegistration(ctx, "external", key("google"))
	if err != nil || found.OAuthBindings["google"] != key("google") {
		t.Fatal("map alias", err)
	}
}
