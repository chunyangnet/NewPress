package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileProfilePersistenceConflictAndIdentityIsolation(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "users.json")
	s, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureRoles(ctx, BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := HashPassword("Profile test password 2026")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, UserMutation{}, UserInput{Username: "profile-reader", Email: "private@example.test", DisplayName: "读者", Locale: "zh-CN", RoleSlugs: []string{"subscriber"}}, hash)
	if err != nil {
		t.Fatal(err)
	}
	input := ProfileInput{DisplayName: "新昵称", Locale: "en-US", Bio: " 第一行\r\n第二行 ", AvatarMediaID: 42}
	updated, err := s.UpdateProfile(ctx, u.ID, UserMutation{ExpectedVersion: u.Version}, input)
	if err != nil || updated.Bio != "第一行\n第二行" || updated.AvatarMediaID != 42 || updated.Username != u.Username || updated.Email != u.Email || updated.PasswordHash != u.PasswordHash || !updated.HasRole("subscriber") {
		t.Fatalf("profile=%+v err=%v", updated, err)
	}
	if _, err := s.UpdateProfile(ctx, u.ID, UserMutation{ExpectedVersion: u.Version}, input); !errors.Is(err, ErrUserVersionConflict) {
		t.Fatal(err)
	}
	reopened, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reopened.ListUsers(ctx, UserQuery{AvatarMediaID: 42})
	if err != nil || len(result.Items) != 1 || result.Items[0].Bio != updated.Bio {
		t.Fatal(result, err)
	}
	// A failed atomic write must retain both the old image reference and version.
	blocked := filepath.Join(t.TempDir(), "block")
	if err := os.WriteFile(blocked, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(blocked, "users.json")
	input.AvatarMediaID = 99
	if _, err := s.UpdateProfile(ctx, u.ID, UserMutation{ExpectedVersion: updated.Version}, input); err == nil {
		t.Fatal("failed storage accepted")
	}
	unchanged, _ := s.GetUser(ctx, u.ID)
	if unchanged.Version != updated.Version || unchanged.AvatarMediaID != 42 {
		t.Fatal("failed update changed memory")
	}
	for _, bio := range []string{strings.Repeat("字", 281), "hidden\x00value"} {
		input.Bio = bio
		if _, fields := NormalizeProfile(input); fields["bio"] == "" {
			t.Fatal("invalid biography accepted")
		}
	}
}
