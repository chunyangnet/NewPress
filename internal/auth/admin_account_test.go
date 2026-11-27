package auth

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAdministrativeAccountFieldsPersistAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	s, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureRoles(t.Context(), BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := HashPassword("Account editor test password 2026")
	if err != nil {
		t.Fatal(err)
	}
	in := UserInput{Username: "account-editor", Email: "editor@example.test", DisplayName: "原名称", RoleSlugs: []string{"author", "subscriber"}, Account: &UserAccountInput{Bio: "原简介", AvatarMediaID: 7, Phone: "+8613800138000", EmailVerified: true, PhoneVerified: true}}
	u, err := s.CreateUser(t.Context(), UserMutation{}, in, hash)
	if err != nil {
		t.Fatal(err)
	}
	if u.Bio != "原简介" || u.AvatarMediaID != 7 || u.PhoneVerifiedAt == nil || u.EmailVerifiedAt == nil {
		t.Fatalf("incomplete create: %+v", u)
	}
	in.DisplayName, in.Status = "新的名称", UserStatusSuspended
	in.Account = &UserAccountInput{Bio: "第一行\r\n第二行", AvatarMediaID: 0, Phone: "+8613800138001", EmailVerified: false, PhoneVerified: true}
	u, err = s.UpdateUser(t.Context(), u.ID, UserMutation{ExpectedVersion: u.Version, Actor: "owner"}, in)
	if err != nil {
		t.Fatal(err)
	}
	if u.Bio != "第一行\n第二行" || u.Status != UserStatusSuspended || u.AvatarMediaID != 0 || u.Phone != in.Account.Phone || u.EmailVerifiedAt != nil || u.PhoneVerifiedAt == nil || len(u.Roles) != 2 {
		t.Fatalf("incomplete update: %+v", u)
	}
	// An older identity-only caller must not erase new account fields.
	in.Account = nil
	u, err = s.UpdateUser(t.Context(), u.ID, UserMutation{ExpectedVersion: u.Version}, in)
	if err != nil || u.Bio != "第一行\n第二行" || u.Phone != "+8613800138001" || u.PhoneVerifiedAt == nil {
		t.Fatalf("legacy update: %+v %v", u, err)
	}
	other, err := s.CreateUser(t.Context(), UserMutation{}, UserInput{Username: "account-other", Email: "other@example.test", RoleSlugs: []string{"subscriber"}, Phone: "+8613800138002"}, hash)
	if err != nil {
		t.Fatal(err)
	}
	in.Account = &UserAccountInput{Bio: "不应保存", Phone: other.Phone}
	if _, err = s.UpdateUser(t.Context(), u.ID, UserMutation{ExpectedVersion: u.Version}, in); !errors.Is(err, ErrPhoneConflict) {
		t.Fatalf("phone conflict: %v", err)
	}
	reopened, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := reopened.GetUser(t.Context(), u.ID)
	if err != nil || saved.Version != u.Version || saved.Bio != u.Bio || saved.Phone != u.Phone {
		t.Fatalf("conflict persisted a partial edit: %+v %v", saved, err)
	}
	in.Account = &UserAccountInput{PhoneVerified: true}
	if _, fields := NormalizeUserInput(in); fields["phone_verified"] == "" {
		t.Fatal("verified empty phone accepted")
	}
}
