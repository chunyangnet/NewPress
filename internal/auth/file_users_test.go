package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFileUserStoreLifecyclePersistenceAndLastAdministrator(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "users.json")
	store, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureRoles(ctx, BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	passwordHash, err := HashPassword("Persistent admin password 2026")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.CreateUser(ctx, UserMutation{Actor: "bootstrap"}, UserInput{
		Username: "Admin", Email: "ADMIN@example.test", DisplayName: "站点管理员",
		Status: UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"administrator"},
	}, passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	if admin.ID != 1 || admin.Version != 1 || admin.Username != "admin" || admin.Email != "admin@example.test" || !admin.Can(CapabilityUsersManage) {
		t.Fatalf("created administrator = %#v", admin)
	}
	if _, err := store.CreateUser(ctx, UserMutation{}, UserInput{
		Username: "ADMIN", Email: "other@example.test", DisplayName: "重复账号",
		Status: UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"editor"},
	}, passwordHash); !errors.Is(err, ErrUsernameConflict) {
		t.Fatalf("duplicate username error = %v", err)
	}
	if _, err := store.UpdateUser(ctx, admin.ID, UserMutation{ExpectedVersion: admin.Version}, UserInput{
		Username: admin.Username, Email: admin.Email, DisplayName: admin.DisplayName,
		Status: UserStatusSuspended, Locale: admin.Locale, RoleSlugs: []string{"administrator"},
	}); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("last administrator suspension error = %v", err)
	}

	second, err := store.CreateUser(ctx, UserMutation{Actor: admin.Username}, UserInput{
		Username: "owner2", Email: "owner2@example.test", DisplayName: "第二管理员",
		Status: UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"administrator", "editor"},
	}, passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	admin, err = store.UpdateUser(ctx, admin.ID, UserMutation{ExpectedVersion: admin.Version, Actor: second.Username}, UserInput{
		Username: admin.Username, Email: admin.Email, DisplayName: admin.DisplayName,
		Status: UserStatusSuspended, Locale: admin.Locale, RoleSlugs: []string{"editor"},
	})
	if err != nil || admin.Status != UserStatusSuspended || admin.Version != 2 || admin.HasRole("administrator") {
		t.Fatalf("updated administrator = %#v, err=%v", admin, err)
	}
	if _, err := store.UpdateUser(ctx, admin.ID, UserMutation{ExpectedVersion: 1}, UserInput{
		Username: admin.Username, Email: admin.Email, DisplayName: admin.DisplayName,
		Status: admin.Status, Locale: admin.Locale, RoleSlugs: []string{"editor"},
	}); !errors.Is(err, ErrUserVersionConflict) {
		t.Fatalf("stale user version error = %v", err)
	}

	newPasswordHash, err := HashPassword("Rotated admin password 2026")
	if err != nil {
		t.Fatal(err)
	}
	second, err = store.UpdateUserPassword(ctx, second.ID, UserMutation{ExpectedVersion: second.Version, Actor: second.Username}, newPasswordHash)
	if err != nil || second.Version != 2 || second.PasswordChangedAt.IsZero() {
		t.Fatalf("password update = %#v, err=%v", second, err)
	}
	loginAt := time.Date(2026, time.August, 11, 12, 30, 0, 0, time.UTC)
	if err := store.RecordUserLogin(ctx, second.ID, loginAt); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.FindUserByLogin(ctx, "OWNER2@EXAMPLE.TEST")
	if err != nil || !loaded.LastLoginAt.Equal(loginAt) || loaded.Version != second.Version {
		t.Fatalf("login lookup = %#v, err=%v", loaded, err)
	}

	list, err := store.ListUsers(ctx, UserQuery{Search: "管理员", Role: "administrator", Limit: 20})
	if err != nil || list.Total != 1 || list.Items[0].ID != second.ID || list.StatusCounts[UserStatusSuspended] != 1 || list.RoleCounts["editor"] != 2 {
		t.Fatalf("user list = %#v, err=%v", list, err)
	}
	roles, err := store.ListRoles(ctx)
	if err != nil || len(roles) != 4 || roles[0].Slug != "administrator" || roles[0].UserCount != 1 {
		t.Fatalf("roles = %#v, err=%v", roles, err)
	}

	reloaded, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := reloaded.CountManagedUsers(ctx); err != nil || count != 2 {
		t.Fatalf("reloaded user count = %d, %v", count, err)
	}
	if user, err := reloaded.GetUser(ctx, second.ID); err != nil || user.Version != 2 || !user.Can(CapabilityAll) {
		t.Fatalf("reloaded user = %#v, %v", user, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("user document permissions = %v", info.Mode().Perm())
	}
}

func TestFileUserStoreRejectsMalformedDocumentsAndInputs(t *testing.T) {
	for name, payload := range map[string]string{
		"unknown field":   `{"schema":1,"kind":"newpress/users","next_user_id":1,"next_role_id":1,"updated_at":"2026-08-11T00:00:00Z","roles":[],"users":[],"unexpected":true}`,
		"duplicate field": `{"schema":1,"schema":1,"kind":"newpress/users","next_user_id":1,"next_role_id":1,"updated_at":"2026-08-11T00:00:00Z","roles":[],"users":[]}`,
		"trailing data":   `{"schema":1,"kind":"newpress/users","next_user_id":1,"next_role_id":1,"updated_at":"2026-08-11T00:00:00Z","roles":[],"users":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "users.json")
			if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileUserStore(path); err == nil {
				t.Fatal("accepted malformed user document")
			}
		})
	}

	store, err := NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureRoles(context.Background(), BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := HashPassword("Another persistent password 2026")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateUser(context.Background(), UserMutation{}, UserInput{
		Username: "bad user", Email: "not-an-email", DisplayName: strings.Repeat("名", 161),
		Status: "deleted", Locale: "bad locale", RoleSlugs: []string{"missing"},
	}, hash)
	fields := UserFieldErrors(err)
	for _, field := range []string{"username", "email", "display_name", "status", "locale"} {
		if fields[field] == "" {
			t.Fatalf("missing %s validation in %#v", field, fields)
		}
	}
	if _, err := store.ListUsers(context.Background(), UserQuery{Search: strings.Repeat("搜", 101)}); UserFieldErrors(err)["search"] == "" {
		t.Fatalf("long search error = %v", err)
	}
}

func TestFileUserStoreRecoversMissingPrimaryFromRollbackCopy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, "users.json")
	store, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureRoles(ctx, BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".bak"); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewFileUserStore(path)
	if err != nil {
		t.Fatalf("recover user document from rollback copy: %v", err)
	}
	roles, err := recovered.ListRoles(ctx)
	if err != nil || len(roles) != len(BuiltInRoles()) {
		t.Fatalf("recovered roles = %#v, %v", roles, err)
	}

	// The rollback copy is only an interruption fallback. A present but corrupt
	// primary remains authoritative and must fail closed.
	if err := os.WriteFile(path, []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileUserStore(path); err == nil {
		t.Fatal("corrupt primary user document fell back to rollback copy")
	}
}
